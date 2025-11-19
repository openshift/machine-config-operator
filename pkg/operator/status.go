package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	opv1 "github.com/openshift/api/operator/v1"
	cov1helpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sversion "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	kcc "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
)

// syncVersion handles reporting the version to the clusteroperator
func (optr *Operator) syncVersion(co *configv1.ClusterOperator) {

	// keep the old version and progressing if we fail progressing
	if cov1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorProgressing) && cov1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorDegraded) {
		return
	}

	if !optr.vStore.Equal(co.Status.Versions) {
		mcoObjectRef := &corev1.ObjectReference{
			Kind:      co.Kind,
			Name:      co.Name,
			Namespace: co.Namespace,
			UID:       co.GetUID(),
		}
		optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeNormal, "OperatorVersionChanged", fmt.Sprintf("clusteroperator/machine-config version changed from %v to %v", co.Status.Versions, optr.vStore.GetAll()))
	}

	co.Status.Versions = optr.vStore.GetAll()
}

// syncRelatedObjects handles reporting the relatedObjects to the clusteroperator
func (optr *Operator) syncRelatedObjects(co *configv1.ClusterOperator) {

	coStatusCopy := co.Status.DeepCopy()
	// RelatedObjects are consumed by https://github.com/openshift/must-gather
	coStatusCopy.RelatedObjects = []configv1.ObjectReference{
		{Resource: "namespaces", Name: optr.namespace},
		{Group: "machineconfiguration.openshift.io", Resource: "machineconfigpools"},
		{Group: "machineconfiguration.openshift.io", Resource: "controllerconfigs"},
		{Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"},
		{Group: "machineconfiguration.openshift.io", Resource: "containerruntimeconfigs"},
		{Group: "machineconfiguration.openshift.io", Resource: "machineconfigs"},
		{Group: "operator.openshift.io", Resource: "machineconfigurations"},
		// gathered because the machineconfigs created container bootstrap credentials and node configuration that gets reflected via the API and is needed for debugging
		{Group: "", Resource: "nodes"},
		// Gathered for the on-prem services running in static pods.
		{Resource: "namespaces", Name: "openshift-kni-infra"},
		{Resource: "namespaces", Name: "openshift-openstack-infra"},
		{Resource: "namespaces", Name: "openshift-ovirt-infra"},
		{Resource: "namespaces", Name: "openshift-vsphere-infra"},
		{Resource: "namespaces", Name: "openshift-nutanix-infra"},
		{Resource: "namespaces", Name: "openshift-cloud-platform-infra"},
	}

	if !equality.Semantic.DeepEqual(coStatusCopy.RelatedObjects, co.Status.RelatedObjects) {
		co.Status = *coStatusCopy
	}

}

// syncAvailableStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncAvailableStatus(co *configv1.ClusterOperator) {

	// Based on Openshift Operator Guidance, Available = False is only necessary
	// if a midnight admin page is required. In the MCO land, nothing quite reaches
	// that level of severity. Most MCO errors typically fall into the degrade category
	// (which imply a working hours admin page)
	// See https://issues.redhat.com/browse/OCPBUGS-9108 for more information.

	coStatusCondition := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorAvailable,
		Status:  configv1.ConditionTrue,
		Message: fmt.Sprintf("Cluster has deployed %s", co.Status.Versions),
		Reason:  asExpectedReason,
	}

	cov1helpers.SetStatusCondition(&co.Status.Conditions, coStatusCondition, clock.RealClock{})
}

// syncProgressingStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncProgressingStatus(co *configv1.ClusterOperator) {

	var (
		optrVersion, _    = optr.vStore.Get("operator")
		coStatusCondition = configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorProgressing,
			Status:  configv1.ConditionFalse,
			Message: fmt.Sprintf("Cluster version is %s", optrVersion),
		}
		mcoObjectRef = &corev1.ObjectReference{
			Kind:      co.Kind,
			Name:      co.Name,
			Namespace: co.Namespace,
			UID:       co.GetUID(),
		}
	)
	if optr.vStore.Equal(co.Status.Versions) {
		if optr.inClusterBringup {
			optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeNormal, "OperatorVersionChanged", fmt.Sprintf("clusteroperator/machine-config is bootstrapping to %v", optr.vStore.GetAll()))
			coStatusCondition.Message = fmt.Sprintf("Cluster is bootstrapping %s", optrVersion)
			coStatusCondition.Status = configv1.ConditionTrue
		}
	} else {
		// we can still be progressing during a sync (e.g. wait for master pool sync)
		// but we want to fire the event only once when we're actually setting progressing and we
		// weren't progressing before.
		if !cov1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorProgressing) {
			optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeNormal, "OperatorVersionChanged", fmt.Sprintf("clusteroperator/machine-config started a version change from %v to %v", co.Status.Versions, optr.vStore.GetAll()))
		}
		coStatusCondition.Message = fmt.Sprintf("Working towards %s", optrVersion)
		coStatusCondition.Status = configv1.ConditionTrue
	}

	cov1helpers.SetStatusCondition(&co.Status.Conditions, coStatusCondition, clock.RealClock{})
}

// This function updates the Cluster Operator's status via an API call only if there is an actual
// update to the status. Returns the final ClusterOperator object and an error if any.
func (optr *Operator) updateClusterOperatorStatus(co *configv1.ClusterOperator, statusUpdate *configv1.ClusterOperatorStatus, syncErr error) (*configv1.ClusterOperator, error) {
	// Update the operator status extension. This fetchs all the MCP status, and appends a syncerror if any.
	optr.setOperatorStatusExtension(statusUpdate, syncErr)

	newCO := co.DeepCopy()
	newCO.Status = *statusUpdate

	if !reflect.DeepEqual(co.Status, newCO.Status) {
		// Attempt first update with the object passed in as it is likely to be the latest.
		klog.V(4).Infof("CO Status Update, old resource version %s", co.ResourceVersion)
		updatedCO, updateErr := optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(context.TODO(), newCO, metav1.UpdateOptions{})
		if apierrors.IsConflict(updateErr) {
			// On conflict, fetch a fresh CO object and attempt update again.
			co, getErr := optr.configClient.ConfigV1().ClusterOperators().Get(context.TODO(), co.Name, metav1.GetOptions{})
			if getErr != nil {
				return newCO, getErr
			}
			klog.V(4).Infof("CO Status Update conflict, re-attempting, new resource version %s", co.ResourceVersion)
			// Refresh updatedCO with the latest CO object and the new status to be applied.
			newCO = co.DeepCopy()
			newCO.Status = *statusUpdate
			// Attempt the update again.
			updatedCO, updateErr = optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(context.TODO(), newCO, metav1.UpdateOptions{})
		}
		// Return the old object, if any errors. This is because the update call will return an empty object if it fails.
		if updateErr != nil {
			return co, updateErr
		}
		// If no error, return the updated object.
		return updatedCO, nil
	}
	// If no update took place, return object as is.
	return co, nil
}

const (
	asExpectedReason = "AsExpected"
)

// This function clears a prior CO degrade condition set by a sync function. If the CO is not
// not degraded, or was degraded by another sync function, this will be a no-op.
func (optr *Operator) clearDegradedStatus(co *configv1.ClusterOperator, syncFn string) (*configv1.ClusterOperator, error) {
	if cov1helpers.IsStatusConditionFalse(co.Status.Conditions, configv1.OperatorDegraded) {
		return co, nil
	}
	degradedStatusCondition := cov1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorDegraded)
	if degradedStatusCondition == nil {
		return co, nil
	}
	if degradedStatusCondition.Reason != taskFailed(syncFn) {
		return co, nil
	}
	newCO := co.DeepCopy()
	// Clear the degraded by applying an empty sync error object
	optr.syncDegradedStatus(newCO, syncError{})
	return optr.updateClusterOperatorStatus(co, &newCO.Status, nil)
}

// syncDegradedStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncDegradedStatus(co *configv1.ClusterOperator, ierr syncError) {

	optrVersion, _ := optr.vStore.Get("operator")
	degraded := configv1.ConditionTrue
	var message, reason string
	if ierr.err == nil {
		degraded = configv1.ConditionFalse
	} else {
		if optr.vStore.Equal(co.Status.Versions) {
			// syncing the state to exiting version.
			message = fmt.Sprintf("Failed to resync %s because: %v", optrVersion, ierr.err.Error())
		} else {
			message = fmt.Sprintf("Unable to apply %s: %v", optrVersion, ierr.err.Error())
		}
		reason = taskFailed(ierr.task)

		// set progressing
		if cov1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorProgressing) {
			cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorProgressing,
				Status:  configv1.ConditionTrue,
				Message: fmt.Sprintf("Unable to apply %s", optrVersion),
			}, clock.RealClock{})
		} else {
			cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorProgressing,
				Status:  configv1.ConditionFalse,
				Message: fmt.Sprintf("Error while reconciling %s", optrVersion),
			}, clock.RealClock{})
		}
	}

	coDegradedCondition := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorDegraded,
		Status:  degraded,
		Message: message,
		Reason:  reason,
	}

	oldCODegradedCondition := cov1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorDegraded)
	// Only post an event if there is a change in the degraded condition message/reason and the status is being set to true
	// Otherwise, this is not a new condition, and posting an event will just spam the operator log/events every sync loop
	if ((oldCODegradedCondition == nil) || (oldCODegradedCondition.Message != coDegradedCondition.Message) || (oldCODegradedCondition.Reason != coDegradedCondition.Reason)) && degraded == configv1.ConditionTrue {
		degradedReason := fmt.Sprintf("OperatorDegraded: %s", reason)
		mcoObjectRef := &corev1.ObjectReference{
			Kind:      co.Kind,
			Name:      co.Name,
			Namespace: co.Namespace,
			UID:       co.GetUID(),
		}
		optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeWarning, degradedReason, message)

	}
	cov1helpers.SetStatusCondition(&co.Status.Conditions, coDegradedCondition, clock.RealClock{})
}

// syncUpgradeableStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncUpgradeableStatus(co *configv1.ClusterOperator) error {

	pools, err := optr.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}

	// Report default "Upgradeable=True" status. When known hazardous states for upgrades are
	// determined, specific "Upgradeable=False" status can be added with messages for how admins
	// can resolve it.
	// [ref] https://github.com/openshift/cluster-version-operator/blob/8402d219f36fc79e03edf45918785376113f2cc1/docs/dev/clusteroperator.md#what-should-an-operator-report-with-clusteroperator-custom-resource
	coStatusCondition := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorUpgradeable,
		Status: configv1.ConditionTrue,
		Reason: asExpectedReason,
	}

	// Check boot image skew upgradeable guards
	skewErrorExists, skewErrorMessage, err := optr.checkBootImageSkewUpgradeableGuard()
	if err != nil {
		return err
	}

	if skewErrorExists {
		coStatusCondition.Status = configv1.ConditionFalse
		coStatusCondition.Reason = "ClusterBootImageSkewError"
		coStatusCondition.Message = skewErrorMessage
	}

	var degraded, interrupted bool
	for _, pool := range pools {
		interrupted = isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolBuildInterrupted)

		degraded = isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolDegraded)
		// degraded should get top billing in the clusteroperator status, if we find this, set it and update
		if degraded {
			coStatusCondition.Status = configv1.ConditionFalse
			coStatusCondition.Reason = "DegradedPool"
			coStatusCondition.Message = "One or more machine config pools are degraded, please see `oc get mcp` for further details and resolve before upgrading"
			break
		}

		if interrupted {
			coStatusCondition.Status = configv1.ConditionFalse
			coStatusCondition.Reason = "InterruptedBuild"
			coStatusCondition.Message = "One or more machine config pools' builds have been interrupted, please see `oc get mcp` for further details and resolve before upgrading"
			break
		}
	}

	cov1helpers.SetStatusCondition(&co.Status.Conditions, coStatusCondition, clock.RealClock{})
	return nil
}

func (optr *Operator) syncClusterFleetEvaluation(co *configv1.ClusterOperator) error {

	unexpectedEvaluations, err := optr.generateClusterFleetEvaluations()
	if err != nil {
		return err
	}

	status := configv1.ConditionFalse
	reason := asExpectedReason
	if len(unexpectedEvaluations) > 0 {
		status = configv1.ConditionTrue
		reason = evaluationToString(unexpectedEvaluations)
	}

	coStatusCondition := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.EvaluationConditionsDetected,
		Status: status,
		Reason: reason,
	}

	cov1helpers.SetStatusCondition(&co.Status.Conditions, coStatusCondition, clock.RealClock{})
	return nil
}

func evaluationToString(e []string) string {
	if len(e) == 0 {
		return ""
	}
	return strings.Join(e, "::")
}

// generateClusterFleetEvaluations is used to flag items where we may consider setting upgradeable=false
// on the operator. We are also able to query telemetry for information on percentage of clusters migrated.
func (optr *Operator) generateClusterFleetEvaluations() ([]string, error) {
	evaluations := []string{}

	enabled, err := optr.cfeEvalFailSwapOn()
	if err != nil {
		return evaluations, err
	}
	if enabled {
		evaluations = append(evaluations, "failswapon: KubeletConfig setting is currently unsupported by OpenShift")
	}

	enabled, err = optr.cfeEvalRunc()
	if err != nil {
		return evaluations, err
	}
	if enabled {
		evaluations = append(evaluations, "runc: transition to default crun")
	}

	sort.Strings(evaluations)

	return evaluations, nil
}

func (optr *Operator) cfeEvalFailSwapOn() (bool, error) {
	// check for nil so we do not have to mock within tests
	if optr.mckLister == nil {
		return false, nil
	}
	kubeletConfigs, err := optr.mckLister.List(labels.Everything())
	if err != nil {
		return false, err
	}
	for _, kubeletConfig := range kubeletConfigs {
		if kubeletConfig.Spec.KubeletConfig == nil || kubeletConfig.Spec.KubeletConfig.Raw == nil {
			continue
		}
		decodedKC, err := kcc.DecodeKubeletConfig(kubeletConfig.Spec.KubeletConfig.Raw)
		if err != nil {
			klog.V(2).Infof("could not decode KubeletConfig: %v", err)
			continue
		}
		if decodedKC.FailSwapOn != nil && !*decodedKC.FailSwapOn {
			return true, nil
		}
	}
	return false, nil
}

func (optr *Operator) cfeEvalRunc() (bool, error) {
	// check for nil so we do not have to mock within tests
	if optr.crcLister == nil {
		return false, nil
	}
	containerConfigs, err := optr.crcLister.List(labels.Everything())
	if err != nil {
		return false, err
	}
	for _, containerConfig := range containerConfigs {
		if containerConfig.Spec.ContainerRuntimeConfig == nil {
			continue
		}
		if containerConfig.Spec.ContainerRuntimeConfig.DefaultRuntime == mcfgv1.ContainerRuntimeDefaultRuntimeRunc {
			return true, nil
		}
	}
	return false, nil
}

// GetAllManagedNodes returns the nodes managed by MCO
func (optr *Operator) GetAllManagedNodes(pools []*mcfgv1.MachineConfigPool) ([]*corev1.Node, error) {
	nodes := []*corev1.Node{}
	for _, pool := range pools {
		selector, err := metav1.LabelSelectorAsSelector(pool.Spec.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("label selector for pool %v failed %v", pool.Name, err)
		}
		poolNodes, err := optr.nodeLister.List(selector)
		if err != nil {
			return nil, fmt.Errorf("could not list nodes for pool %v with error %w", pool.Name, err)
		}
		nodes = append(nodes, poolNodes...)
	}
	return nodes, nil
}

func (optr *Operator) fetchClusterOperator() (*configv1.ClusterOperator, error) {
	co, err := optr.clusterOperatorLister.Get(optr.name)

	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	if apierrors.IsNotFound(err) {
		return optr.initializeClusterOperator()
	}
	if err != nil {
		return nil, err
	}
	return co, nil
}

func (optr *Operator) initializeClusterOperator() (*configv1.ClusterOperator, error) {
	co, err := optr.configClient.ConfigV1().ClusterOperators().Create(context.TODO(), &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: optr.name,
		},
	},
		metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse,
	}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse,
	}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse,
	}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorUpgradeable, Status: configv1.ConditionUnknown, Reason: "NoData",
	}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
		Type: configv1.EvaluationConditionsDetected, Status: configv1.ConditionFalse, Reason: asExpectedReason,
	}, clock.RealClock{})

	// RelatedObjects are consumed by https://github.com/openshift/must-gather
	co.Status.RelatedObjects = []configv1.ObjectReference{
		{Resource: "namespaces", Name: optr.namespace},
		{Group: "machineconfiguration.openshift.io", Resource: "machineconfigpools", Name: "master"},
		{Group: "machineconfiguration.openshift.io", Resource: "machineconfigpools", Name: "worker"},
		{Group: "machineconfiguration.openshift.io", Resource: "controllerconfigs", Name: "machine-config-controller"},
		{Group: "operator.openshift.io", Resource: "machineconfigurations"},
	}
	// During an installation we report the RELEASE_VERSION as soon as the component is created.
	// For both normal runs and upgrades, this code isn't hit and we get the right version every
	// time. This also only contains the operator RELEASE_VERSION when we're here.
	co.Status.Versions = optr.vStore.GetAll()
	return optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(context.TODO(), co, metav1.UpdateOptions{})
}

// setOperatorStatusExtension sets the raw extension field of the clusteroperator. Today, we set
// the MCPs statuses and an optional error status which we may get during a sync.
func (optr *Operator) setOperatorStatusExtension(status *configv1.ClusterOperatorStatus, statusErr error) {
	statuses, err := optr.allMachineConfigPoolStatus()
	if err != nil {
		klog.Error(err)
		return
	}
	if statusErr != nil {
		statuses["lastSyncError"] = statusErr.Error()
	}
	raw, err := json.Marshal(statuses)
	if err != nil {
		klog.Error(err)
		return
	}
	status.Extension.Raw = raw
}

func (optr *Operator) allMachineConfigPoolStatus() (map[string]string, error) {
	pools, err := optr.mcpLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	ret := map[string]string{}
	for _, pool := range pools {
		ret[pool.GetName()] = machineConfigPoolStatus(optr.fgHandler, pool)
	}
	return ret, nil
}

// isMachineConfigPoolConfigurationValid returns nil, or error when the configuration of a `pool` is created by the controller at version `version`,
// when the osImageURL does not match what's in the configmap or when the rendered-config-xxx does not match the OCP release version.
func isMachineConfigPoolConfigurationValid(fgHandler ctrlcommon.FeatureGatesHandler, pool *mcfgv1.MachineConfigPool, version, releaseVersion, osURL string, machineConfigGetter func(string) (*mcfgv1.MachineConfig, error)) error {
	// both .status.configuration.name and .status.configuration.source must be set.
	if pool.Spec.Configuration.Name == "" {
		return fmt.Errorf("configuration spec for pool %s is empty: %v", pool.GetName(), machineConfigPoolStatus(fgHandler, pool))
	}
	if pool.Status.Configuration.Name == "" {
		// if status is empty, it means the node controller hasn't seen any node at the target configuration
		// we bubble up any error from the pool to make the info more visible
		return fmt.Errorf("configuration status for pool %s is empty: %s", pool.GetName(), machineConfigPoolStatus(fgHandler, pool))
	}
	if len(pool.Status.Configuration.Source) == 0 {
		return fmt.Errorf("list of MachineConfigs that were used to generate configuration for pool %s is empty: %v", pool.GetName(), machineConfigPoolStatus(fgHandler, pool))
	}
	mcs := []string{pool.Status.Configuration.Name}
	for _, fragment := range pool.Status.Configuration.Source {
		mcs = append(mcs, fragment.Name)
	}
	for _, mcName := range mcs {
		mc, err := machineConfigGetter(mcName)
		if err != nil {
			return err
		}

		// We check that all of the machineconfigs (generated configs, as well as those that were used to create generated configs,
		// but not the user provided configs that don't have a version) were generated by the correct version of the controller.
		v, ok := mc.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey]
		// The generated machineconfig from fragments for the pool MUST have a version and an annotation.
		// The bootstrapped MCs fragments have this annotation, however, we don't fail (???) if they don't have
		// the annotation for some reason.
		if !ok && pool.Status.Configuration.Name == mcName {
			return fmt.Errorf("%s must be created by controller version %s: %v", mcName, version, machineConfigPoolStatus(fgHandler, pool))
		}
		// user provided MC fragments do not have the annotation, so we just skip the version check there.
		// The check below is for: 1) the generated MC for the pool, and 2) the bootstrapped fragments
		// that do have this annotation set with a version.
		if ok && v != version {
			return fmt.Errorf("controller version mismatch for %s expected %s has %s: %v", mcName, version, v, machineConfigPoolStatus(fgHandler, pool))
		}
	}
	// all MCs were generated by correct controller, but osImageURL is not a source, so let's double check here
	// to cover case where hashes match but there is an upgrade and avoid race where matching hashes pass before a new config is
	// rolled out
	renderedMC, err := machineConfigGetter(pool.Status.Configuration.Name)
	if err != nil {
		return err
	}

	// TODO(jkyros): For "Phase 0" layering, we're going to allow this check to pass once the user has "taken the wheel" by overriding OSImageURL.
	// We will find a way to make this more visible to the user somewhere since the MCO is kind of "lying" about completing the
	// upgrade to the new os version otherwise.
	if renderedMC.Spec.OSImageURL != osURL {
		// If we didn't override OSImageURL, this is still bad, because it means that we aren't on the proper OS image yet
		_, ok := renderedMC.Annotations[ctrlcommon.OSImageURLOverriddenKey]
		if !ok {
			return fmt.Errorf("osImageURL mismatch for %s in %s expected: %s got: %s", pool.GetName(), renderedMC.Name, osURL, renderedMC.Spec.OSImageURL)
		}
	}

	// check that the rendered config matches the OCP release version for cases where there is no OSImageURL change nor new MCO commit
	rv, ok := renderedMC.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey]
	if ok && rv != releaseVersion {
		return fmt.Errorf("release image version mismatch for %s in %s expected: %s got: %s", pool.GetName(), renderedMC.Name, releaseVersion, rv)
	}

	if !ok {
		return fmt.Errorf("Unable to access annotation %s for %s expected: %s", ctrlcommon.ReleaseImageVersionAnnotationKey, renderedMC.Name, releaseVersion)
	}

	return nil
}

func machineConfigPoolStatus(fgHandler ctrlcommon.FeatureGatesHandler, pool *mcfgv1.MachineConfigPool) string {
	switch {
	case apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded):
		cond := apihelpers.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolRenderDegraded)
		return fmt.Sprintf("pool is degraded because rendering fails with %q: %q", cond.Reason, cond.Message)
	case apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolNodeDegraded):
		cond := apihelpers.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolNodeDegraded)
		return fmt.Sprintf("pool is degraded because nodes fail with %q: %q", cond.Reason, cond.Message)
	case apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdated):
		return fmt.Sprintf("all %d nodes are at latest configuration %s", pool.Status.MachineCount, pool.Status.Configuration.Name)
	case apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating):
		return fmt.Sprintf("%d (ready %d) out of %d nodes are updating to latest configuration %s", pool.Status.UpdatedMachineCount, pool.Status.ReadyMachineCount, pool.Status.MachineCount, pool.Spec.Configuration.Name)
	default:
		if fgHandler.Enabled(features.FeatureGatePinnedImages) {
			if apihelpers.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolPinnedImageSetsDegraded) {
				cond := apihelpers.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolPinnedImageSetsDegraded)
				return fmt.Sprintf("pool is degraded because pinned image sets failed with %q: %q", cond.Reason, cond.Message)
			}
		}
		return "<unknown>"
	}
}

func taskFailed(task string) string {
	return task + "Failed"
}

// checkBootImageSkewUpgradeableGuard checks if the boot image version is within acceptable limits.
// It returns an error if there is no skew enforcement opinion specified. If one is specified,
// it checks if boot image skew is within the expected limit.
func (optr *Operator) checkBootImageSkewUpgradeableGuard() (bool, string, error) {
	// Check if feature gate is enabled
	if !optr.fgHandler.Enabled(features.FeatureGateBootImageSkewEnforcement) {
		return false, "", nil
	}

	// Fetch MachineConfiguration
	mcop, err := optr.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(4).Infof("MachineConfiguration not found, skipping boot image skew enforcement")
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to get MachineConfiguration: %w", err)
	}

	// Perform boot image skew enforcement based on mode
	skewLimitExceeded := false
	skewLimitExceededMessage := ""

	switch mcop.Status.BootImageSkewEnforcementStatus.Mode {
	case opv1.BootImageSkewEnforcementModeStatusAutomatic:
		skewLimitExceeded, skewLimitExceededMessage = checkBootImageSkew(
			mcop.Status.BootImageSkewEnforcementStatus.Automatic.OCPVersion,
			mcop.Status.BootImageSkewEnforcementStatus.Automatic.RHCOSVersion,
		)
	case opv1.BootImageSkewEnforcementModeStatusManual:
		skewLimitExceeded, skewLimitExceededMessage = checkBootImageSkew(
			mcop.Status.BootImageSkewEnforcementStatus.Manual.OCPVersion,
			mcop.Status.BootImageSkewEnforcementStatus.Manual.RHCOSVersion,
		)
	case opv1.BootImageSkewEnforcementModeStatusNone:
		// TODO: Set a low level prom alert to set scaling risk
		// Tracked in https://issues.redhat.com/browse/MCO-2035
		klog.V(4).Infof("evaluating boot image skew enforcement: mode set to None")
		return false, "", nil
	default:
		// Sanity check, this should only be possible if status hasn't been populated yet.
		return false, "", nil
	}

	if skewLimitExceeded {
		// TODO: Update error message; tracked in https://issues.redhat.com/browse/MCO-2034
		return true, fmt.Sprintf("Upgrades have been disabled because %s. To enable upgrades, please update your boot images following the documentation at [TODO: insert link], or disable boot image skew enforcement at [TODO: insert link]", skewLimitExceededMessage), nil
	}

	return false, "", nil
}

// checkBootImageSkew determines if the cluster's boot images are within acceptable version skew.
// It compares the oldest boot image version (currentOCPVersion, currentRHCOSVersion) against the minimum
// supported version.
// Returns true if the boot image version is older than the minimum, along with an error message.
func checkBootImageSkew(currentOCPVersion, currentRHCOSVersion string) (bool, string) {

	if currentOCPVersion != "" {
		return checkOCPVersionSkew(currentOCPVersion)
	}

	if currentRHCOSVersion != "" {
		return checkRHCOSVersionSkew(currentRHCOSVersion)
	}

	// This isn't possible due to API validations; more of a sanity check for safety
	klog.Warningf("no boot image versions provided, skipping skew check")
	return false, ""
}

// checkOCPVersionSkew compares a version string against the minimum supported version.
// Returns true if the version is below the minimum, along with an error message.
func checkOCPVersionSkew(version string) (bool, string) {
	// Parse the boot image version
	bootImageVersion, err := k8sversion.ParseGeneric(version)
	if err != nil {
		klog.Warningf("Failed to parse boot image version %q: %v", version, err)
		return false, ""
	}

	// Parse the minimum supported version
	minSupportedVersion, err := k8sversion.ParseGeneric(ctrlcommon.OCPVersionBootImageSkewLimit)
	if err != nil {
		klog.Errorf("Failed to parse OCPVersionBootImageSkewLimit constant %q: %v", ctrlcommon.OCPVersionBootImageSkewLimit, err)
		return false, ""
	}

	// Check if boot image version is less than the minimum supported version
	if bootImageVersion.LessThan(minSupportedVersion) {
		return true, fmt.Sprintf("the cluster is using OCP boot image version %s, which is below the minimum required version %s",
			version, ctrlcommon.OCPVersionBootImageSkewLimit)
	}

	klog.V(4).Infof("Boot image version %s meets minimum version requirement (>= %s)",
		version, ctrlcommon.OCPVersionBootImageSkewLimit)
	return false, ""
}

// checkRHCOSVersionSkew compares an RHCOS version string against the minimum supported version.
// Returns true if the version is below the minimum, along with an error message.
//
// Note: RHCOS versions can either have formatting of [major].[minor].[datestamp(YYYYMMDD)]-[buildnumber] (example:9.6.20251023-0) or the legacy
// format of [major].[minor].[timestamp(YYYYMMDDHHmm)]-[buildnumber] (example: 48.84.202208021106-0). In the modern(or RHEL) formatting, we just
// need to compare [major.minor] against the RHCOS skew limit. In the legacy format, the minor version includes the whole RHEL major/minor
// and only that bit should be used to compare against the RHCOS skew limit.
func checkRHCOSVersionSkew(version string) (bool, string) {
	// Split version to extract components
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		klog.Warningf("Failed to parse RHCOS version %q: expected at least 3 parts", version)
		return false, ""
	}

	major := parts[0]
	minor := parts[1]

	// Extract timestamp (remove build number suffix if present)
	timestampPart := parts[2]
	if idx := strings.Index(timestampPart, "-"); idx != -1 {
		timestampPart = timestampPart[:idx]
	}

	var versionToCompare string

	// Determine format based on timestamp length
	switch len(timestampPart) {
	case 8:
		// Modern format (YYYYMMDD): compare major.minor directly
		versionToCompare = fmt.Sprintf("%s.%s", major, minor)
	case 12:
		// Legacy format (YYYYMMDDHHmm): minor contains RHEL version (e.g., 84 = RHEL 8.4, 810 = RHEL 8.10)
		// First digit is RHEL major, remaining digits are RHEL minor.
		if len(minor) >= 2 {
			versionToCompare = fmt.Sprintf("%s.%s", minor[:1], minor[1:])
		} else {
			klog.Warningf("Failed to parse RHCOS legacy version %q: minor version too short", version)
			return false, ""
		}
	default:
		klog.Warningf("Failed to parse RHCOS version %q: unexpected timestamp format (length %d)", version, len(timestampPart))
		return false, ""
	}

	// Parse the version to compare
	bootImageVersion, err := k8sversion.ParseGeneric(versionToCompare)
	if err != nil {
		klog.Warningf("Failed to parse RHCOS version %q (extracted %q): %v", version, versionToCompare, err)
		return false, ""
	}

	// Parse the minimum supported version
	minSupportedVersion, err := k8sversion.ParseGeneric(ctrlcommon.RHCOSVersionBootImageSkewLimit)
	if err != nil {
		klog.Errorf("Failed to parse RHCOSVersionBootImageSkewLimit constant %q: %v", ctrlcommon.RHCOSVersionBootImageSkewLimit, err)
		return false, ""
	}

	// Check if boot image version is less than the minimum supported version
	if bootImageVersion.LessThan(minSupportedVersion) {
		return true, fmt.Sprintf("the cluster is using RHCOS boot image version %s(RHEL version: %s), which is below the minimum required RHEL version %s",
			version, versionToCompare, ctrlcommon.RHCOSVersionBootImageSkewLimit)
	}

	klog.V(4).Infof("RHCOS boot image version %s meets minimum version requirement (>= %s)",
		version, ctrlcommon.RHCOSVersionBootImageSkewLimit)
	return false, ""
}
