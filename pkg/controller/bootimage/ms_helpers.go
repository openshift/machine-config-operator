package bootimage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	opv1 "github.com/openshift/api/operator/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	operatorversion "github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	archtranslater "github.com/coreos/stream-metadata-go/arch"
)

// syncMAPIMachineSets will attempt to enqueue every machineset
// nolint:dupl // I separated this from syncControlPlaneMachineSets for readability
func (ctrl *Controller) syncMAPIMachineSets(reason string) {

	// Get MachineConfiguration to determine which resources are enrolled
	mcop, err := ctrl.mcopLister.Get(ctrlcommon.MCOOperatorKnobsObjectName)
	if err != nil {
		klog.Errorf("Failed to get MachineConfiguration: %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to get MachineConfiguration while enqueueing MAPI MachineSets: %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	machineManagerFound, machineResourceSelector, err := getMachineResourceSelectorFromMachineManagers(mcop.Status.ManagedBootImagesStatus.MachineManagers, opv1.MachineAPI, opv1.MachineSets)
	if err != nil {
		klog.Errorf("failed to create a machineset selector while enqueueing MAPI machineset %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to create a machineset selector while enqueueing MAPI machineset %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}
	if !machineManagerFound {
		klog.V(4).Infof("No MAPI machineset manager was found, so no MAPI machinesets will be enrolled.")
		// clear out MAPI boot image history
		for k := range ctrl.mapiBootImageState {
			delete(ctrl.mapiBootImageState, k)
		}

	}

	mapiMachineSets, err := ctrl.mapiMachineSetLister.List(machineResourceSelector)
	if err != nil {
		klog.Errorf("failed to fetch MachineSet list while enqueueing MAPI MachineSets %v", err)
		ctrl.updateConditions(reason, fmt.Errorf("failed to fetch MachineSet list while enqueueing MAPI MachineSets %v", err), opv1.MachineConfigurationBootImageUpdateDegraded)
		return
	}

	// If no machine resources were enrolled; exit the enqueue process without errors.
	if len(mapiMachineSets) == 0 {
		klog.Infof("No MAPI machinesets were enrolled, so no MAPI machinesets will be enqueued.")
		// clear out MAPI boot image history
		for k := range ctrl.mapiBootImageState {
			delete(ctrl.mapiBootImageState, k)
		}
	}

	// Reset stats before initiating reconciliation loop
	ctrl.mapiStats.inProgress = 0
	ctrl.mapiStats.totalCount = len(mapiMachineSets)
	ctrl.mapiStats.erroredCount = 0

	// Signal start of reconciliation process, by setting progressing to true
	var syncErrors []error
	ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)

	for _, machineSet := range mapiMachineSets {
		err := ctrl.syncMAPIMachineSet(machineSet)
		if err == nil {
			ctrl.mapiStats.inProgress++
		} else {
			klog.Errorf("Error syncing MAPI MachineSet %v", err)
			syncErrors = append(syncErrors, fmt.Errorf("error syncing MAPI MachineSet %s: %v", machineSet.Name, err))
			ctrl.mapiStats.erroredCount++
		}
		// Update progressing conditions every step of the loop
		ctrl.updateConditions(reason, nil, opv1.MachineConfigurationBootImageUpdateProgressing)
	}
	// Update/Clear degrade conditions based on errors from this loop
	ctrl.updateConditions(reason, kubeErrs.NewAggregate(syncErrors), opv1.MachineConfigurationBootImageUpdateDegraded)
}

// syncMAPIMachineSet will attempt to reconcile the provided machineset
func (ctrl *Controller) syncMAPIMachineSet(machineSet *machinev1beta1.MachineSet) error {

	startTime := time.Now()
	klog.V(4).Infof("Started syncing MAPI machineset %q (%v)", machineSet.Name, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing MAPI machineset %q (%v)", machineSet.Name, time.Since(startTime))
	}()

	// If the machineset has an owner reference, exit and report error. This means
	// that the machineset may be managed by another workflow and should not be reconciled.
	if len(machineSet.GetOwnerReferences()) != 0 {
		klog.Infof("machineset %s has OwnerReference: %v, skipping boot image update", machineSet.GetOwnerReferences()[0].Kind+"/"+machineSet.GetOwnerReferences()[0].Name, machineSet.Name)
		return nil
	}

	if os, ok := machineSet.Spec.Template.Labels[OSLabelKey]; ok {
		if os == "Windows" {
			klog.Infof("machineset %s has a windows os label, skipping boot image update", machineSet.Name)
			return nil
		}
	}

	// Fetch the ClusterVersion to determine if this is a multi-arch cluster
	clusterVersion, err := ctrl.clusterVersionLister.Get("version")
	if err != nil {
		return fmt.Errorf("failed to fetch clusterversion during machineset sync: %v, defaulting to single-arch behavior", err)
	}

	// Fetch the architecture type of this machineset
	arch, err := getArchFromMachineSet(machineSet, clusterVersion)
	if err != nil {
		// If no architecture annotation was found, skip this machineset without erroring
		// A later sync loop will pick it up once the annotation is added
		if strings.Contains(err.Error(), "no architecture annotation found") {
			return nil
		}
		return fmt.Errorf("failed to fetch arch during machineset sync: %w", err)
	}

	// Fetch the infra object to determine the platform type
	infra, err := ctrl.infraLister.Get("cluster")
	if err != nil {
		return fmt.Errorf("failed to fetch infra object during machineset sync: %w", err)
	}

	// Fetch the bootimage configmap & ensure it has been stamped by the operator. This is done by
	// the operator when a master node successfully updates to a new image. This is
	// to prevent machinesets from being updated before the operator itself has updated.
	// If it hasn't been updated, exit and wait for a resync.
	configMap, err := ctrl.mcoCmLister.ConfigMaps(ctrlcommon.MCONamespace).Get(ctrlcommon.BootImagesConfigMapName)
	if err != nil {
		return fmt.Errorf("failed to fetch coreos-bootimages config map during machineset sync: %w", err)
	}
	versionHashFromCM, versionHashFound := configMap.Data[ctrlcommon.MCOVersionHashKey]
	if !versionHashFound {
		klog.Infof("failed to find mco version hash in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
		return nil
	}
	if versionHashFromCM != operatorversion.Hash {
		klog.Infof("mismatch between MCO hash version stored in configmap and current MCO version; sync will exit to wait for the MCO upgrade to complete")
		return nil
	}
	releaseVersionFromCM, releaseVersionFound := configMap.Data[ctrlcommon.OCPReleaseVersionKey]
	if !releaseVersionFound {
		klog.Infof("failed to find OCP release version in %s configmap, sync will exit to wait for the MCO upgrade to complete", ctrlcommon.BootImagesConfigMapName)
		return nil
	}
	if releaseVersionFromCM != operatorversion.ReleaseVersion {
		klog.Infof("mismatch between OCP release version stored in configmap and current MCO release version; sync will exit to wait for the MCO upgrade to complete")
		return nil
	}

	// Check if the this MachineSet requires an update
	patchRequired, newMachineSet, err := checkMachineSet(infra, machineSet, configMap, arch, ctrl.kubeClient)
	if err != nil {
		return fmt.Errorf("failed to reconcile machineset %s, err: %w", machineSet.Name, err)
	}

	// Patch the machineset if required
	if patchRequired {
		// First, check if we're hot looping
		if ctrl.checkMAPIMachineSetHotLoop(newMachineSet) {
			return fmt.Errorf("refusing to reconcile machineset %s, hot loop detected. Please opt-out of boot image updates, adjust your machine provisioning workflow to prevent hot loops and opt back in to resume boot image updates", machineSet.Name)
		}
		klog.Infof("Patching MAPI machineset %s", machineSet.Name)
		return ctrl.patchMachineSet(machineSet, newMachineSet)
	}
	klog.Infof("No patching required for MAPI machineset %s", machineSet.Name)
	return nil
}

// Checks against a local store of boot image updates to detect hot looping
func (ctrl *Controller) checkMAPIMachineSetHotLoop(machineSet *machinev1beta1.MachineSet) bool {
	bis, ok := ctrl.mapiBootImageState[machineSet.Name]
	if !ok {
		// If the machineset doesn't currently have a record, create a new one.
		ctrl.mapiBootImageState[machineSet.Name] = BootImageState{
			value:        machineSet.Spec.Template.Spec.ProviderSpec.Value.Raw,
			hotLoopCount: 1,
		}
	} else {
		hotLoopCount := 1
		// If the controller is updating to a value that was previously updated to, increase the hot loop counter
		if bytes.Equal(bis.value, machineSet.Spec.Template.Spec.ProviderSpec.Value.Raw) {
			hotLoopCount = (bis.hotLoopCount) + 1
		}
		// Return an error and degrade if the hot loop counter is above threshold
		if hotLoopCount > HotLoopLimit {
			return true
		}
		ctrl.mapiBootImageState[machineSet.Name] = BootImageState{
			value:        machineSet.Spec.Template.Spec.ProviderSpec.Value.Raw,
			hotLoopCount: hotLoopCount,
		}
	}
	return false
}

// This function patches the machineset object using the machineClient
// Returns an error if marshsalling or patching fails.
func (ctrl *Controller) patchMachineSet(oldMachineSet, newMachineSet *machinev1beta1.MachineSet) error {
	machineSetMarshal, err := json.Marshal(oldMachineSet)
	if err != nil {
		return fmt.Errorf("unable to marshal old machineset: %w", err)
	}
	newMachineSetMarshal, err := json.Marshal(newMachineSet)
	if err != nil {
		return fmt.Errorf("unable to marshal new machineset: %w", err)
	}
	patchBytes, err := jsonmergepatch.CreateThreeWayJSONMergePatch(machineSetMarshal, newMachineSetMarshal, machineSetMarshal)
	if err != nil {
		return fmt.Errorf("unable to create patch for new machineset: %w", err)
	}
	_, err = ctrl.machineClient.MachineV1beta1().MachineSets(MachineAPINamespace).Patch(context.TODO(), oldMachineSet.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("unable to patch new machineset: %w", err)
	}
	klog.Infof("Successfully patched machineset %s", oldMachineSet.Name)
	return nil
}

// Returns architecture type for a given machineset
func getArchFromMachineSet(machineset *machinev1beta1.MachineSet, clusterVersion *osconfigv1.ClusterVersion) (arch string, err error) {

	// Valid set of machineset/node architectures
	validArchSet := sets.New("arm64", "s390x", "amd64", "ppc64le")
	// Check if the annotation enclosing arch label is present on this machineset
	archLabel, archLabelMatch := machineset.Annotations[MachineSetArchAnnotationKey]

	if !archLabelMatch {
		// Check if this is a multi-arch cluster
		// clusterVersion should never be nil as it's validated by the caller
		if clusterVersion.Status.Desired.Architecture == osconfigv1.ClusterVersionArchitectureMulti {
			// For multi-arch clusters, we require the architecture annotation
			klog.Errorf("No architecture annotation found on machineset %s in multi-arch cluster, skipping boot image update", machineset.Name)
			return "", fmt.Errorf("no architecture annotation found on machineset %s", machineset.Name)
		}
		// For single-arch clusters, default to control plane architecture
		klog.Infof("No architecture annotation found on machineset %s, defaulting to control plane architecture", machineset.Name)
		return archtranslater.CurrentRpmArch(), nil
	}

	// Parse the annotation value which may contain multiple comma-separated labels
	// Example: kubernetes.io/arch=amd64,topology.ebs.csi.aws.com/zone=eu-central-1a
	for label := range strings.SplitSeq(archLabel, ",") {
		label = strings.TrimSpace(label)
		if archLabelValue, found := strings.CutPrefix(label, ArchLabelKey); found {
			// Extract just the architecture value after "kubernetes.io/arch="
			if validArchSet.Has(archLabelValue) {
				return archtranslater.RpmArch(archLabelValue), nil
			}
			return "", fmt.Errorf("invalid architecture value found in annotation: %s", archLabelValue)
		}
	}
	return "", fmt.Errorf("kubernetes.io/arch label not found in annotation: %s", archLabel)
}
