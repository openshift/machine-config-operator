package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/golang/glog"
	configv1 "github.com/openshift/api/config/v1"
	cov1helpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	v1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// syncVersion handles reporting the version to the clusteroperator
func (optr *Operator) syncVersion() error {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}

	// keep the old version and progressing if we fail progressing
	if cov1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorProgressing) && cov1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorDegraded) {
		return nil
	}

	if !optr.vStore.Equal(co.Status.Versions) {
		mcoObjectRef := &corev1.ObjectReference{
			Kind:      co.Kind,
			Name:      co.Name,
			Namespace: co.Namespace,
			UID:       co.GetUID(),
		}
		optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeNormal, "OperatorVersionChanged", fmt.Sprintf("clusteroperator/machine-config-operator version changed from %v to %v", co.Status.Versions, optr.vStore.GetAll()))
	}

	co.Status.Versions = optr.vStore.GetAll()
	// TODO(runcom): abstract below with updateStatus
	optr.setOperatorStatusExtension(&co.Status, nil)
	_, err = optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(context.TODO(), co, metav1.UpdateOptions{})
	return err
}

// syncRelatedObjects handles reporting the relatedObjects to the clusteroperator
func (optr *Operator) syncRelatedObjects() error {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}

	coCopy := co.DeepCopy()
	// RelatedObjects are consumed by https://github.com/openshift/must-gather
	co.Status.RelatedObjects = []configv1.ObjectReference{
		{Resource: "namespaces", Name: optr.namespace},
		{Group: "machineconfiguration.openshift.io", Resource: "machineconfigpools"},
		{Group: "machineconfiguration.openshift.io", Resource: "controllerconfigs"},
		{Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"},
		{Group: "machineconfiguration.openshift.io", Resource: "containerruntimeconfigs"},
		{Group: "machineconfiguration.openshift.io", Resource: "machineconfigs"},
		// gathered because the machineconfigs created container bootstrap credentials and node configuration that gets reflected via the API and is needed for debugging
		{Group: "", Resource: "nodes"},
		// Gathered for the on-prem services running in static pods.
		{Resource: "namespaces", Name: "openshift-kni-infra"},
		{Resource: "namespaces", Name: "openshift-openstack-infra"},
		{Resource: "namespaces", Name: "openshift-ovirt-infra"},
		{Resource: "namespaces", Name: "openshift-vsphere-infra"},
		{Resource: "namespaces", Name: "openshift-nutanix-infra"},
	}

	if !equality.Semantic.DeepEqual(coCopy.Status.RelatedObjects, co.Status.RelatedObjects) {
		_, err := optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(context.TODO(), co, metav1.UpdateOptions{})
		return err
	}

	return nil
}

// syncAvailableStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncAvailableStatus(ierr syncError) error {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}

	message := fmt.Sprintf("Cluster has deployed %s", co.Status.Versions)
	available := configv1.ConditionTrue

	// we will only be Available = False where there is a problem syncing
	// operands of the MCO as that points to impaired operator functionality.
	// RequiredPools failing but everything else being ok, should be just Degraded = True.
	if ierr.err != nil && ierr.task != "RequiredPools" {
		available = configv1.ConditionFalse
		mcoObjectRef := &corev1.ObjectReference{
			Kind:      co.Kind,
			Name:      co.Name,
			Namespace: co.Namespace,
			UID:       co.GetUID(),
		}

		optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeWarning, "OperatorNotAvailable", message)
		message = fmt.Sprintf("Cluster not available for %s", co.Status.Versions)
	}

	coStatus := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorAvailable,
		Status:  available,
		Message: message,
	}

	return optr.updateStatus(co, coStatus)
}

// syncProgressingStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncProgressingStatus() error {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}

	var (
		optrVersion, _ = optr.vStore.Get("operator")
		coStatus       = configv1.ClusterOperatorStatusCondition{
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
			optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeNormal, "OperatorVersionChanged", fmt.Sprintf("clusteroperator/machine-config-operator is bootstrapping to %v", optr.vStore.GetAll()))
			coStatus.Message = fmt.Sprintf("Cluster is bootstrapping %s", optrVersion)
			coStatus.Status = configv1.ConditionTrue
		}
	} else {
		// we can still be progressing during a sync (e.g. wait for master pool sync)
		// but we want to fire the event only once when we're actually setting progressing and we
		// weren't progressing before.
		if !cov1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorProgressing) {
			optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeNormal, "OperatorVersionChanged", fmt.Sprintf("clusteroperator/machine-config-operator started a version change from %v to %v", co.Status.Versions, optr.vStore.GetAll()))
		}
		coStatus.Message = fmt.Sprintf("Working towards %s", optrVersion)
		coStatus.Status = configv1.ConditionTrue
	}

	return optr.updateStatus(co, coStatus)
}

func (optr *Operator) updateStatus(co *configv1.ClusterOperator, status configv1.ClusterOperatorStatusCondition) error {
	cov1helpers.SetStatusCondition(&co.Status.Conditions, status)
	optr.setOperatorStatusExtension(&co.Status, nil)
	_, err := optr.configClient.ConfigV1().ClusterOperators().UpdateStatus(context.TODO(), co, metav1.UpdateOptions{})
	return err
}

const (
	asExpectedReason = "AsExpected"
)

func (optr *Operator) clearDegradedStatus(task string) error {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}
	if cov1helpers.IsStatusConditionFalse(co.Status.Conditions, configv1.OperatorDegraded) {
		return nil
	}
	degradedStatusCondition := cov1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorDegraded)
	if degradedStatusCondition == nil {
		return nil
	}
	if degradedStatusCondition.Reason != task+"Failed" {
		return nil
	}
	return optr.syncDegradedStatus(syncError{})
}

// syncDegradedStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncDegradedStatus(ierr syncError) (err error) {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}

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
		reason = ierr.task + "Failed"
		mcoObjectRef := &corev1.ObjectReference{
			Kind:      co.Kind,
			Name:      co.Name,
			Namespace: co.Namespace,
			UID:       co.GetUID(),
		}
		degradedReason := fmt.Sprintf("OperatorDegraded: %s", reason)
		optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeWarning, degradedReason, message)

		// set progressing
		if cov1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorProgressing) {
			cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorProgressing,
				Status:  configv1.ConditionTrue,
				Message: fmt.Sprintf("Unable to apply %s", optrVersion),
			})
		} else {
			cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorProgressing,
				Status:  configv1.ConditionFalse,
				Message: fmt.Sprintf("Error while reconciling %s", optrVersion),
			})
		}
	}

	coStatus := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorDegraded,
		Status:  degraded,
		Message: message,
		Reason:  reason,
	}

	return optr.updateStatus(co, coStatus)
}

const (
	skewUnchecked   = "KubeletSkewUnchecked"
	skewSupported   = "KubeletSkewSupported"
	skewUnsupported = "KubeletSkewUnsupported"
	skewPresent     = "KubeletSkewPresent"
)

// syncUpgradeableStatus applies the new condition to the mco's ClusterOperator object.
func (optr *Operator) syncUpgradeableStatus() error {
	co, err := optr.fetchClusterOperator()
	if err != nil {
		return err
	}
	if co == nil {
		return nil
	}

	pools, err := optr.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}

	// Report default "Upgradeable=True" status. When known hazardous states for upgrades are
	// determined, specific "Upgradeable=False" status can be added with messages for how admins
	// can resolve it.
	// [ref] https://github.com/openshift/cluster-version-operator/blob/8402d219f36fc79e03edf45918785376113f2cc1/docs/dev/clusteroperator.md#what-should-an-operator-report-with-clusteroperator-custom-resource
	coStatus := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorUpgradeable,
		Status: configv1.ConditionTrue,
		Reason: asExpectedReason,
	}
	var updating, degraded bool
	for _, pool := range pools {
		// collect updating status but continue to check each pool to see if any pool is degraded
		if isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolUpdating) {
			updating = true
		}
		degraded = isPoolStatusConditionTrue(pool, mcfgv1.MachineConfigPoolDegraded)
		// degraded should get top billing in the clusteroperator status, if we find this, set it and update
		if degraded {
			coStatus.Status = configv1.ConditionFalse
			coStatus.Reason = "DegradedPool"
			coStatus.Message = "One or more machine config pools are degraded, please see `oc get mcp` for further details and resolve before upgrading"
			break
		}
	}
	// updating and degraded can occur together, in that case defer to the degraded Reason that is already set above
	if updating && !degraded {
		coStatus.Status = configv1.ConditionFalse
		coStatus.Reason = "PoolUpdating"
		coStatus.Message = "One or more machine config pools are updating, please see `oc get mcp` for further details"
	}

	// don't overwrite status if updating or degraded
	if !updating && !degraded {
		skewStatus, status, err := optr.isKubeletSkewSupported(pools)
		if err != nil {
			glog.Errorf("Error checking version skew: %v, kubelet skew status: %v, status reason: %v, status message: %v", err, skewStatus, status.Reason, status.Message)
			coStatus.Reason = status.Reason
			coStatus.Message = status.Message
			return optr.updateStatus(co, coStatus)
		}
		switch skewStatus {
		case skewUnchecked:
			coStatus.Reason = status.Reason
			coStatus.Message = status.Message
			return optr.updateStatus(co, coStatus)
		case skewUnsupported:
			coStatus.Reason = status.Reason
			coStatus.Message = status.Message
			mcoObjectRef := &corev1.ObjectReference{
				Kind:      co.Kind,
				Name:      co.Name,
				Namespace: co.Namespace,
				UID:       co.GetUID(),
			}
			glog.Infof("kubelet skew status: %v, status reason: %v", skewStatus, status.Reason)
			optr.eventRecorder.Eventf(mcoObjectRef, corev1.EventTypeWarning, coStatus.Reason, coStatus.Message)
			return optr.updateStatus(co, coStatus)
		case skewPresent:
			coStatus.Reason = status.Reason
			coStatus.Message = status.Message
			glog.Infof("kubelet skew status: %v, status reason: %v", skewStatus, status.Reason)
			return optr.updateStatus(co, coStatus)
		}
	}
	return optr.updateStatus(co, coStatus)
}

// isKubeletSkewSupported checks the version skew of kube-apiserver and node kubelet version.
// Returns the skew status. version skew > 2 is not supported.
func (optr *Operator) isKubeletSkewSupported(pools []*v1.MachineConfigPool) (skewStatus string, coStatus configv1.ClusterOperatorStatusCondition, err error) {
	coStatus = configv1.ClusterOperatorStatusCondition{}
	kubeAPIServerStatus, err := optr.configClient.ConfigV1().ClusterOperators().Get(context.TODO(), "kube-apiserver", metav1.GetOptions{})
	if err != nil {
		coStatus.Reason = skewUnchecked
		coStatus.Message = fmt.Sprintf("An error occurred when checking kubelet version skew: %v", err)
		return skewUnchecked, coStatus, err
	}
	// looks like
	//   - name: kube-apiserver
	//     version: 1.21.0-rc.0
	kubeAPIServerVersion := ""
	for _, version := range kubeAPIServerStatus.Status.Versions {
		if version.Name != "kube-apiserver" {
			continue
		}
		kubeAPIServerVersion = version.Version
		break
	}
	if kubeAPIServerVersion == "" {
		err = fmt.Errorf("kube-apiserver does not yet have a version")
		coStatus.Reason = skewUnchecked
		coStatus.Message = fmt.Sprintf("An error occurred when checking kubelet version skew: %v", err.Error())
		return skewUnchecked, coStatus, err
	}
	kubeAPIServerMinorVersion, err := getMinorKubeletVersion(kubeAPIServerVersion)
	if err != nil {
		coStatus.Reason = skewUnchecked
		coStatus.Message = fmt.Sprintf("An error occurred when checking kubelet version skew: %v", err)
		return skewUnchecked, coStatus, err
	}
	var (
		lastError      error
		kubeletVersion string
	)
	nodes, err := optr.GetAllManagedNodes(pools)
	if err != nil {
		err = fmt.Errorf("getting all managed nodes failed: %w", err)
		coStatus.Reason = skewUnchecked
		coStatus.Message = fmt.Sprintf("An error occurred when getting all the managed nodes: %v", err.Error())
	}
	for _, node := range nodes {
		// looks like kubeletVersion: v1.21.0-rc.0+6143dea
		kubeletVersion = node.Status.NodeInfo.KubeletVersion
		if kubeletVersion == "" {
			continue
		}
		nodeMinorVersion, err := getMinorKubeletVersion(kubeletVersion)
		if err != nil {
			lastError = err
			continue
		}
		if nodeMinorVersion+2 < kubeAPIServerMinorVersion {
			coStatus.Reason = skewUnsupported
			coStatus.Message = fmt.Sprintf("One or more nodes have an unsupported kubelet version skew. Please see `oc get nodes` for details and upgrade all nodes so that they have a kubelet version of at least %v.", getMinimalSkewSupportNodeVersion(kubeAPIServerVersion))
			return skewUnsupported, coStatus, nil
		}
		if nodeMinorVersion+2 == kubeAPIServerMinorVersion {
			coStatus.Reason = skewPresent
			coStatus.Message = fmt.Sprintf("Current kubelet version %v will not be supported by newer kube-apiserver. Please upgrade the kubelet first if plan to upgrade the kube-apiserver", kubeletVersion)
			return skewPresent, coStatus, nil
		}
	}
	if kubeletVersion == "" {
		err = fmt.Errorf("kubelet does not yet have a version")
		coStatus.Reason = skewUnchecked
		coStatus.Message = fmt.Sprintf("An error occurred when checking kubelet version skew: %v", err.Error())
		return skewUnchecked, coStatus, err
	}
	if lastError != nil {
		coStatus.Reason = skewUnchecked
		coStatus.Message = fmt.Sprintf("An error occurred when checking kubelet version skew: %v", err)
		return skewUnchecked, coStatus, lastError
	}
	return skewSupported, coStatus, nil
}

// GetAllManagedNodes returns the nodes managed by MCO
func (optr *Operator) GetAllManagedNodes(pools []*v1.MachineConfigPool) ([]*corev1.Node, error) {
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

// getMinorKubeletVersion parses the minor version number of kubelet
func getMinorKubeletVersion(version string) (int, error) {
	tokens := strings.Split(version, ".")
	if len(tokens) < 2 {
		return 0, fmt.Errorf("incorrect version syntax: %q", version)
	}
	minorVersion, err := strconv.ParseInt(tokens[1], 10, 32)
	if err != nil {
		return 0, err
	}
	return int(minorVersion), nil
}

// getMinimalSkewSupportNodeVersion returns the minimal supported node kubelet version.
func getMinimalSkewSupportNodeVersion(version string) string {
	// drop the pre-release and commit hash
	idx := strings.Index(version, "-")
	if idx >= 0 {
		version = version[:idx]
	}

	idx = strings.Index(version, "+")
	if idx >= 0 {
		version = version[:idx]
	}

	tokens := strings.Split(version, ".")
	if minorVersion, err := strconv.ParseInt(tokens[1], 10, 32); err == nil {
		tokens[1] = strconv.Itoa(int(minorVersion) - 2)
		return strings.Join(tokens, ".")
	}
	return version
}

func (optr *Operator) fetchClusterOperator() (*configv1.ClusterOperator, error) {
	co, err := optr.configClient.ConfigV1().ClusterOperators().Get(context.TODO(), optr.name, metav1.GetOptions{})
	if meta.IsNoMatchError(err) {
		return nil, nil
	}
	if apierrors.IsNotFound(err) {
		return optr.initializeClusterOperator()
	}
	if err != nil {
		return nil, err
	}
	coCopy := co.DeepCopy()
	return coCopy, nil
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
		Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
		Type: configv1.OperatorUpgradeable, Status: configv1.ConditionUnknown, Reason: "NoData"})

	// RelatedObjects are consumed by https://github.com/openshift/must-gather
	co.Status.RelatedObjects = []configv1.ObjectReference{
		{Resource: "namespaces", Name: optr.namespace},
		{Group: "machineconfiguration.openshift.io", Resource: "machineconfigpools", Name: "master"},
		{Group: "machineconfiguration.openshift.io", Resource: "machineconfigpools", Name: "worker"},
		{Group: "machineconfiguration.openshift.io", Resource: "controllerconfigs", Name: "machine-config-controller"},
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
		glog.Error(err)
		return
	}
	if statusErr != nil {
		statuses["lastSyncError"] = statusErr.Error()
	}
	raw, err := json.Marshal(statuses)
	if err != nil {
		glog.Error(err)
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
		ret[pool.GetName()] = machineConfigPoolStatus(pool)
	}
	return ret, nil
}

// isMachineConfigPoolConfigurationValid returns nil, or error when the configuration of a `pool` is created by the controller at version `version`,
// when the osImageURL does not match what's in the configmap or when the rendered-config-xxx does not match the OCP release version.
func isMachineConfigPoolConfigurationValid(pool *mcfgv1.MachineConfigPool, version, releaseVersion, osURL string, machineConfigGetter func(string) (*mcfgv1.MachineConfig, error)) error {
	// both .status.configuration.name and .status.configuration.source must be set.
	if pool.Spec.Configuration.Name == "" {
		return fmt.Errorf("configuration spec for pool %s is empty: %v", pool.GetName(), machineConfigPoolStatus(pool))
	}
	if pool.Status.Configuration.Name == "" {
		// if status is empty, it means the node controller hasn't seen any node at the target configuration
		// we bubble up any error from the pool to make the info more visible
		return fmt.Errorf("configuration status for pool %s is empty: %s", pool.GetName(), machineConfigPoolStatus(pool))
	}
	if len(pool.Status.Configuration.Source) == 0 {
		return fmt.Errorf("list of MachineConfigs that were used to generate configuration for pool %s is empty: %v", pool.GetName(), machineConfigPoolStatus(pool))
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
			return fmt.Errorf("%s must be created by controller version %s: %v", mcName, version, machineConfigPoolStatus(pool))
		}
		// user provided MC fragments do not have the annotation, so we just skip the version check there.
		// The check below is for: 1) the generated MC for the pool, and 2) the bootstrapped fragments
		// that do have this annotation set with a version.
		if ok && v != version {
			return fmt.Errorf("controller version mismatch for %s expected %s has %s: %v", mcName, version, v, machineConfigPoolStatus(pool))
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

func machineConfigPoolStatus(pool *mcfgv1.MachineConfigPool) string {
	switch {
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded):
		cond := mcfgv1.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolRenderDegraded)
		return fmt.Sprintf("pool is degraded because rendering fails with %q: %q", cond.Reason, cond.Message)
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolNodeDegraded):
		cond := mcfgv1.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolNodeDegraded)
		return fmt.Sprintf("pool is degraded because nodes fail with %q: %q", cond.Reason, cond.Message)
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdated):
		return fmt.Sprintf("all %d nodes are at latest configuration %s", pool.Status.MachineCount, pool.Status.Configuration.Name)
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating):
		return fmt.Sprintf("%d (ready %d) out of %d nodes are updating to latest configuration %s", pool.Status.UpdatedMachineCount, pool.Status.ReadyMachineCount, pool.Status.MachineCount, pool.Spec.Configuration.Name)
	default:
		return "<unknown>"
	}
}
