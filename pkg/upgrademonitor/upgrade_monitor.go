package upgrademonitor

import (
	"context"
	"fmt"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	features "github.com/openshift/api/features"
	machineconfigurationv1 "github.com/openshift/client-go/machineconfiguration/applyconfigurations/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	applyconfigurationsmeta "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/klog/v2"

	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const NotYetSet = "not-yet-set"

type Condition struct {
	State   mcfgv1.StateProgress
	Reason  string
	Message string
}

// GenerateAndApplyMachineConfigNodes takes a parent and child condition and applies them to the given node's MachineConfigNode object
// there are a few stipulations:
// 1) if the parent and child condition exactly match their currently applied statuses, no new MCN is generated
// 2) the desiredConfig in the MCN Status will only be set once the update is proven to be compatible. Meanwhile the desired and current config in the spec react to live changes of state on the Node
// 3) none of this will be executed unless the TechPreviewNoUpgrade featuregate is applied. //TODO (ijanssen): Remove comment once feature gate is graduated to default.
func GenerateAndApplyMachineConfigNodes(
	parentCondition,
	childCondition *Condition,
	parentStatus,
	childStatus metav1.ConditionStatus,
	node *corev1.Node,
	mcfgClient mcfgclientset.Interface,
	fgHandler ctrlcommon.FeatureGatesHandler,
	pool string,
) error {
	return generateAndApplyMachineConfigNodes(parentCondition, childCondition, parentStatus, childStatus, node, mcfgClient, nil, fgHandler, pool)
}

func UpdateMachineConfigNodeStatus(
	parentCondition,
	childCondition *Condition,
	parentStatus,
	childStatus metav1.ConditionStatus,
	node *corev1.Node,
	mcfgClient mcfgclientset.Interface,
	imageSetApplyConfig []*machineconfigurationv1.MachineConfigNodeStatusPinnedImageSetApplyConfiguration,
	fgHandler ctrlcommon.FeatureGatesHandler,
	pool string,
) error {
	return generateAndApplyMachineConfigNodes(parentCondition, childCondition, parentStatus, childStatus, node, mcfgClient, imageSetApplyConfig, fgHandler, pool)
}

// Helper function to convert metav1.Condition to ConditionApplyConfiguration
func convertConditionToApplyConfiguration(condition metav1.Condition) *applyconfigurationsmeta.ConditionApplyConfiguration {
	return &applyconfigurationsmeta.ConditionApplyConfiguration{
		Type:               &condition.Type,
		Status:             &condition.Status,
		Reason:             &condition.Reason,
		Message:            &condition.Message,
		LastTransitionTime: &condition.LastTransitionTime,
	}
}

// Helper function to convert a slice of metav1.Condition to a slice of *ConditionApplyConfiguration
func convertConditionsToApplyConfigurations(conditions []metav1.Condition) []*applyconfigurationsmeta.ConditionApplyConfiguration {
	var result []*applyconfigurationsmeta.ConditionApplyConfiguration
	for _, condition := range conditions {
		result = append(result, convertConditionToApplyConfiguration(condition))
	}
	return result
}

// nolint:gocyclo
func generateAndApplyMachineConfigNodes(
	parentCondition,
	childCondition *Condition,
	parentStatus,
	childStatus metav1.ConditionStatus,
	node *corev1.Node,
	mcfgClient mcfgclientset.Interface,
	imageSetApplyConfig []*machineconfigurationv1.MachineConfigNodeStatusPinnedImageSetApplyConfiguration,
	fgHandler ctrlcommon.FeatureGatesHandler,
	pool string,
) error {
	if fgHandler == nil || node == nil || parentCondition == nil || mcfgClient == nil {
		return nil
	}

	// get the existing MCN, or if it DNE create one below
	mcNode, needNewMCNode := createOrGetMachineConfigNode(mcfgClient, node)
	newMCNode := mcNode.DeepCopy()
	newParentCondition := metav1.Condition{
		Type:               string(parentCondition.State),
		Status:             parentStatus,
		Reason:             parentCondition.Reason,
		Message:            parentCondition.Message,
		LastTransitionTime: metav1.Now(),
	}
	var newChildCondition *metav1.Condition
	if childCondition != nil {
		newChildCondition = &metav1.Condition{
			Type:               string(childCondition.State),
			Status:             childStatus,
			Reason:             childCondition.Reason,
			Message:            childCondition.Message,
			LastTransitionTime: metav1.Now(),
		}

	}
	reset := false
	if newParentCondition.Type == string(mcfgv1.MachineConfigNodeUpdated) {
		reset = true
	}

	// singleton conditions are conditions that should only have one instance (no children) in the MCN status.
	singletonConditionTypes := []mcfgv1.StateProgress{
		mcfgv1.MachineConfigNodePinnedImageSetsDegraded,
		mcfgv1.MachineConfigNodePinnedImageSetsProgressing,
	}

	// we use this array to see if the MCN has all of its conditions set
	// if not we set a sane default
	// TODO (MCO-1775): Once ImageModeStatusReporting is GA, clean up the below logic. The `if`
	// conditional will no longer be needed.
	var allConditionTypes []mcfgv1.StateProgress
	imageModeStatusReportingEnabled := fgHandler.Enabled(features.FeatureGateImageModeStatusReporting)
	// If `ImageModeStatusReporting` is not enabled, use the default conditions
	if !imageModeStatusReportingEnabled {
		allConditionTypes = append(
			allConditionTypes,
			mcfgv1.MachineConfigNodeUpdatePrepared,
			mcfgv1.MachineConfigNodeUpdateExecuted,
			mcfgv1.MachineConfigNodeUpdatePostActionComplete,
			mcfgv1.MachineConfigNodeUpdateComplete,
			mcfgv1.MachineConfigNodeResumed,
			mcfgv1.MachineConfigNodeUpdateDrained,
			mcfgv1.MachineConfigNodeUpdateFilesAndOS,
			mcfgv1.MachineConfigNodeUpdateCordoned,
			mcfgv1.MachineConfigNodeUpdateRebooted,
			mcfgv1.MachineConfigNodeUpdated,
			mcfgv1.MachineConfigNodeUpdateUncordoned,
			mcfgv1.MachineConfigNodeNodeDegraded,
		)
	} else { // If `ImageModeStatusReporting` is enabled, include the new condition states
		allConditionTypes = append(
			allConditionTypes,
			mcfgv1.MachineConfigNodeUpdatePrepared,
			mcfgv1.MachineConfigNodeUpdateExecuted,
			mcfgv1.MachineConfigNodeUpdatePostActionComplete,
			mcfgv1.MachineConfigNodeUpdateComplete,
			mcfgv1.MachineConfigNodeResumed,
			mcfgv1.MachineConfigNodeUpdateDrained,
			// Note that the following two conditions replace the previous, singular
			// `MachineConfigNodeUpdateFilesAndOS` condition
			mcfgv1.MachineConfigNodeUpdateFiles,
			mcfgv1.MachineConfigNodeUpdateOS,
			mcfgv1.MachineConfigNodeUpdateCordoned,
			mcfgv1.MachineConfigNodeUpdateRebooted,
			mcfgv1.MachineConfigNodeUpdated,
			mcfgv1.MachineConfigNodeUpdateUncordoned,
			// Note that the following condition is new
			mcfgv1.MachineConfigNodeImagePulledFromRegistry,
			mcfgv1.MachineConfigNodeNodeDegraded,
		)
	}
	allConditionTypes = append(allConditionTypes, singletonConditionTypes...)

	// create all of the conditions, even the false ones
	if newMCNode.Status.Conditions == nil {
		newMCNode.Status.Conditions = []metav1.Condition{}
		newMCNode.Status.Conditions = append(newMCNode.Status.Conditions, newParentCondition)
		if newChildCondition != nil {
			newMCNode.Status.Conditions = append(newMCNode.Status.Conditions, *newChildCondition)
		}
		for _, condType := range allConditionTypes {
			found := false
			for _, cond := range newMCNode.Status.Conditions {
				// if this is one of our two conditions, do not nullify this
				if condType == mcfgv1.StateProgress(cond.Type) {
					found = true
				}
			}
			// else if we do not have this one yet, set it to some sane default.
			if !found {
				newMCNode.Status.Conditions = append(newMCNode.Status.Conditions,
					metav1.Condition{
						Type:               string(condType),
						Message:            fmt.Sprintf("This node has not yet entered the %s phase", string(condType)),
						Reason:             "NotYetOccurred",
						LastTransitionTime: metav1.Now(),
						Status:             metav1.ConditionFalse,
					})
			}
		}
		// else we already have some conditions. Lets update accordingly
	} else {
		// we now check if child or parent exist. If they do, we also need to make sure they NEED to be updated. If not return nil.
		foundChild := false
		foundParent := false
		// look through all of the conditions for our current ones, update them accordingly
		// also set all other ones to false and update last transition time.
		for i, condition := range newMCNode.Status.Conditions {
			switch {
			case condition.Type == string(mcfgv1.MachineConfigNodeUpdated) && condition.Status == metav1.ConditionTrue && condition.Type != newParentCondition.Type:
				// if this happens, it is because we manually updated the MCO.
				// so, if we get a parent state == unknown or true or ANYTHING and updated also == true but it isn't the parent, set updated == false
				newC := metav1.Condition{
					Type:               string(mcfgv1.MachineConfigNodeUpdated),
					Message:            "This node is not updated, sensed disruption via a manual update.",
					Reason:             string(mcfgv1.MachineConfigNodeUpdated),
					LastTransitionTime: metav1.Now(),
					Status:             metav1.ConditionFalse,
				}
				newC.DeepCopyInto(&newMCNode.Status.Conditions[i])

			case newChildCondition != nil && condition.Type == newChildCondition.Type:
				foundChild = true
				newChildCondition.DeepCopyInto(&condition)

			case condition.Type == newParentCondition.Type:
				foundParent = true
				if !isParentConditionChanged(condition, newParentCondition) && !isSingletonCondition(singletonConditionTypes, condition.Type) {
					// there is nothing to update. Return.
					// this allows us to put the conditions in more general places but if we are already in phases like "updated"
					// then nothing happens
					// only do this if the messages match too
					// singleton conditions should also evaluate applyConfigs
					return nil
				}
				newParentCondition.DeepCopyInto(&condition)

			case condition.Status != metav1.ConditionFalse && reset:
				condition.Status = metav1.ConditionFalse
				condition.LastTransitionTime = metav1.Now()

				// Set the update to annotation to the desired rendered MC version by default for the condition message
				updateToMessage := fmt.Sprintf("Action during update to %s: %s", newMCNode.Spec.ConfigVersion.Desired, condition.Message)
				// Handle OCL update cases differently
				if newMCNode.Spec.ConfigImage.DesiredImage != newMCNode.Status.ConfigImage.CurrentImage {
					if newMCNode.Spec.ConfigImage.DesiredImage != "" { // Handle case when desired image exists
						updateToMessage = fmt.Sprintf("Action during update to %s: %s", newMCNode.Spec.ConfigImage.DesiredImage, condition.Message)
					} else { // When the desired image is empty, it means OCL is being disabled; provide a more useful message in this case
						updateToMessage = fmt.Sprintf("Action during update to disable image mode: %s", condition.Message)
					}
				}
				condition.Message = updateToMessage
			}
			condition.DeepCopyInto(&newMCNode.Status.Conditions[i])
		}
		if !foundChild && newChildCondition != nil {
			newMCNode.Status.Conditions = append(newMCNode.Status.Conditions, *newChildCondition)
		}
		if !foundParent {
			newMCNode.Status.Conditions = append(newMCNode.Status.Conditions, newParentCondition)
		}
	}

	// Set desired version in MCN.Status.ConfigVersion
	desiredAnnotation := NotYetSet
	// 	if the update is compatible, we can set the desired to the one being used in the update,
	// 	otherwise continue using the placeholder value
	if newParentCondition.Type == string(mcfgv1.MachineConfigNodeUpdatePrepared) && newParentCondition.Status == metav1.ConditionTrue || newParentCondition.Type != string(mcfgv1.MachineConfigNodeUpdatePrepared) && node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] != "" {
		desiredAnnotation = node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
	}
	if newMCNode.Status.ConfigVersion == nil {
		newMCNode.Status.ConfigVersion = &mcfgv1.MachineConfigNodeStatusMachineConfigVersion{
			Desired: desiredAnnotation,
		}
	} else {
		newMCNode.Status.ConfigVersion.Desired = desiredAnnotation
	}

	// Set current version in MCN.Status.ConfigVersion if node annotation exists
	if node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey] != "" {
		newMCNode.Status.ConfigVersion.Current = node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey]
	}

	// Set current and desired image values in MCN.Status.ConfigImage
	// This is only done when the ImageModeStatusReporting feature gate is enabled
	if fgHandler.Enabled(features.FeatureGateImageModeStatusReporting) {
		newMCNStatusConfigImage := mcfgv1.MachineConfigNodeStatusConfigImage{}
		currentImageAnnotation := node.Annotations[daemonconsts.CurrentImageAnnotationKey]
		desiredImageAnnotation := node.Annotations[daemonconsts.DesiredImageAnnotationKey]

		// Set current image if annotation exists
		if currentImageAnnotation != "" {
			newMCNStatusConfigImage.CurrentImage = mcfgv1.ImageDigestFormat(currentImageAnnotation)
		}

		// Set desired image if annotation exists
		if desiredImageAnnotation != "" {
			newMCNStatusConfigImage.DesiredImage = mcfgv1.ImageDigestFormat(desiredImageAnnotation)
		}

		newMCNode.Status.ConfigImage = newMCNStatusConfigImage
	}

	// if we do not need a new MCN, generate the apply configurations for this object
	if !needNewMCNode {
		statusconfigVersionApplyConfig := machineconfigurationv1.MachineConfigNodeStatusMachineConfigVersion().WithDesired(newMCNode.Status.ConfigVersion.Desired)
		if node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey] != "" {
			statusconfigVersionApplyConfig = statusconfigVersionApplyConfig.WithCurrent(newMCNode.Status.ConfigVersion.Current)
		}
		statusApplyConfig := machineconfigurationv1.MachineConfigNodeStatus().
			// WithConditions(newMCNode.Status.Conditions...).
			WithConditions(convertConditionsToApplyConfigurations(newMCNode.Status.Conditions)...).
			WithObservedGeneration(newMCNode.Generation + 1).
			WithConfigVersion(statusconfigVersionApplyConfig)

		// Add ConfigImage to apply configuration if feature gate is enabled and image annotations exist
		if fgHandler.Enabled(features.FeatureGateImageModeStatusReporting) && (newMCNode.Status.ConfigImage.CurrentImage != "" || newMCNode.Status.ConfigImage.DesiredImage != "") {
			configImageApplyConfig := machineconfigurationv1.MachineConfigNodeStatusConfigImage()

			// Set current image if it exists
			if newMCNode.Status.ConfigImage.CurrentImage != "" {
				configImageApplyConfig = configImageApplyConfig.WithCurrentImage(newMCNode.Status.ConfigImage.CurrentImage)
			}

			// Set desired image if it exists
			if newMCNode.Status.ConfigImage.DesiredImage != "" {
				configImageApplyConfig = configImageApplyConfig.WithDesiredImage(newMCNode.Status.ConfigImage.DesiredImage)
			}

			statusApplyConfig = statusApplyConfig.WithConfigImage(configImageApplyConfig)
		}

		if imageSetApplyConfig == nil {
			for _, imageSet := range newMCNode.Status.PinnedImageSets {
				// By default, a PinnedImageSet reference must include the name of the PIS and the desired generation
				pisApplyConfig := &machineconfigurationv1.MachineConfigNodeStatusPinnedImageSetApplyConfiguration{
					DesiredGeneration: ptr.To(imageSet.DesiredGeneration),
					Name:              ptr.To(imageSet.Name),
				}
				// Only set `CurrentGeneration` value when we are currently on a valid generation (imageSet.CurrentGeneration value is non-0)
				if imageSet.CurrentGeneration != 0 {
					pisApplyConfig.CurrentGeneration = ptr.To(imageSet.CurrentGeneration)
				}
				// Only set `LastFailedGeneration` value when it is a non-default (non-0) value
				if imageSet.LastFailedGeneration != 0 {
					pisApplyConfig.LastFailedGeneration = ptr.To(imageSet.LastFailedGeneration)
					pisApplyConfig.LastFailedGenerationError = ptr.To(imageSet.LastFailedGenerationError)
				}

				statusApplyConfig = statusApplyConfig.WithPinnedImageSets(pisApplyConfig)
			}
		} else if len(imageSetApplyConfig) > 0 {
			statusApplyConfig = statusApplyConfig.WithPinnedImageSets(imageSetApplyConfig...)
		}

		mcnodeApplyConfig := machineconfigurationv1.MachineConfigNode(newMCNode.Name).WithStatus(statusApplyConfig)
		_, err := mcfgClient.MachineconfigurationV1().MachineConfigNodes().ApplyStatus(context.TODO(), mcnodeApplyConfig, metav1.ApplyOptions{FieldManager: "machine-config-operator", Force: true})
		if err != nil {
			klog.Errorf("Error applying MCN status: %v", err)
			return err
		}
	} else if node.Status.Phase != corev1.NodePending && node.Status.Phase != corev1.NodePhase("Provisioning") {
		// there are cases where we get here before the MCO has settled and applied all of the MCnodes.
		newMCNode.Spec.ConfigVersion = mcfgv1.MachineConfigNodeSpecMachineConfigVersion{
			Desired: node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey],
		}
		if newMCNode.Spec.ConfigVersion.Desired == "" {
			newMCNode.Spec.ConfigVersion.Desired = NotYetSet
		}
		newMCNode.Name = node.Name
		newMCNode.Spec.Pool = mcfgv1.MCOObjectReference{Name: pool}
		newMCNode.Spec.Node = mcfgv1.MCOObjectReference{Name: node.Name}

		_, err := mcfgClient.MachineconfigurationV1().MachineConfigNodes().Create(context.TODO(), newMCNode, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Error creating MCN: %v", err)
			return err
		}
	}
	// if this is the first time we are applying the MCN and the node is ready, set the config version probably
	if node.Status.Phase != corev1.NodePending && node.Status.Phase != corev1.NodePhase("Provisioning") && newMCNode.Spec.ConfigVersion.Desired == "NotYetSet" {
		err := GenerateAndApplyMachineConfigNodeSpec(fgHandler, pool, node, mcfgClient)
		if err != nil {
			klog.Errorf("Error making MCN spec for Update Compatible: %v", err)
		}
	}
	return nil
}

func isParentConditionChanged(old, newCondition metav1.Condition) bool {
	return old.Status != newCondition.Status || old.Message != newCondition.Message
}

// isSingletonCondition checks if the condition is a singleton condition which means it will never have a child.
func isSingletonCondition(singletonConditionTypes []mcfgv1.StateProgress, conditionType string) bool {
	for _, cond := range singletonConditionTypes {
		if conditionType == string(cond) {
			return true
		}
	}
	return false
}

// UpdateMachineConfigNodeSpecDesiredAnnotations sets the desired config version and image
// annotation values in the `Spec` of an existing MachineConfigNode resource
func UpdateMachineConfigNodeSpecDesiredAnnotations(fgHandler ctrlcommon.FeatureGatesHandler, mcfgClient mcfgclientset.Interface, nodeName, desiredConfig, desiredImage string) error {
	if fgHandler == nil {
		return nil
	}

	// Get the existing MCN
	mcn, mcnErr := mcfgClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	// Note that this function is only intended to update the Spec of an existing MCN. We should
	// not reach this point if there is not an existing MCN for a node, but we need to handle the
	// DNE and other potential error situations just in case.
	if mcnErr != nil {
		return mcnErr
	}

	// Set the desired config annotation
	mcn.Spec.ConfigVersion.Desired = NotYetSet
	if desiredConfig != "" {
		mcn.Spec.ConfigVersion.Desired = desiredConfig
	}

	// Set the desired image annotation if the ImageModeStatusReporting feature gate is enabled
	if fgHandler.Enabled(features.FeatureGateImageModeStatusReporting) {
		// Set the desired image annotation
		mcn.Spec.ConfigImage = mcfgv1.MachineConfigNodeSpecConfigImage{}
		if desiredImage != "" {
			mcn.Spec.ConfigImage = mcfgv1.MachineConfigNodeSpecConfigImage{
				DesiredImage: mcfgv1.ImageDigestFormat(desiredImage),
			}
		}
	}

	// Update the MCN resource
	if _, err := mcfgClient.MachineconfigurationV1().MachineConfigNodes().Update(context.TODO(), mcn, metav1.UpdateOptions{FieldManager: "machine-config-operator"}); err != nil {
		return fmt.Errorf("failed to update the %s mcn spec with the new desired config and image value: %w", nodeName, err)
	}

	return nil
}

// GenerateAndApplyMachineConfigNodeSpec generates and applies a new MCN spec based off the node state
func GenerateAndApplyMachineConfigNodeSpec(fgHandler ctrlcommon.FeatureGatesHandler, pool string, node *corev1.Node, mcfgClient mcfgclientset.Interface) error {
	if fgHandler == nil || node == nil {
		return nil
	}

	// get the existing MCN, or if it DNE create one below
	mcNode, needNewMCNode := createOrGetMachineConfigNode(mcfgClient, node)
	newMCNode := mcNode.DeepCopy()
	// Set the MCN owner references
	newMCNode.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "v1",
			Name:       node.ObjectMeta.Name,
			Kind:       "Node",
			UID:        node.ObjectMeta.UID,
		},
	}

	// Set the desired config version in the MCN
	newMCNode.Spec.ConfigVersion = mcfgv1.MachineConfigNodeSpecMachineConfigVersion{
		Desired: node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey],
	}
	// If the desired config does not yet exist for the node, the desired config should be set to NotYetSet
	if newMCNode.Spec.ConfigVersion.Desired == "" {
		newMCNode.Spec.ConfigVersion.Desired = NotYetSet
	}

	// Check that the ImageModeStatusReporting feature gate is enabled
	if fgHandler.Enabled(features.FeatureGateImageModeStatusReporting) {
		// Set the desired image in the MCN if it exists
		newMCNode.Spec.ConfigImage = mcfgv1.MachineConfigNodeSpecConfigImage{}
		if node.Annotations[daemonconsts.DesiredImageAnnotationKey] != "" {
			newMCNode.Spec.ConfigImage.DesiredImage = mcfgv1.ImageDigestFormat(node.Annotations[daemonconsts.DesiredImageAnnotationKey])
		}
	}

	// Set the MCN pool and node names
	newMCNode.Spec.Pool = mcfgv1.MCOObjectReference{
		Name: pool,
	}
	newMCNode.Spec.Node = mcfgv1.MCOObjectReference{
		Name: node.Name,
	}

	// Update the existing MCN with the new Spec values or create a new MCN
	if !needNewMCNode {
		nodeRefApplyConfig := machineconfigurationv1.MCOObjectReference().WithName(newMCNode.Spec.Node.Name)
		poolRefApplyConfig := machineconfigurationv1.MCOObjectReference().WithName(newMCNode.Spec.Pool.Name)
		specconfigVersionApplyConfig := machineconfigurationv1.MachineConfigNodeSpecMachineConfigVersion().WithDesired(newMCNode.Spec.ConfigVersion.Desired)
		specApplyConfig := machineconfigurationv1.MachineConfigNodeSpec().WithNode(nodeRefApplyConfig).WithPool(poolRefApplyConfig).WithConfigVersion(specconfigVersionApplyConfig)
		mcnodeApplyConfig := machineconfigurationv1.MachineConfigNode(newMCNode.Name).WithSpec(specApplyConfig)
		_, err := mcfgClient.MachineconfigurationV1().MachineConfigNodes().Apply(context.TODO(), mcnodeApplyConfig, metav1.ApplyOptions{FieldManager: "machine-config-operator", Force: true})
		if err != nil {
			klog.Errorf("Error applying MCN Spec: %v", err)
			return err
		}
	} else {
		_, err := mcfgClient.MachineconfigurationV1().MachineConfigNodes().Create(context.TODO(), newMCNode, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Error creating MCN: %v", err)
			return err
		}
	}
	return nil
}

// createOrGetMachineConfigNode gets the named MCN or returns a boolean indicating we need to create one
func createOrGetMachineConfigNode(mcfgClient mcfgclientset.Interface, node *corev1.Node) (*mcfgv1.MachineConfigNode, bool) {
	mcNode, err := mcfgClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
	if err != nil {
		// no existing MCN found since no resource found, no error yet just create a new one
		if apierrors.IsNotFound((err)) {
			klog.V(4).Infof("MachineConfigNode for node %q not found, will create a new one", node.Name)
			return mcNode, true
		}
		// true error getting existing MCN
		klog.Errorf("error getting existing MCN: %v", err)
		return mcNode, true
	}
	return mcNode, false
}

type ApplyCallback struct {
	StatusConfigFn      func(applyConfig *machineconfigurationv1.MachineConfigNodeStatusApplyConfiguration)
	MachineConfigNodeFn func(*mcfgv1.MachineConfigNode)
}
