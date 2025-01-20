package upgrademonitor

import (
	"context"
	"fmt"

	features "github.com/openshift/api/features"
	machineconfigurationalphav1 "github.com/openshift/client-go/machineconfiguration/applyconfigurations/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"

	mcfgalphav1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	applyconfigurationsmeta "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/klog/v2"

	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const NotYetSet = "NotYetSet"

type Condition struct {
	State   mcfgalphav1.StateProgress
	Reason  string
	Message string
}

// GenerateAndApplyMachineConfigNodes takes a parent and child conditions and applies them to the given node's MachineConfigNode object
// there are a few stipulations. 1) if the parent and child condition exactly match their currently applied statuses, no new MCN is generated
// 2) the desiredConfig in the MCN Status will only be set once the update is proven to be compatible. Meanwhile the desired and current config in the spec react to live changes of state on the Node
// 3) None of this will be executed unless the TechPreviewNoUpgrade featuregate is applied.
func GenerateAndApplyMachineConfigNodes(
	parentCondition,
	childCondition *Condition,
	parentStatus,
	childStatus metav1.ConditionStatus,
	node *corev1.Node,
	mcfgClient mcfgclientset.Interface,
	fgAccessor featuregates.FeatureGateAccess,
) error {
	return generateAndApplyMachineConfigNodes(parentCondition, childCondition, parentStatus, childStatus, node, mcfgClient, nil, nil, fgAccessor)
}

func UpdateMachineConfigNodeStatus(
	parentCondition,
	childCondition *Condition,
	parentStatus,
	childStatus metav1.ConditionStatus,
	node *corev1.Node,
	mcfgClient mcfgclientset.Interface,
	imageSetApplyConfig []*machineconfigurationalphav1.MachineConfigNodeStatusPinnedImageSetApplyConfiguration,
	imageSetSpec []mcfgalphav1.MachineConfigNodeSpecPinnedImageSet,
	fgAccessor featuregates.FeatureGateAccess,
) error {
	return generateAndApplyMachineConfigNodes(parentCondition, childCondition, parentStatus, childStatus, node, mcfgClient, imageSetApplyConfig, imageSetSpec, fgAccessor)
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
	imageSetApplyConfig []*machineconfigurationalphav1.MachineConfigNodeStatusPinnedImageSetApplyConfiguration,
	imageSetSpec []mcfgalphav1.MachineConfigNodeSpecPinnedImageSet,
	fgAccessor featuregates.FeatureGateAccess,
) error {
	if fgAccessor == nil || node == nil || parentCondition == nil || mcfgClient == nil {
		return nil
	}
	fg, err := fgAccessor.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("Could not get fg: %v", err)
		return err
	}
	if fg == nil || !fg.Enabled(features.FeatureGateMachineConfigNodes) {
		return nil
	}

	var pool string
	var ok bool
	if _, ok = node.Labels["node-role.kubernetes.io/worker"]; ok {
		pool = "worker"
	} else if _, ok = node.Labels["node-role.kubernetes.io/master"]; ok {
		pool = "master"
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
	if newParentCondition.Type == string(mcfgalphav1.MachineConfigNodeUpdated) {
		reset = true
	}

	// singleton conditions are conditions that should only have one instance (no children) in the MCN status.
	var singletonConditionTypes []mcfgalphav1.StateProgress
	if fg.Enabled(features.FeatureGatePinnedImages) {
		singletonConditionTypes = append(singletonConditionTypes, mcfgalphav1.MachineConfigNodePinnedImageSetsDegraded, mcfgalphav1.MachineConfigNodePinnedImageSetsProgressing)
	}

	// we use this array to see if the MCN has all of its conditions set
	// if not we set a sane default
	allConditionTypes := []mcfgalphav1.StateProgress{
		mcfgalphav1.MachineConfigNodeUpdatePrepared,
		mcfgalphav1.MachineConfigNodeUpdateExecuted,
		mcfgalphav1.MachineConfigNodeUpdatePostActionComplete,
		mcfgalphav1.MachineConfigNodeUpdateComplete,
		mcfgalphav1.MachineConfigNodeResumed,
		mcfgalphav1.MachineConfigNodeUpdateCompatible,
		mcfgalphav1.MachineConfigNodeUpdateDrained,
		mcfgalphav1.MachineConfigNodeUpdateFilesAndOS,
		mcfgalphav1.MachineConfigNodeUpdateCordoned,
		mcfgalphav1.MachineConfigNodeUpdateRebooted,
		mcfgalphav1.MachineConfigNodeUpdateReloaded,
		mcfgalphav1.MachineConfigNodeUpdated,
		mcfgalphav1.MachineConfigNodeUpdateUncordoned,
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
				if condType == mcfgalphav1.StateProgress(cond.Type) {
					found = true
				}
			}
			// else if we do not have this one yet, set it to some sane default.
			if !found {
				newMCNode.Status.Conditions = append(newMCNode.Status.Conditions,
					metav1.Condition{
						Type:               string(condType),
						Message:            fmt.Sprintf("This node has not yet entered the %s phase", string(condType)),
						Reason:             "NotYetOccured",
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
			case condition.Type == string(mcfgalphav1.MachineConfigNodeUpdated) && condition.Status == metav1.ConditionTrue && condition.Type != newParentCondition.Type:
				// if this happens, it is because we manually updated the MCO.
				// so, if we get a parent state == unknown or true or ANYTHING and updated also == true but it isn't the parent, set updated == false
				newC := metav1.Condition{
					Type:               string(mcfgalphav1.MachineConfigNodeUpdated),
					Message:            "This node is not updated, sensed disruption via a manual update.",
					Reason:             string(mcfgalphav1.MachineConfigNodeUpdated),
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
				condition.Message = fmt.Sprintf("Action during update to %s: %s", newMCNode.Spec.ConfigVersion.Desired, condition.Message)
				condition.LastTransitionTime = metav1.Now()
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

	// for now, keep spec and status aligned
	if node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey] != "" {
		newMCNode.Status.ConfigVersion = mcfgalphav1.MachineConfigNodeStatusMachineConfigVersion{
			Desired: newMCNode.Status.ConfigVersion.Desired,
			Current: node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey],
		}
	} else {
		newMCNode.Status.ConfigVersion = mcfgalphav1.MachineConfigNodeStatusMachineConfigVersion{
			Desired: newMCNode.Status.ConfigVersion.Desired,
		}
	}
	// if the update is compatible, we can set the desired to the one being used in the update
	// this happens either if we get prepared == true OR literally any other parent condition, since if we get past prepared, then the desiredConfig is correct.
	if newParentCondition.Type == string(mcfgalphav1.MachineConfigNodeUpdatePrepared) && newParentCondition.Status == metav1.ConditionTrue || newParentCondition.Type != string(mcfgalphav1.MachineConfigNodeUpdatePrepared) && node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] != "" {
		newMCNode.Status.ConfigVersion.Desired = node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey]
	} else if newMCNode.Status.ConfigVersion.Desired == "" {
		newMCNode.Status.ConfigVersion.Desired = NotYetSet
	}

	// if we do not need a new MCN, generate the apply configurations for this object
	if !needNewMCNode {
		statusconfigVersionApplyConfig := machineconfigurationalphav1.MachineConfigNodeStatusMachineConfigVersion().WithDesired(newMCNode.Status.ConfigVersion.Desired)
		if node.Annotations[daemonconsts.CurrentMachineConfigAnnotationKey] != "" {
			statusconfigVersionApplyConfig = statusconfigVersionApplyConfig.WithCurrent(newMCNode.Status.ConfigVersion.Current)
		}
		statusApplyConfig := machineconfigurationalphav1.MachineConfigNodeStatus().
			// WithConditions(newMCNode.Status.Conditions...).
			WithConditions(convertConditionsToApplyConfigurations(newMCNode.Status.Conditions)...).
			WithObservedGeneration(newMCNode.Generation + 1).
			WithConfigVersion(statusconfigVersionApplyConfig)

		if fg.Enabled(features.FeatureGatePinnedImages) {
			if imageSetApplyConfig == nil {
				for _, imageSet := range newMCNode.Status.PinnedImageSets {
					statusApplyConfig = statusApplyConfig.WithPinnedImageSets(&machineconfigurationalphav1.MachineConfigNodeStatusPinnedImageSetApplyConfiguration{
						DesiredGeneration:          ptr.To(imageSet.DesiredGeneration),
						CurrentGeneration:          ptr.To(imageSet.CurrentGeneration),
						Name:                       ptr.To(imageSet.Name),
						LastFailedGeneration:       ptr.To(imageSet.LastFailedGeneration),
						LastFailedGenerationErrors: imageSet.LastFailedGenerationErrors,
					})
				}
			} else if len(imageSetApplyConfig) > 0 {
				statusApplyConfig = statusApplyConfig.WithPinnedImageSets(imageSetApplyConfig...)
			}
		}

		mcnodeApplyConfig := machineconfigurationalphav1.MachineConfigNode(newMCNode.Name).WithStatus(statusApplyConfig)
		_, err := mcfgClient.MachineconfigurationV1alpha1().MachineConfigNodes().ApplyStatus(context.TODO(), mcnodeApplyConfig, metav1.ApplyOptions{FieldManager: "machine-config-operator", Force: true})
		if err != nil {
			klog.Errorf("Error applying MCN status: %v", err)
			return err
		}
	} else if node.Status.Phase != corev1.NodePending && node.Status.Phase != corev1.NodePhase("Provisioning") {
		// there are cases where we get here before the MCO has settled and applied all of the MCnodes.
		newMCNode.Spec.ConfigVersion = mcfgalphav1.MachineConfigNodeSpecMachineConfigVersion{
			Desired: node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey],
		}
		if newMCNode.Spec.ConfigVersion.Desired == "" {
			newMCNode.Spec.ConfigVersion.Desired = NotYetSet
		}
		newMCNode.Name = node.Name
		newMCNode.Spec.Pool = mcfgalphav1.MCOObjectReference{Name: pool}
		newMCNode.Spec.Node = mcfgalphav1.MCOObjectReference{Name: node.Name}
		if imageSetSpec != nil {
			newMCNode.Spec.PinnedImageSets = imageSetSpec
		}

		_, err := mcfgClient.MachineconfigurationV1alpha1().MachineConfigNodes().Create(context.TODO(), newMCNode, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Error creating MCN: %v", err)
			return err
		}
	}
	// if this is the first time we are applying the MCN and the node is ready, set the config version probably
	if node.Status.Phase != corev1.NodePending && node.Status.Phase != corev1.NodePhase("Provisioning") && newMCNode.Spec.ConfigVersion.Desired == "NotYetSet" {
		err = GenerateAndApplyMachineConfigNodeSpec(fgAccessor, pool, node, mcfgClient)
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
func isSingletonCondition(singletonConditionTypes []mcfgalphav1.StateProgress, conditionType string) bool {
	for _, cond := range singletonConditionTypes {
		if conditionType == string(cond) {
			return true
		}
	}
	return false
}

// GenerateAndApplyMachineConfigNodeSpec generates and applies a new MCN spec based off the node state
func GenerateAndApplyMachineConfigNodeSpec(fgAccessor featuregates.FeatureGateAccess, pool string, node *corev1.Node, mcfgClient mcfgclientset.Interface) error {
	if fgAccessor == nil || node == nil {
		return nil
	}
	fg, err := fgAccessor.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("Could not get fg: %v", err)
		return err
	}
	if fg == nil || !fg.Enabled(features.FeatureGateMachineConfigNodes) {
		klog.Infof("MCN Featuregate is not enabled. Please enable the TechPreviewNoUpgrade featureset to use MachineConfigNodes")
		return nil
	}
	// get the existing MCN, or if it DNE create one below
	mcNode, needNewMCNode := createOrGetMachineConfigNode(mcfgClient, node)
	newMCNode := mcNode.DeepCopy()
	// set the spec config version
	newMCNode.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: "v1",
			Name:       node.ObjectMeta.Name,
			Kind:       "Node",
			UID:        node.ObjectMeta.UID,
		},
	}

	newMCNode.Spec.ConfigVersion = mcfgalphav1.MachineConfigNodeSpecMachineConfigVersion{
		Desired: node.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey],
	}
	// Set desired config to NotYetSet if the annotation is empty to satisfy API validation
	if newMCNode.Spec.ConfigVersion.Desired == "" {
		newMCNode.Spec.ConfigVersion.Desired = NotYetSet
	}

	newMCNode.Spec.Pool = mcfgalphav1.MCOObjectReference{
		Name: pool,
	}
	newMCNode.Spec.Node = mcfgalphav1.MCOObjectReference{
		Name: node.Name,
	}
	if !needNewMCNode {
		nodeRefApplyConfig := machineconfigurationalphav1.MCOObjectReference().WithName(newMCNode.Spec.Node.Name)
		poolRefApplyConfig := machineconfigurationalphav1.MCOObjectReference().WithName(newMCNode.Spec.Pool.Name)
		specconfigVersionApplyConfig := machineconfigurationalphav1.MachineConfigNodeSpecMachineConfigVersion().WithDesired(newMCNode.Spec.ConfigVersion.Desired)
		specApplyConfig := machineconfigurationalphav1.MachineConfigNodeSpec().WithNode(nodeRefApplyConfig).WithPool(poolRefApplyConfig).WithConfigVersion(specconfigVersionApplyConfig)
		mcnodeApplyConfig := machineconfigurationalphav1.MachineConfigNode(newMCNode.Name).WithSpec(specApplyConfig)
		_, err := mcfgClient.MachineconfigurationV1alpha1().MachineConfigNodes().Apply(context.TODO(), mcnodeApplyConfig, metav1.ApplyOptions{FieldManager: "machine-config-operator", Force: true})
		if err != nil {
			klog.Errorf("Error applying MCN Spec: %v", err)
			return err
		}
	} else {
		_, err := mcfgClient.MachineconfigurationV1alpha1().MachineConfigNodes().Create(context.TODO(), newMCNode, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Error creating MCN: %v", err)
			return err
		}
	}
	return nil
}

// createOrGetMachineConfigNode gets the named MCN or returns a boolean indicating we need to create one
func createOrGetMachineConfigNode(mcfgClient mcfgclientset.Interface, node *corev1.Node) (*mcfgalphav1.MachineConfigNode, bool) {
	mcNode, err := mcfgClient.MachineconfigurationV1alpha1().MachineConfigNodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
	if mcNode.Name == "" || (err != nil && apierrors.IsNotFound(err)) {
		klog.Errorf("error getting existing MCN: %v", err)
		return mcNode, true
	}

	return mcNode, false
}

type ApplyCallback struct {
	StatusConfigFn      func(applyConfig *machineconfigurationalphav1.MachineConfigNodeStatusApplyConfiguration)
	MachineConfigNodeFn func(*mcfgalphav1.MachineConfigNode)
}

func applyStatusConfig(cfg *machineconfigurationalphav1.MachineConfigNodeStatusApplyConfiguration, applyCallback ...*ApplyCallback) {
	for _, apply := range applyCallback {
		apply.StatusConfigFn(cfg)
	}
}

func applyMachineConfigNode(mcn *mcfgalphav1.MachineConfigNode, applyCallback ...*ApplyCallback) {
	for _, apply := range applyCallback {
		apply.MachineConfigNodeFn(mcn)
	}
}
