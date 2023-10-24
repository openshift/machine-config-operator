package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcfgalphav1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	opv1 "github.com/openshift/api/operator/v1"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

type ComponentSyncProgression struct {
	Phase   ComponentSyncProgressionPhase
	Message string
	Error   error
}

type ComponentSyncProgressionPhase string

const (
	FetchingObject       ComponentSyncProgressionPhase = "FetchingObject"
	UpdatingObjectStatus ComponentSyncProgressionPhase = "UpdatingObjectStatus"
	UpdatingObjectconfig ComponentSyncProgressionPhase = "UpdatingObjectStatus"
)

// oc get machine-state
// oc get machine-state/mcc
// oc describe machine-state/mcc
// inside of this, we can have the persistent mcc/mcd/operator/upgrading states.
// though operator and upgrading will look different

// Each of these need an eventhandler ?
// ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigdaemon", Host: nodeName})),
func (ctrl *Controller) syncAll(syncFuncs []syncFunc, obj string) error {
	// ok here we need to look at what kind of object we have. and then make a machinestate based off of which type of object we are looking at?
	// or ig make all of them. and if any of them change based off the object spec/status, update here?
	/*for _, syncFunc := range syncFuncs {
		objAndNamespace := strings.Split(obj, "/")
		if len(objAndNamespace) != 2 {
			klog.Infof("Obj: %s", obj)
			return fmt.Errorf("Object could not be split up, %s", obj)
		}
		var object interface{}
		var err error

			object, err = ctrl.Kubeclient.CoreV1().Nodes().Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
			if err == nil {
				return syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().KubeletConfigs().Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().ControllerConfigs().Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}

			return fmt.Errorf("Object %s not found", objAndNamespace[1])
			// find out which type of obj we are
			/*
				object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
				if err != nil {
					object, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metamcfgalphav1.GetOptions{})
					if err != nil {
						return err
					}
					// split up event into its pieces
					ev := object.(*corev1.Event)
					for i := 0; i < 2; i++ {
						klog.Infof("Looking at event: %s", ev.Name)
						annos := make(map[string]string)
						noValFound := true // if we do not find number in list, that means we have reached the end
						for key, val := range ev.Annotations {
							klog.Infof("annotation: %s %s", key, val)
							if strings.Contains(key, fmt.Sprintf("%d.", i)) {
								noValFound = false
								annos[strings.Split(key, ".")[1]] = val
							}
						}
						if noValFound {
							klog.Infof("did not find an annotation. breaking")
							break // reached our max number or there is no number
						}

						// add some careful parsing here for weird situations
						var reason, message string
						reasons := strings.Split(ev.Reason, ".")
						messages := strings.Split(ev.Message, ".")
						if len(reasons) >= i+1 {
							reason = strings.Split(ev.Reason, ".")[i]
						} else {
							reason = ev.Reason
						}
						if len(messages) >= i+1 {
							message = strings.Split(ev.Message, ".")[i]
						} else {
							message = ev.Message
						}
						newEv := ev.DeepCopy()
						newEv.Message = message
						newEv.Reason = reason
						newEv.SetAnnotations(annos)
						klog.Infof("event Reason %s and event message %s", newEv.Message, newEv.Reason)
						syncFunc.fn(newEv) // use a new and singular event to pass to our sync functions
					}
				} else {
					if out := syncFunc.fn(object); out != nil { // if we just have a machinestate, we can pass that
						return out
					}
				}

	}
	*/
	return nil
}

// namespace/event
// namespace/machine-state-name
func (ctrl *Controller) syncMCC(obj interface{}) error {
	var msToSync *mcfgalphav1.MachineConfigNode
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*mcfgalphav1.MachineConfigNode); !ok {
		msToSync = nil
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}
	// we either have an event or a machineState
	if msToSync != nil && msToSync.Name == "mcc-health" {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "mcc-health" {
			//	// return ctrl.WriteStatus("mcc-health", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	klog.Infof("no event component or MS name found.")
	return nil
}

func (ctrl *Controller) syncMCD(obj interface{}) error {
	//syncNode
	var msToSync *mcfgalphav1.MachineConfigNode
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*mcfgalphav1.MachineConfigNode); !ok {
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}

	// we either have an event or a machineState
	if msToSync != nil && msToSync.Name == "mcd-health" {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "mcd-health" {
			klog.Infof("RECIEVED EVENT FROM DAEMON: %s %s", eventToSync.Message, eventToSync.Reason)
			// return ctrl.WriteStatus("mcd-health", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	return nil
}

func (ctrl *Controller) syncMCS(obj interface{}) error {
	var msToSync *mcfgalphav1.MachineConfigNode
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*mcfgalphav1.MachineConfigNode); !ok {
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}

	// we either have an event or a machineState
	if msToSync != nil && msToSync.Name == "mcs-health" {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "mcs-health" {
			// return ctrl.WriteStatus("mcs-health", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	return nil
}

func (ctrl *Controller) syncMetrics(obj interface{}) error {
	// get requests
	// update metrics requested, or register, or degregister
	var msToSync *mcfgalphav1.MachineConfigNode
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*mcfgalphav1.MachineConfigNode); !ok {
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}
	// we either have an event or a machineState
	if msToSync != nil && msToSync.Name == "metrics" {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "metrics" {
			// return ctrl.WriteStatus("metrics", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	return nil

}

func (ctrl *Controller) syncUpgradingProgression(obj interface{}) error {
	var msToSync *mcfgalphav1.MachineConfigNode
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*mcfgalphav1.MachineConfigNode); !ok {
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}

	// we either have an event or a machineState
	if msToSync != nil && strings.Contains(msToSync.Name, "upgrade") {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "upgrade-health" {
			if _, ok := eventToSync.Annotations["Pool"]; ok {
				if eventToSync.Annotations["Pool"] == "worker" {
					// return ctrl.WriteStatus("upgrade-worker", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
				} else if eventToSync.Annotations["Pool"] == "master" {
					// return ctrl.WriteStatus("upgrade-master", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
				}
			}
		}
	}
	return nil
}

func (ctrl *Controller) syncOperatorProgression(obj interface{}) error {
	var msToSync *mcfgalphav1.MachineConfigNode
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*mcfgalphav1.MachineConfigNode); !ok {
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}

	// we either have an event or a machineState
	if msToSync != nil && msToSync.Name == "operator-health" {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "operator-health" {
			// return ctrl.WriteStatus("operator-health", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	return nil

}

func (ctrl *Controller) GenerateMachineConfigNodes(object interface{}) []*mcfgalphav1.MachineConfigNode {
	var msArr []*mcfgalphav1.MachineConfigNode
	var ParentState mcfgalphav1.StateProgress
	var ParentPhase string
	var ParentReason string

	var ChildState mcfgalphav1.StateProgress
	var ChildPhase string
	var ChildReason string

	// We Should have 2 true conditions and all of the other ones should be false
	// 1 == higher up state (updating, updateinprogress)
	// 2 == phase (cordoning, draining etc)
	klog.Infof("OBJ: %s", object.(string))
	if nodeToSync, err := ctrl.nodeInformer.Lister().Get(object.(string)); err == nil {
		klog.Infof("GOT NODE %s", nodeToSync.Name)
		ms, _ := ctrl.Mcfgclient.MachineconfigurationV1alpha1().MachineConfigNodes().Get(context.TODO(), nodeToSync.Name, metav1.GetOptions{})
		newMS := ms.DeepCopy()
		// if the config annotations differ, we are upgrading. else we are not.
		if nodeToSync.Annotations[constants.DesiredMachineConfigAnnotationKey] != nodeToSync.Annotations[constants.CurrentMachineConfigAnnotationKey] {
			switch nodeToSync.Annotations[constants.MachineConfigDaemonStateAnnotationKey] {
			case constants.MachineConfigDaemonStateWorkPerparing:
				ChildState = mcfgalphav1.StateProgress(nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey])
				ParentState = mcfgalphav1.MachineConfigPoolUpdatePreparing
				ParentPhase = string(ChildState)
				ParentReason = fmt.Sprintf("Update Preparing. Currently: %s", string(ChildState))
				ChildPhase = fmt.Sprintf("%s%s", string(mcfgalphav1.MachineConfigPoolUpdatePreparing), string(ChildState))
				ChildReason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
			case constants.MachineConfigDaemonStateWorking:
				if nodeToSync.Annotations[constants.LastAppliedDrainerAnnotationKey] != nodeToSync.Annotations[constants.DesiredDrainerAnnotationKey] {
					desiredVerb := strings.Split(nodeToSync.Annotations[constants.DesiredDrainerAnnotationKey], "-")[0]
					switch desiredVerb {
					// this might not be right. We seem to drain/uncordon more than I assumed
					case constants.DrainerStateDrain:
						// we are draining
						ChildState = mcfgalphav1.StateProgress(nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey])
						ParentState = mcfgalphav1.MachineConfigPoolUpdateInProgress
						ParentPhase = string(ChildState)
						ParentReason = fmt.Sprintf("Update In Progress. Currently: %s", string(ChildState))
						ChildPhase = fmt.Sprintf("%s%s", string(mcfgalphav1.MachineConfigPoolUpdateInProgress), string(ChildState))
						ChildReason = fmt.Sprintf("Draining node. Desired Drainer is %s", nodeToSync.Annotations[constants.DesiredDrainerAnnotationKey])
					case constants.DrainerStateUncordon:
						// we are uncordoning, this only happens (I think) in completeUpdate. If not, we need another state for this
						ChildState = mcfgalphav1.StateProgress(nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey])
						ParentState = mcfgalphav1.MachineConfigPoolUpdateCompleting
						ParentPhase = string(ChildState)
						ParentReason = fmt.Sprintf("Update Completing. Currently: %s", string(ChildState))
						ChildPhase = fmt.Sprintf("%s%s", string(mcfgalphav1.MachineConfigPoolUpdateCompleting), string(ChildState))
						ChildReason = fmt.Sprintf("Cordining/Uncordoning node. Desired Drainer is %s", nodeToSync.Annotations[constants.DesiredDrainerAnnotationKey])
					}
					// we are draining
					break
				}
				ChildState = mcfgalphav1.StateProgress(nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey])
				ParentState = mcfgalphav1.MachineConfigPoolUpdateInProgress
				ParentPhase = string(ChildState)
				ParentReason = fmt.Sprintf("Update in Progress. Currently: %s", string(ChildState))
				ChildPhase = fmt.Sprintf("%s%s", string(mcfgalphav1.MachineConfigPoolUpdateInProgress), string(ChildState))
				ChildReason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
			case constants.MachineConfigDaemonStateWorkPostAction:
				// updatePostAction
				ChildState = mcfgalphav1.StateProgress(nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey])
				ParentState = mcfgalphav1.MachineConfigPoolUpdatePostAction
				ParentPhase = string(ChildState)
				ParentReason = fmt.Sprintf("Update in Post Action Phase. Currently: %s", string(ChildState))
				ChildPhase = fmt.Sprintf("%s%s", string(mcfgalphav1.MachineConfigPoolUpdatePostAction), string(ChildState))
				ChildReason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
			case constants.MachineConfigDaemonResuming:
				ChildState = mcfgalphav1.StateProgress(nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey])
				ParentState = mcfgalphav1.MachineConfigPoolResuming
				ParentPhase = string(ChildState)
				ParentReason = fmt.Sprintf("Update Complete. Currently: %s", string(ChildState))
				ChildPhase = fmt.Sprintf("%s%s", string(mcfgalphav1.MachineConfigPoolResuming), string(ChildState))
				ChildReason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
			case constants.MachineConfigDaemonStateDegraded:
				ParentState = mcfgalphav1.MachineConfigNodeErrored
				ParentPhase = nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey]
				ParentReason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]

			}
		} else {
			// else they are equal... but we need to figure out: were we just updating though?
			alreadyUpdated := false
			for _, s := range ms.Status.Conditions {
				if mcfgalphav1.StateProgress(s.Type) == mcfgalphav1.MachineConfigPoolUpdateComplete && s.Status == metav1.ConditionTrue {
					alreadyUpdated = true
				}
			}
			// if the MCO was updating, but no longer has mismatched configs, that means the update is complete
			if !alreadyUpdated {
				ParentState = mcfgalphav1.MachineConfigPoolUpdateComplete
				ParentPhase = "NodeUpgradeComplete"
				ParentReason = fmt.Sprintf("Node has completed update to config %s", nodeToSync.Annotations[constants.CurrentMachineConfigAnnotationKey])
			} else {
				// else we are already Ready. No need to update
				return nil
			}
		}
		newParentCondition := metav1.Condition{
			Type:               string(ParentState),
			Status:             metav1.ConditionTrue,
			Reason:             ParentPhase,
			Message:            ParentReason,
			LastTransitionTime: metav1.Now(),
		}
		var newChildCondition *metav1.Condition
		var c2 []byte
		if len(ChildState) > 0 {
			newChildCondition = &metav1.Condition{
				Type:               string(ChildState),
				Status:             metav1.ConditionTrue,
				Reason:             ChildPhase,
				Message:            ChildReason,
				LastTransitionTime: metav1.Now(),
			}
			c2, _ = json.Marshal(&newChildCondition)
			klog.Infof("new condition 1: %s", string(c2))
		}

		c1, _ := json.Marshal(newParentCondition)
		klog.Infof("new condition 1: %s", string(c1))

		allConditionTypes := []mcfgalphav1.StateProgress{
			mcfgalphav1.MachineConfigPoolUpdatePreparing,
			mcfgalphav1.MachineConfigPoolUpdateInProgress,
			mcfgalphav1.MachineConfigPoolUpdatePostAction,
			mcfgalphav1.MachineConfigPoolUpdateCompleting,
			mcfgalphav1.MachineConfigPoolUpdateComplete,
			mcfgalphav1.MachineConfigPoolResuming,
			mcfgalphav1.MachineConfigNodeErrored,
			mcfgalphav1.MachineConfigPoolUpdateComparingMC,
			mcfgalphav1.MachineConfigPoolUpdateDraining,
			mcfgalphav1.MachineConfigPoolUpdateFilesAndOS,
			mcfgalphav1.MachineConfigPoolUpdateCordoning,
			mcfgalphav1.MachineConfigPoolUpdateRebooting,
			mcfgalphav1.MachineConfigPoolUpdateReloading,
		}
		// create all of the conditions, even the false ones
		if newMS.Status.Conditions == nil {
			newMS.Status.Conditions = []metav1.Condition{}
			newMS.Status.Conditions = append(newMS.Status.Conditions, newParentCondition)
			if newChildCondition != nil {
				newMS.Status.Conditions = append(newMS.Status.Conditions, *newChildCondition)
			}
			for _, condType := range allConditionTypes {
				found := false
				for _, cond := range newMS.Status.Conditions {
					// if this is one of our two conditions, do not nullify this
					if condType == mcfgalphav1.StateProgress(cond.Type) {
						found = true
					}
				}
				// else if we do not have this one yet, set it to some sane default.
				if !found {
					newMS.Status.Conditions = append(newMS.Status.Conditions,
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
			foundChild := false
			foundParent := false
			// look through all of the conditions for our current ones, updat them accordingly
			// also set all other ones to false and update last transition time.
			for i, condition := range newMS.Status.Conditions {
				if newChildCondition != nil && condition.Type == newChildCondition.Type {
					foundChild = true
					newChildCondition.DeepCopyInto(&condition)
				} else if condition.Type == newParentCondition.Type {
					foundParent = true
					newParentCondition.DeepCopyInto(&condition)
				} else if condition.Status != metav1.ConditionFalse {
					condition.Status = metav1.ConditionFalse
					condition.LastTransitionTime = metav1.Now()
				}
				condition.DeepCopyInto(&newMS.Status.Conditions[i])
			}
			// I don't think this'll happen given the above logic, but if somehow we do not have an entry for this yet, add one.
			if !foundChild && newChildCondition != nil {
				newMS.Status.Conditions = append(newMS.Status.Conditions, *newChildCondition)
			}
			if !foundParent {
				newMS.Status.Conditions = append(newMS.Status.Conditions, newParentCondition)
			}
		}

		newMS.Spec.ConfigVersion = mcfgalphav1.MachineConfigVersion{
			Desired: nodeToSync.Annotations["machineconfiguration.openshift.io/desiredConfig"],
			Current: nodeToSync.Annotations["machineconfiguration.openshift.io/currentConfig"],
		}

		msArr = append(msArr, newMS)

	}
	return msArr
}

func (ctrl *Controller) GenerateMachineConfiguration(object interface{}) []opv1.MachineConfiguration {
	mcfgArr := []opv1.MachineConfiguration{}

	objectToUse := ""
	objAndNamespace := strings.Split(object.(string), "/")
	if len(objAndNamespace) != 2 {
		klog.Infof("Obj: %s", object.(string))
		// just try it
		objectToUse = objAndNamespace[0]
		//return fmt.Errorf("Object could not be split up, %s", key)
	} else {
		objectToUse = objAndNamespace[1]
	}

	// how are we going to do this.
	// we get word that an object was either created, updated, or deleted.
	// certain objects are modified in certain areas of the mco and in certain ways
	// so if we can create a layout map of who is modified where, we can figure out what we are doing.
	ms, err := ctrl.Opv1client.OperatorV1().MachineConfigurations().Get(context.TODO(), "default", metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Err getting mcfg: %w", err)
		return mcfgArr
	}
	newMcfg := ms.DeepCopy()
	ParentCondition := metav1.Condition{}
	ChildCondition := metav1.Condition{}
	if cc, err := ctrl.ccInformer.Lister().Get(objectToUse); err == nil {
		// render config generated by the operator
		// but not applied or updated until the syncControllerConfig call from the controller's sync loop
		ParentCondition = metav1.Condition{
			Type:    string(opv1.OperatorSync),
			Status:  metav1.ConditionTrue,
			Reason:  string(opv1.MCCSyncControllerConfig),
			Message: "Operator has generated and is applying a new CConfig based off of renderconfig",
		}
		ChildCondition = metav1.Condition{
			Type:    string(opv1.MCCSyncControllerConfig),
			Status:  metav1.ConditionTrue,
			Reason:  fmt.Sprintf("%s%s", string(opv1.OperatorSync), string(opv1.MCCSyncControllerConfig)),
			Message: fmt.Sprintf("Applying ControllerConfig %s during operator sync", cc.Name),
		}
		klog.Infof("Got an edit on cc: %s Not Implemeneted yet :)", cc.Name)
	} else if mc, err := ctrl.mcInformer.Lister().Get(objectToUse); err == nil {
		// machineconfigs are modified in the following situations:
		// ContainerRuntimeConfig
		// KubeletConfig
		// Rendered MachineConfigs
		// we need to determine the name of the mc
		var childCondition opv1.StateProgress
		splitMCName := strings.Split(mc.Name, "-")
		switch splitMCName[len(splitMCName)-1] {
		case "kubelet":
			// this is a kc
			childCondition = opv1.MCCSyncGeneratedKubeletConfigs
		case "runtime":
			// this is a crc
			childCondition = opv1.MCCSyncContainerRuntimeConfig
		default:
			//
			childCondition = opv1.MCCSyncRenderedMachineConfigs
		}
		ParentCondition = metav1.Condition{
			Type:    string(opv1.MCCSync),
			Status:  metav1.ConditionTrue,
			Reason:  string(childCondition),
			Message: fmt.Sprintf("Controller has generated and is applying a new MachineConfig of type %s", splitMCName[len(splitMCName)-1]),
		}
		ChildCondition = metav1.Condition{
			Type:    string(childCondition),
			Status:  metav1.ConditionTrue,
			Reason:  fmt.Sprintf("%s%s", string(opv1.MCCSync), string(childCondition)),
			Message: fmt.Sprintf("Applying MachineConfig %s during Controller sync", mc.Name),
		}
		klog.Infof("Got an edit on mc: %s Not Implemeneted yet :)", mc.Name)
	} else if kc, err := ctrl.kcInformer.Lister().Get(objectToUse); err == nil {
		// sync'd either from addFinalizerToKubeletConfig
		// or from syncStatusOnly where the status is updated
		// or when annotations are added via .Update addAnnotations
		childCondition := opv1.MCCSyncGeneratedKubeletConfigs
		ParentCondition = metav1.Condition{
			Type:    string(opv1.MCCSync),
			Status:  metav1.ConditionTrue,
			Reason:  string(childCondition),
			Message: "Controller has generated and is applying a new KubeletConfig",
		}
		ChildCondition = metav1.Condition{
			Type:    string(childCondition),
			Status:  metav1.ConditionTrue,
			Reason:  fmt.Sprintf("%s%s", string(opv1.MCCSync), string(childCondition)),
			Message: fmt.Sprintf("Applying KubeletConfig %s during controller sync", kc.Name),
		}
		klog.Infof("Got an edit on kc: %s Not Implemeneted yet :)", kc.Name)
	} else if cm, err := ctrl.cmInformer.Lister().ConfigMaps(objAndNamespace[0]).Get(objectToUse); err == nil {
		// modified in a few different namespaces but the ones I am going to care about at first are:
		// merged-trusted-image-registry-ca
		// image-registry-ca
		childCondition := opv1.OperatorSyncConfigMaps
		ParentCondition = metav1.Condition{
			Type:    string(opv1.OperatorSync),
			Status:  metav1.ConditionTrue,
			Reason:  string(childCondition),
			Message: "Operator is editing a configmap during its sync phase",
		}
		ChildCondition = metav1.Condition{
			Type:    string(childCondition),
			Status:  metav1.ConditionTrue,
			Reason:  fmt.Sprintf("%s%s", string(opv1.OperatorSync), string(childCondition)),
			Message: fmt.Sprintf("Applying Configmap %s during operator sync", cm.Name),
		}
		klog.Infof("Got an edit on cm : %s Not Implemeneted yet :)", cm.Name)
	} else if mcp, err := ctrl.mcpInformer.Lister().Get(objectToUse); err == nil {
		// modified in the node controller when we sync status or change annotations
		// modified in osbuilder -- syncing status after failed build or successful one
		// modified in render controller -- after applying generated MC
		//
		var anno string
		var ok bool
		if anno, ok = mcp.Annotations["machineconfiguration.openshift.io/editor"]; !ok {
			klog.Infof("Did not get mcp anno on pool %s", mcp.Name)
			return mcfgArr
		}

		var parentCondition opv1.StateProgress
		var childCondition opv1.StateProgress
		switch anno {
		case "machine-config-operator":
			childCondition = opv1.OperatorSyncMCP
			parentCondition = opv1.OperatorSync
		case "machine-config-controller-render":
			parentCondition = opv1.MCCSync
			// need new one here
			childCondition = opv1.MCCSyncRenderedMachineConfigs
		case "machine-config-controller-node":
			parentCondition = opv1.MCCSync
			childCondition = opv1.MCCSyncMachineConfigPool
		default:
			return mcfgArr
		}
		ParentCondition = metav1.Condition{
			Type:    string(parentCondition),
			Status:  metav1.ConditionTrue,
			Reason:  string(childCondition),
			Message: fmt.Sprintf("MCP has been modified in the %s", anno),
		}
		ChildCondition = metav1.Condition{
			Type:    string(childCondition),
			Status:  metav1.ConditionTrue,
			Reason:  fmt.Sprintf("%s%s", string(parentCondition), string(childCondition)),
			Message: fmt.Sprintf("Applying MCP %s", mcp.Name),
		}
		klog.Infof("Got an edit on mcp: %s Not Implemeneted yet :)", mcp.Name)

	} else if crd, err := ctrl.crdInformer.Lister().Get(objectToUse); err == nil {
		// basically only updated (by us) in the operator's sync func: syncCustomResourceDefinitions

		if crd.Spec.Group != "machineconfiguration.openshift.io" {
			return mcfgArr
		}
		childCondition := opv1.OperatorSyncCustomResourceDefinitions
		ParentCondition = metav1.Condition{
			Type:    string(opv1.OperatorSync),
			Status:  metav1.ConditionTrue,
			Reason:  string(childCondition),
			Message: "CRD has been modified in the operator",
		}
		ChildCondition = metav1.Condition{
			Type:    string(childCondition),
			Status:  metav1.ConditionTrue,
			Reason:  fmt.Sprintf("%s%s", string(opv1.OperatorSync), string(childCondition)),
			Message: fmt.Sprintf("Applying CRD %s", crd.Name),
		}

		klog.Infof("Got an edit on crd: %s Not Implemeneted yet :)", crd.Name)

	} else if deployment, err := ctrl.deploymentInformer.Lister().Deployments("openshift-machine-config-operator").Get(objectToUse); err == nil {
		// our deplyoment
		// mcc deployment
		// mob deployment
		klog.Infof("Got an edit on deployment: %s Not Implemeneted yet :)", deployment.Name)
		return mcfgArr

	} else {
		return mcfgArr
	}

	newMcfg.Status.Controller.Conditions = GenerateMCfgCondition(newMcfg.Status.Controller, ChildCondition, ParentCondition, []opv1.StateProgress{
		opv1.MCCSync,
		opv1.MCCSyncContainerRuntimeConfig,
		opv1.MCCSyncControllerConfig,
		opv1.MCCSyncGeneratedKubeletConfigs,
		opv1.MCCSyncMachineConfigPool,
		opv1.MCCSyncRenderedMachineConfigs,
	})
	newMcfg.Status.Daemon.Conditions = GenerateMCfgCondition(newMcfg.Status.Daemon, ChildCondition, ParentCondition, []opv1.StateProgress{
		opv1.MCDSync,
		opv1.MCDSyncChangingStateAndReason,
		opv1.MCDSyncTriggeringUpdate,
	})
	newMcfg.Status.Operator.Conditions = GenerateMCfgCondition(newMcfg.Status.Operator, ChildCondition, ParentCondition, []opv1.StateProgress{
		opv1.OperatorSync,
		opv1.OperatorSyncConfigMaps,
		opv1.OperatorSyncCustomResourceDefinitions,
		opv1.OperatorSyncKubeletConfig,
		opv1.OperatorSyncMCC,
		opv1.OperatorSyncMCD,
		opv1.OperatorSyncMCP,
		opv1.OperatorSyncMCPRequired,
		opv1.OperatorSyncMCS,
		opv1.OperatorSyncRenderConfig,
	})
	// create all of the conditions, even the false ones
	mcfgArr = append(mcfgArr, *newMcfg)

	return mcfgArr
}

func GenerateMCfgCondition(Component opv1.MachineConfigurationComponent, newChildCondition metav1.Condition, newParentCondition metav1.Condition, allConditionTypes []opv1.StateProgress) []metav1.Condition {

	if Component.Conditions == nil {
		Component.Conditions = []metav1.Condition{}
		Component.Conditions = append(Component.Conditions, newParentCondition)
		Component.Conditions = append(Component.Conditions, newChildCondition)
		for _, condType := range allConditionTypes {
			found := false
			for _, cond := range Component.Conditions {
				// if this is one of our two conditions, do not nullify this
				if condType == opv1.StateProgress(cond.Type) {
					found = true
				}
			}
			// else if we do not have this one yet, set it to some sane default.
			if !found {
				Component.Conditions = append(Component.Conditions,
					metav1.Condition{
						Type:               string(condType),
						Message:            fmt.Sprintf("This component has not yet entered the %s phase", string(condType)),
						Reason:             "NotYetOccured",
						LastTransitionTime: metav1.Now(),
						Status:             metav1.ConditionFalse,
					})
			}
		}
		// else we already have some conditions. Lets update accordingly
	} else {
		foundChild := false
		foundParent := false
		// look through all of the conditions for our current ones, updat them accordingly
		// also set all other ones to false and update last transition time.
		for i, condition := range Component.Conditions {
			if condition.Type == newChildCondition.Type {
				foundChild = true
				newChildCondition.DeepCopyInto(&condition)
			} else if condition.Type == newParentCondition.Type {
				foundParent = true
				newParentCondition.DeepCopyInto(&condition)
			} else if condition.Status != metav1.ConditionFalse {
				condition.Status = metav1.ConditionFalse
				condition.LastTransitionTime = metav1.Now()
			}
			condition.DeepCopyInto(&Component.Conditions[i])
		}
		// I don't think this'll happen given the above logic, but if somehow we do not have an entry for this yet, add one.
		if !foundChild {
			Component.Conditions = append(Component.Conditions, newChildCondition)
		}
		if !foundParent {
			Component.Conditions = append(Component.Conditions, newParentCondition)
		}
	}
	return Component.Conditions
}
