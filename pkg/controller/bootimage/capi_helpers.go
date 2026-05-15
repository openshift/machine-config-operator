package bootimage

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	archtranslater "github.com/coreos/stream-metadata-go/arch"
	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	opv1 "github.com/openshift/api/operator/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	operatorversion "github.com/openshift/machine-config-operator/pkg/version"
	kubeApiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// TODO: replace with opv1.ClusterAPI and opv1.MachineDeployments once added to openshift/api.
const (
	capiAPIGroup           opv1.MachineManagerMachineSetsAPIGroupType = "cluster.x-k8s.io"
	capiMachineDeployments opv1.MachineManagerMachineSetsResourceType = "machinedeployments"
)

// syncCAPIMachineSets reconciles boot images for enrolled CAPI MachineSets.
// nolint:dupl
func (ctrl *Controller) syncCAPIMachineSets(reason string) {
	if ctrl.capiMachineSetLister == nil {
		return
	}

	mcop, err := ctrl.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		klog.Errorf("Failed to get MachineConfiguration: %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to get MachineConfiguration while enqueueing CAPI MachineSets: %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	machineManagerFound, machineResourceSelector, err := getMachineResourceSelectorFromMachineManagers(mcop.Status.ManagedBootImagesStatus.MachineManagers, capiAPIGroup, opv1.MachineSets)
	if err != nil {
		klog.Errorf("failed to create a machineset selector while enqueueing CAPI MachineSets: %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to create a machineset selector while enqueueing CAPI MachineSets: %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}
	if !machineManagerFound {
		klog.V(4).Infof("No CAPI MachineSet manager found, clearing CAPI boot image state")
		for k := range ctrl.capiBootImageState {
			delete(ctrl.capiBootImageState, k)
		}
		return
	}

	objs, err := ctrl.capiMachineSetLister.List(machineResourceSelector)
	if err != nil {
		klog.Errorf("failed to list CAPI MachineSets: %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to list CAPI MachineSets: %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	if len(objs) == 0 {
		klog.Infof("No CAPI MachineSets enrolled")
		for k := range ctrl.capiBootImageState {
			delete(ctrl.capiBootImageState, k)
		}
	}

	ctrl.capiMachineSetStats.inProgress = 0
	ctrl.capiMachineSetStats.totalCount = len(objs)
	ctrl.capiMachineSetStats.skippedCount = 0
	ctrl.capiMachineSetStats.erroredCount = 0

	var syncErrors []error
	ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)

	for _, obj := range objs {
		ms, err := unstructuredToMachineSet(obj)
		if err != nil {
			klog.Errorf("failed to convert unstructured to CAPI MachineSet: %v", err)
			syncErrors = append(syncErrors, err)
			ctrl.capiMachineSetStats.erroredCount++
			continue
		}
		patchSkipped, err := ctrl.syncCAPIMachineSet(ms)
		if err == nil {
			ctrl.capiMachineSetStats.inProgress++
		} else {
			klog.Errorf("Error syncing CAPI MachineSet %s: %v", ms.Name, err)
			syncErrors = append(syncErrors, fmt.Errorf("error syncing CAPI MachineSet %s: %v", ms.Name, err))
			ctrl.capiMachineSetStats.erroredCount++
		}
		if patchSkipped {
			ctrl.capiMachineSetStats.skippedCount++
		}
		ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)
	}
	ctrl.updateConditions(reason, kubeErrs.NewAggregate(syncErrors), opv1.MachineConfigurationBootImageUpdateDegraded)
}

// syncCAPIMachineDeployments reconciles boot images for enrolled CAPI MachineDeployments.
// nolint:dupl
func (ctrl *Controller) syncCAPIMachineDeployments(reason string) {
	if ctrl.capiMachineDeploymentLister == nil {
		return
	}

	mcop, err := ctrl.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		klog.Errorf("Failed to get MachineConfiguration: %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to get MachineConfiguration while enqueueing CAPI MachineDeployments: %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	machineManagerFound, machineResourceSelector, err := getMachineResourceSelectorFromMachineManagers(mcop.Status.ManagedBootImagesStatus.MachineManagers, capiAPIGroup, capiMachineDeployments)
	if err != nil {
		klog.Errorf("failed to create a selector while enqueueing CAPI MachineDeployments: %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to create a selector while enqueueing CAPI MachineDeployments: %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}
	if !machineManagerFound {
		klog.V(4).Infof("No CAPI MachineDeployment manager found")
		return
	}

	objs, err := ctrl.capiMachineDeploymentLister.List(machineResourceSelector)
	if err != nil {
		klog.Errorf("failed to list CAPI MachineDeployments: %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to list CAPI MachineDeployments: %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	ctrl.capiMachineDeploymentStats.inProgress = 0
	ctrl.capiMachineDeploymentStats.totalCount = len(objs)
	ctrl.capiMachineDeploymentStats.skippedCount = 0
	ctrl.capiMachineDeploymentStats.erroredCount = 0

	var syncErrors []error
	ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)

	for _, obj := range objs {
		md, err := unstructuredToMachineDeployment(obj)
		if err != nil {
			klog.Errorf("failed to convert unstructured to CAPI MachineDeployment: %v", err)
			syncErrors = append(syncErrors, err)
			ctrl.capiMachineDeploymentStats.erroredCount++
			continue
		}
		patchSkipped, err := ctrl.syncCAPIMachineDeployment(md)
		if err == nil {
			ctrl.capiMachineDeploymentStats.inProgress++
		} else {
			klog.Errorf("Error syncing CAPI MachineDeployment %s: %v", md.Name, err)
			syncErrors = append(syncErrors, fmt.Errorf("error syncing CAPI MachineDeployment %s: %v", md.Name, err))
			ctrl.capiMachineDeploymentStats.erroredCount++
		}
		if patchSkipped {
			ctrl.capiMachineDeploymentStats.skippedCount++
		}
		ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)
	}
	ctrl.updateConditions(reason, kubeErrs.NewAggregate(syncErrors), opv1.MachineConfigurationBootImageUpdateDegraded)
}

// syncCAPIMachineSet reconciles a single CAPI MachineSet's boot image.
func (ctrl *Controller) syncCAPIMachineSet(ms *clusterv1.MachineSet) (bool, error) {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing CAPI MachineSet %q (%v)", ms.Name, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing CAPI MachineSet %q (%v)", ms.Name, time.Since(startTime))
	}()

	// Skip MachineSets owned by a MachineDeployment — those are already-created
	// copies; the MachineDeployment itself is the resource to patch.
	// See: https://github.com/kubernetes-sigs/cluster-api/issues/2446
	for _, ref := range ms.GetOwnerReferences() {
		if ref.Kind == "MachineDeployment" {
			klog.Infof("CAPI MachineSet %s is owned by MachineDeployment %s, boot image managed via parent", ms.Name, ref.Name)
			return false, nil
		}
	}

	// If a MAPI MachineSet with the same name exists and is still authoritative, defer to
	// the MAPI sync path. Only proceed once authoritativeAPI has transitioned to ClusterAPI.
	mapiMS, err := ctrl.mapiMachineSetLister.MachineSets(MachineAPINamespace).Get(ms.Name)
	if err != nil && !kubeApiErrors.IsNotFound(err) {
		return false, fmt.Errorf("failed to check MAPI MachineSet for CAPI MachineSet %s: %w", ms.Name, err)
	}
	if mapiMS != nil && mapiMS.Status.AuthoritativeAPI != machinev1beta1.MachineAuthorityClusterAPI {
		klog.Infof("CAPI MachineSet %s has a MAPI counterpart with authoritativeAPI=%s, deferring to MAPI path", ms.Name, mapiMS.Status.AuthoritativeAPI)
		return false, nil
	}

	// Skip non-default OS streams. If no label is present, treat as a default stream.
	if streamLabel, ok := ms.GetLabels()[OSStreamLabelKey]; ok {
		if streamLabel != SupportedOSStream {
			klog.Infof("CAPI MachineSet %s has unsupported stream: %v, skipping boot image update", ms.Name, streamLabel)
			return false, nil
		}
	}

	// Skip Windows MachineSets.
	if os, ok := ms.Spec.Template.Labels[OSLabelKey]; ok {
		if os == "Windows" {
			klog.Infof("CAPI MachineSet %s has a Windows OS label, skipping boot image update", ms.Name)
			return false, nil
		}
	}

	// Fetch ClusterVersion to determine if this is a multi-arch cluster.
	clusterVersion, err := ctrl.clusterVersionLister.Get("version")
	if err != nil {
		return false, fmt.Errorf("failed to fetch clusterversion during CAPI MachineSet sync: %w", err)
	}

	arch, err := getArchFromCAPIMachineSet(ms, clusterVersion)
	if err != nil {
		if strings.Contains(err.Error(), "no architecture annotation found") {
			return true, nil
		}
		return false, fmt.Errorf("failed to fetch arch during CAPI MachineSet sync: %w", err)
	}

	infra, err := ctrl.infraLister.Get("cluster")
	if err != nil {
		return false, fmt.Errorf("failed to fetch infra object during CAPI MachineSet sync: %w", err)
	}

	configMap, err := ctrl.mcoCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
	if err != nil {
		return false, fmt.Errorf("failed to fetch coreos-bootimages config map during CAPI MachineSet sync: %w", err)
	}
	releaseVersionFromCM, releaseVersionFound := configMap.Data[ctrlcommon.OCPReleaseVersionKey]
	if !releaseVersionFound {
		klog.Infof("failed to find OCP release version in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
		return true, nil
	}
	if releaseVersionFromCM != operatorversion.ReleaseVersion {
		klog.Infof("mismatch between OCP release version stored in configmap and current MCO release version; sync will exit to wait for the MCO upgrade to complete")
		return true, nil
	}

	// Fetch the current infrastructure template from the cache-backed per-platform lister.
	currentTemplate, err := ctrl.getCAPIInfraTemplate(ms.Spec.Template.Spec.InfrastructureRef.Name)
	if err != nil {
		return false, fmt.Errorf("failed to fetch infrastructure template for CAPI MachineSet %s: %w", ms.Name, err)
	}

	patchRequired, patchSkipped, newTemplate, err := checkCAPIMachineSet(infra, ms.Name, currentTemplate, configMap, arch)
	if err != nil {
		return false, fmt.Errorf("failed to reconcile CAPI MachineSet %s: %w", ms.Name, err)
	}

	if patchRequired {
		newTemplateSpecBytes, err := json.Marshal(newTemplate.Object["spec"])
		if err != nil {
			return false, fmt.Errorf("failed to marshal new template spec for CAPI MachineSet %s: %w", ms.Name, err)
		}
		if ctrl.checkCAPIMachineSetHotLoop(ms.Name, newTemplateSpecBytes) {
			return false, fmt.Errorf("refusing to reconcile CAPI MachineSet %s, hot loop detected. Please opt-out of boot image updates, adjust your machine provisioning workflow to prevent hot loops and opt back in to resume boot image updates", ms.Name)
		}
		newTemplateName := newInfraTemplateName(ms.Name, newTemplateSpecBytes)
		klog.Infof("Creating new CAPI infrastructure template %s for MachineSet %s", newTemplateName, ms.Name)
		return false, ctrl.patchCAPIMachineSet(ms, newTemplate, infra.Status.PlatformStatus.Type, newTemplateName)
	}

	klog.Infof("No patching required for CAPI MachineSet %s", ms.Name)
	return patchSkipped, nil
}

// syncCAPIMachineDeployment reconciles a single CAPI MachineDeployment's boot image.
func (ctrl *Controller) syncCAPIMachineDeployment(md *clusterv1.MachineDeployment) (bool, error) {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing CAPI MachineDeployment %q (%v)", md.Name, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing CAPI MachineDeployment %q (%v)", md.Name, time.Since(startTime))
	}()

	// Skip non-default OS streams. If no label is present, treat as a default stream.
	if streamLabel, ok := md.GetLabels()[OSStreamLabelKey]; ok {
		if streamLabel != SupportedOSStream {
			klog.Infof("CAPI MachineDeployment %s has unsupported stream: %v, skipping boot image update", md.Name, streamLabel)
			return false, nil
		}
	}

	// Skip Windows MachineDeployments.
	if os, ok := md.Spec.Template.Labels[OSLabelKey]; ok {
		if os == "Windows" {
			klog.Infof("CAPI MachineDeployment %s has a Windows OS label, skipping boot image update", md.Name)
			return false, nil
		}
	}

	clusterVersion, err := ctrl.clusterVersionLister.Get("version")
	if err != nil {
		return false, fmt.Errorf("failed to fetch clusterversion during CAPI MachineDeployment sync: %w", err)
	}

	arch, err := getArchFromCAPIMachineDeployment(md, clusterVersion)
	if err != nil {
		if strings.Contains(err.Error(), "no architecture annotation found") {
			return true, nil
		}
		return false, fmt.Errorf("failed to fetch arch during CAPI MachineDeployment sync: %w", err)
	}

	infra, err := ctrl.infraLister.Get("cluster")
	if err != nil {
		return false, fmt.Errorf("failed to fetch infra object during CAPI MachineDeployment sync: %w", err)
	}

	configMap, err := ctrl.mcoCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
	if err != nil {
		return false, fmt.Errorf("failed to fetch coreos-bootimages config map during CAPI MachineDeployment sync: %w", err)
	}
	releaseVersionFromCM, releaseVersionFound := configMap.Data[ctrlcommon.OCPReleaseVersionKey]
	if !releaseVersionFound {
		klog.Infof("failed to find OCP release version in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
		return true, nil
	}
	if releaseVersionFromCM != operatorversion.ReleaseVersion {
		klog.Infof("mismatch between OCP release version stored in configmap and current MCO release version; sync will exit to wait for the MCO upgrade to complete")
		return true, nil
	}

	currentTemplate, err := ctrl.getCAPIInfraTemplate(md.Spec.Template.Spec.InfrastructureRef.Name)
	if err != nil {
		return false, fmt.Errorf("failed to fetch infrastructure template for CAPI MachineDeployment %s: %w", md.Name, err)
	}

	patchRequired, patchSkipped, newTemplate, err := checkCAPIMachineSet(infra, md.Name, currentTemplate, configMap, arch)
	if err != nil {
		return false, fmt.Errorf("failed to reconcile CAPI MachineDeployment %s: %w", md.Name, err)
	}

	if patchRequired {
		newTemplateSpecBytes, err := json.Marshal(newTemplate.Object["spec"])
		if err != nil {
			return false, fmt.Errorf("failed to marshal new template spec for CAPI MachineDeployment %s: %w", md.Name, err)
		}
		if ctrl.checkCAPIMachineSetHotLoop(md.Name, newTemplateSpecBytes) {
			return false, fmt.Errorf("refusing to reconcile CAPI MachineDeployment %s, hot loop detected. Please opt-out of boot image updates, adjust your machine provisioning workflow to prevent hot loops and opt back in to resume boot image updates", md.Name)
		}
		newTemplateName := newInfraTemplateName(md.Name, newTemplateSpecBytes)
		klog.Infof("Creating new CAPI infrastructure template %s for MachineDeployment %s", newTemplateName, md.Name)
		return false, ctrl.patchCAPIMachineDeployment(md, newTemplate, infra.Status.PlatformStatus.Type, newTemplateName)
	}

	klog.Infof("No patching required for CAPI MachineDeployment %s", md.Name)
	return patchSkipped, nil
}

// checkCAPIMachineSetHotLoop checks whether the controller is hot-looping on a CAPI MachineSet.
func (ctrl *Controller) checkCAPIMachineSetHotLoop(msName string, newTemplateSpec []byte) bool {
	bis, ok := ctrl.capiBootImageState[msName]
	if !ok {
		ctrl.capiBootImageState[msName] = BootImageState{
			value:        newTemplateSpec,
			hotLoopCount: 1,
		}
	} else {
		hotLoopCount := 1
		if bytes.Equal(bis.value, newTemplateSpec) {
			hotLoopCount = bis.hotLoopCount + 1
		}
		if hotLoopCount > HotLoopLimit {
			return true
		}
		ctrl.capiBootImageState[msName] = BootImageState{
			value:        newTemplateSpec,
			hotLoopCount: hotLoopCount,
		}
	}
	return false
}

// getCAPIInfraTemplate fetches a CAPI infrastructure template by name from the
// platform-specific cache-backed lister wired in Run().
func (ctrl *Controller) getCAPIInfraTemplate(name string) (*unstructured.Unstructured, error) {
	return ctrl.capiInfraTemplateLister.Get(name)
}

// patchCAPIMachineSet creates a new infrastructure template with the given name and patches
// the MachineSet's infrastructureRef to point at it.
func (ctrl *Controller) patchCAPIMachineSet(ms *clusterv1.MachineSet, newTemplate *unstructured.Unstructured, platform osconfigv1.PlatformType, newTemplateName string) error {
	templateGVR, err := capiInfraTemplateGVR(platform)
	if err != nil {
		return err
	}
	newTemplate.SetName(newTemplateName)
	newTemplate.SetResourceVersion("")
	newTemplate.SetUID("")
	// Clear BlockOwnerDeletion from copied owner references. The owner is the CAPI
	// Cluster object, which already carries its own finalizer (cluster.cluster.x-k8s.io)
	// managing its deletion lifecycle. BlockOwnerDeletion is therefore redundant, and
	// setting it would require MCO to have update on clusters/finalizers — which it doesn't.
	refs := newTemplate.GetOwnerReferences()
	for i := range refs {
		refs[i].BlockOwnerDeletion = nil
	}
	newTemplate.SetOwnerReferences(refs)

	_, err = ctrl.dynamicClient.Resource(templateGVR).Namespace(ms.Namespace).Create(context.TODO(), newTemplate, metav1.CreateOptions{})
	if err != nil && !kubeApiErrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create new infrastructure template %s: %w", newTemplateName, err)
	}
	if kubeApiErrors.IsAlreadyExists(err) {
		// The template name is a deterministic hash of the spec, so an existing template
		// with this name already has the correct boot image. Proceed to point the MachineSet
		// at it without creating a duplicate.
		klog.Infof("Infrastructure template %s already exists, reusing for CAPI MachineSet %s", newTemplateName, ms.Name)
	}

	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"infrastructureRef": map[string]any{
						"name": newTemplateName,
					},
				},
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("unable to marshal patch for CAPI MachineSet %s: %w", ms.Name, err)
	}
	_, err = ctrl.dynamicClient.Resource(capiMachineSetGVR).Namespace(ms.Namespace).Patch(context.TODO(), ms.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("unable to patch CAPI MachineSet %s: %w", ms.Name, err)
	}
	klog.Infof("Successfully patched CAPI MachineSet %s to use infrastructure template %s", ms.Name, newTemplateName)
	return nil
}

// patchCAPIMachineDeployment creates a new infrastructure template with the given name and patches
// the MachineDeployment's infrastructureRef to point at it.
func (ctrl *Controller) patchCAPIMachineDeployment(md *clusterv1.MachineDeployment, newTemplate *unstructured.Unstructured, platform osconfigv1.PlatformType, newTemplateName string) error {
	templateGVR, err := capiInfraTemplateGVR(platform)
	if err != nil {
		return err
	}
	newTemplate.SetName(newTemplateName)
	newTemplate.SetResourceVersion("")
	newTemplate.SetUID("")
	// Clear BlockOwnerDeletion from copied owner references — see patchCAPIMachineSet for rationale.
	refs := newTemplate.GetOwnerReferences()
	for i := range refs {
		refs[i].BlockOwnerDeletion = nil
	}
	newTemplate.SetOwnerReferences(refs)

	_, err = ctrl.dynamicClient.Resource(templateGVR).Namespace(md.Namespace).Create(context.TODO(), newTemplate, metav1.CreateOptions{})
	if err != nil && !kubeApiErrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create new infrastructure template %s: %w", newTemplateName, err)
	}
	if kubeApiErrors.IsAlreadyExists(err) {
		// The template name is a deterministic hash of the spec, so an existing template
		// with this name already has the correct boot image. Proceed to point the MachineDeployment
		// at it without creating a duplicate.
		klog.Infof("Infrastructure template %s already exists, reusing for CAPI MachineDeployment %s", newTemplateName, md.Name)
	}

	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"infrastructureRef": map[string]any{
						"name": newTemplateName,
					},
				},
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("unable to marshal patch for CAPI MachineDeployment %s: %w", md.Name, err)
	}
	_, err = ctrl.dynamicClient.Resource(capiMachineDeploymentGVR).Namespace(md.Namespace).Patch(context.TODO(), md.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("unable to patch CAPI MachineDeployment %s: %w", md.Name, err)
	}
	klog.Infof("Successfully patched CAPI MachineDeployment %s to use infrastructure template %s", md.Name, newTemplateName)
	return nil
}

// newInfraTemplateName returns a deterministic name for a new infrastructure template.
// Format: <machineset-name>-<hash8> where hash8 is the 8 hex digit FNV-1a 32-bit hash of
// the new template spec. Matches the naming convention used by cluster-capi-operator.
func newInfraTemplateName(msName string, newSpec []byte) string {
	hasher := fnv.New32a()
	hasher.Write(newSpec)
	return fmt.Sprintf("%s-%s", msName, hex.EncodeToString(hasher.Sum(nil)))
}

// getArchFromCAPIMachineSet returns the architecture for a given CAPI MachineSet.
func getArchFromCAPIMachineSet(ms *clusterv1.MachineSet, clusterVersion *osconfigv1.ClusterVersion) (string, error) {
	validArchSet := sets.New("arm64", "s390x", "amd64", "ppc64le")
	archLabel, archLabelMatch := ms.Annotations[MachineSetArchAnnotationKey]

	if !archLabelMatch {
		if clusterVersion.Status.Desired.Architecture == osconfigv1.ClusterVersionArchitectureMulti {
			klog.Errorf("No architecture annotation found on CAPI MachineSet %s in multi-arch cluster, skipping boot image update", ms.Name)
			return "", fmt.Errorf("no architecture annotation found on CAPI MachineSet %s", ms.Name)
		}
		klog.Infof("No architecture annotation found on CAPI MachineSet %s, defaulting to control plane architecture", ms.Name)
		return archtranslater.CurrentRpmArch(), nil
	}

	for label := range strings.SplitSeq(archLabel, ",") {
		label = strings.TrimSpace(label)
		if archLabelValue, found := strings.CutPrefix(label, ArchLabelKey); found {
			if validArchSet.Has(archLabelValue) {
				return archtranslater.RpmArch(archLabelValue), nil
			}
			return "", fmt.Errorf("invalid architecture value found in annotation: %s", archLabelValue)
		}
	}
	return "", fmt.Errorf("kubernetes.io/arch label not found in annotation: %s", archLabel)
}

// getArchFromCAPIMachineDeployment returns the architecture for a given CAPI MachineDeployment.
func getArchFromCAPIMachineDeployment(md *clusterv1.MachineDeployment, clusterVersion *osconfigv1.ClusterVersion) (string, error) {
	validArchSet := sets.New("arm64", "s390x", "amd64", "ppc64le")
	archLabel, archLabelMatch := md.Annotations[MachineSetArchAnnotationKey]

	if !archLabelMatch {
		if clusterVersion.Status.Desired.Architecture == osconfigv1.ClusterVersionArchitectureMulti {
			klog.Errorf("No architecture annotation found on CAPI MachineDeployment %s in multi-arch cluster, skipping boot image update", md.Name)
			return "", fmt.Errorf("no architecture annotation found on CAPI MachineDeployment %s", md.Name)
		}
		klog.Infof("No architecture annotation found on CAPI MachineDeployment %s, defaulting to control plane architecture", md.Name)
		return archtranslater.CurrentRpmArch(), nil
	}

	for label := range strings.SplitSeq(archLabel, ",") {
		label = strings.TrimSpace(label)
		if archLabelValue, found := strings.CutPrefix(label, ArchLabelKey); found {
			if validArchSet.Has(archLabelValue) {
				return archtranslater.RpmArch(archLabelValue), nil
			}
			return "", fmt.Errorf("invalid architecture value found in annotation: %s", archLabelValue)
		}
	}
	return "", fmt.Errorf("kubernetes.io/arch label not found in annotation: %s", archLabel)
}

// unstructuredToMachineSet converts an unstructured object to a typed CAPI MachineSet.
func unstructuredToMachineSet(obj *unstructured.Unstructured) (*clusterv1.MachineSet, error) {
	ms := &clusterv1.MachineSet{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, ms); err != nil {
		return nil, fmt.Errorf("failed to convert unstructured to MachineSet %s: %w", obj.GetName(), err)
	}
	return ms, nil
}

// unstructuredToMachineDeployment converts an unstructured object to a typed CAPI MachineDeployment.
func unstructuredToMachineDeployment(obj *unstructured.Unstructured) (*clusterv1.MachineDeployment, error) {
	md := &clusterv1.MachineDeployment{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, md); err != nil {
		return nil, fmt.Errorf("failed to convert unstructured to MachineDeployment %s: %w", obj.GetName(), err)
	}
	return md, nil
}
