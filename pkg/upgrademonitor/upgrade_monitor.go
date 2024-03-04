package upgrademonitor

import (
	"context"
	"fmt"

	machineconfigurationalphav1 "github.com/openshift/client-go/machineconfiguration/applyconfigurations/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	v1 "github.com/openshift/api/config/v1"
	mcfgalphav1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
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
// nolint:gocyclo
func GenerateAndApplyMachineConfigNodes(parentCondition, childCondition *Condition, parentStatus, childStatus metav1.ConditionStatus, node *corev1.Node, mcfgClient mcfgclientset.Interface, fgAccessor featuregates.FeatureGateAccess) error {
	if fgAccessor == nil || node == nil || parentCondition == nil || mcfgClient == nil {
		return nil
	}
	fg, err := fgAccessor.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("Could not get fg: %v", err)
		return err
	}
	if fg == nil || !fg.Enabled(v1.FeatureGateMachineConfigNodes) {
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
		childDNEOrIsTheSame := true
		// look through all of the conditions for our current ones, update them accordingly
		// also set all other ones to false and update last transition time.
		for i, condition := range newMCNode.Status.Conditions {
			if condition.Type == string(mcfgalphav1.MachineConfigNodeUpdated) && condition.Status == metav1.ConditionTrue && condition.Type != newParentCondition.Type {
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
			} else if newChildCondition != nil && condition.Type == newChildCondition.Type {
				childDNEOrIsTheSame = false
				foundChild = true
				newChildCondition.DeepCopyInto(&condition)
				if newChildCondition.Status == condition.Status && newChildCondition.Message == condition.Message {
					childDNEOrIsTheSame = true
				}
			} else if condition.Type == newParentCondition.Type {
				foundParent = true
				if condition.Status == newParentCondition.Status && condition.Message == newParentCondition.Message && childDNEOrIsTheSame {
					// there is nothing to update. Return.
					// this allows us to put the conditions in more general places but if we are already in phases like "updated"
					// then nothing happens
					// only do this if the messages match too
					return nil
				}
				newParentCondition.DeepCopyInto(&condition)
			} else if condition.Status != metav1.ConditionFalse && reset {
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
	if node.Annotations["machineconfiguration.openshift.io/currentConfig"] != "" {
		newMCNode.Status.ConfigVersion = mcfgalphav1.MachineConfigNodeStatusMachineConfigVersion{
			Desired: newMCNode.Status.ConfigVersion.Desired,
			Current: node.Annotations["machineconfiguration.openshift.io/currentConfig"],
		}
	} else {
		newMCNode.Status.ConfigVersion = mcfgalphav1.MachineConfigNodeStatusMachineConfigVersion{
			Desired: newMCNode.Status.ConfigVersion.Desired,
		}
	}
	// if the update is compatible, we can set the desired to the one being used in the update
	// this happens either if we get prepared == true OR literally any other parent condition, since if we get past prepared, then the desiredConfig is correct.
	if newParentCondition.Type == string(mcfgalphav1.MachineConfigNodeUpdatePrepared) && newParentCondition.Status == metav1.ConditionTrue || newParentCondition.Type != string(mcfgalphav1.MachineConfigNodeUpdatePrepared) && node.Annotations["machineconfiguration.openshift.io/desiredConfig"] != "" {
		newMCNode.Status.ConfigVersion.Desired = node.Annotations["machineconfiguration.openshift.io/desiredConfig"]
	} else if newMCNode.Status.ConfigVersion.Desired == "" {
		newMCNode.Status.ConfigVersion.Desired = NotYetSet
	}

	// if we do not need a new MCN, generate the apply configurations for this object
	if !needNewMCNode {
		statusconfigVersionApplyConfig := machineconfigurationalphav1.MachineConfigNodeStatusMachineConfigVersion().WithDesired(newMCNode.Status.ConfigVersion.Desired)
		if node.Annotations["machineconfiguration.openshift.io/currentConfig"] != "" {
			statusconfigVersionApplyConfig = statusconfigVersionApplyConfig.WithCurrent(newMCNode.Status.ConfigVersion.Current)
		}
		statusApplyConfig := machineconfigurationalphav1.MachineConfigNodeStatus().WithConditions(newMCNode.Status.Conditions...).WithObservedGeneration(newMCNode.Generation + 1).WithConfigVersion(statusconfigVersionApplyConfig)
		mcnodeApplyConfig := machineconfigurationalphav1.MachineConfigNode(newMCNode.Name).WithStatus(statusApplyConfig)
		_, err := mcfgClient.MachineconfigurationV1alpha1().MachineConfigNodes().ApplyStatus(context.TODO(), mcnodeApplyConfig, metav1.ApplyOptions{FieldManager: "machine-config-operator", Force: true})
		if err != nil {
			klog.Errorf("Error applying MCN status: %v", err)
			return err
		}
	} else if node.Status.Phase != corev1.NodePending && node.Status.Phase != corev1.NodePhase("Provisioning") {
		// there are cases where we get here before the MCO has settled and applied all of the MCnodes.
		newMCNode.Spec.ConfigVersion = mcfgalphav1.MachineConfigNodeSpecMachineConfigVersion{
			Desired: node.Annotations["machineconfiguration.openshift.io/desiredConfig"],
		}
		if newMCNode.Spec.ConfigVersion.Desired == "" {
			newMCNode.Spec.ConfigVersion.Desired = NotYetSet
		}
		newMCNode.Name = node.Name
		newMCNode.Spec.Pool = mcfgalphav1.MCOObjectReference{Name: pool}
		newMCNode.Spec.Node = mcfgalphav1.MCOObjectReference{Name: node.Name}
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
	if fg == nil || !fg.Enabled(v1.FeatureGateMachineConfigNodes) {
		klog.Infof("MCN Featuregate is not enabled. Please enable the TechPreviewNoUpgrade featureset to use MachineConfigNodes")
		return nil
	}
	// get the existing MCN, or if it DNE create one below
	mcNode, needNewMCNode := createOrGetMachineConfigNode(mcfgClient, node)
	newMCNode := mcNode.DeepCopy()
	// set the spec config version
	newMCNode.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion: node.APIVersion,
			Name:       node.ObjectMeta.Name,
			Kind:       node.Kind,
			UID:        node.ObjectMeta.UID,
		},
	}
	newMCNode.Spec.ConfigVersion = mcfgalphav1.MachineConfigNodeSpecMachineConfigVersion{
		Desired: node.Annotations["machineconfiguration.openshift.io/desiredConfig"],
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
