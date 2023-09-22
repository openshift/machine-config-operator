package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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

			object, err = ctrl.Kubeclient.CoreV1().Nodes().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
			if err == nil {
				return syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().KubeletConfigs().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().ControllerConfigs().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}
			object, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
			if err == nil {
				syncFunc.fn(object)
			}

			return fmt.Errorf("Object %s not found", objAndNamespace[1])
			// find out which type of obj we are
			/*
				object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
				if err != nil {
					object, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
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
	var msToSync *v1.MachineConfigState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineConfigState); !ok {
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
	var msToSync *v1.MachineConfigState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineConfigState); !ok {
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
	var msToSync *v1.MachineConfigState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineConfigState); !ok {
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
	var msToSync *v1.MachineConfigState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineConfigState); !ok {
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
	var msToSync *v1.MachineConfigState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineConfigState); !ok {
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
	var msToSync *v1.MachineConfigState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineConfigState); !ok {
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

func (ctrl *Controller) GenerateMachineConfigStates(object interface{}) []*v1.MachineConfigState {
	var msArr []*v1.MachineConfigState
	var state v1.StateProgress
	var phase string
	var reason string
	//newStatus :=
	klog.Infof("OBJ: %s", object.(string))
	if nodeToSync, err := ctrl.nodeInformer.Lister().Get(object.(string)); err == nil {
		klog.Infof("GOT NODE %s", nodeToSync.Name)
		pool := "worker"
		if _, ok := nodeToSync.Labels["node-role.kubernetes.io/master"]; ok {
			pool = "master"
		}
		whichMS := fmt.Sprintf("upgrade-%s", pool)
		ms, _ := ctrl.Mcfgclient.MachineconfigurationV1().MachineConfigStates().Get(context.TODO(), whichMS, metav1.GetOptions{})
		newMS := ms.DeepCopy()
		// if the config annotations differ, we are upgrading. else we are not.
		if nodeToSync.Annotations[constants.DesiredMachineConfigAnnotationKey] != nodeToSync.Annotations[constants.CurrentMachineConfigAnnotationKey] {
			switch nodeToSync.Annotations[constants.MachineConfigDaemonStateAnnotationKey] {
			case constants.MachineConfigDaemonStateWorkPerparing:
				state = v1.MachineConfigPoolUpdatePreparing
				phase = "NodeUpgradePreparing"
				reason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
			case constants.MachineConfigDaemonStateWorking:
				if nodeToSync.Annotations[constants.LastAppliedDrainerAnnotationKey] != nodeToSync.Annotations[constants.DesiredDrainerAnnotationKey] {
					desiredVerb := strings.Split(nodeToSync.Annotations[constants.DesiredDrainerAnnotationKey], "-")[0]
					switch desiredVerb {
					// this might not be right. We seem to drain/uncordon more than I assumed
					case constants.DrainerStateDrain:
						// we are draining
						state = v1.MachineConfigPoolUpdateInProgress
						phase = fmt.Sprintf("Draining node. Desired Drainer is %s", nodeToSync.Annotations[constants.DesiredDrainerAnnotationKey])
						reason = "NodeDraining"
					case constants.DrainerStateUncordon:
						// we are uncordoning, this only happens (I think) in completeUpdate. If not, we need another state for this
						state = v1.MachineConfigPoolUpdateCompleting
						phase = fmt.Sprintf("Cordining/Uncordoning node. Desired Drainer is %s", nodeToSync.Annotations[constants.DesiredDrainerAnnotationKey])
						reason = "NodeUncordoning"
					}
					// we are draining
					break
				}
				state = v1.MachineConfigPoolUpdateInProgress
				phase = nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey]
				reason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
			case constants.MachineConfigDaemonStateWorkPostAction:
				// updatePostAction
				state = v1.MachineConfigPoolUpdatePostAction
				phase = nodeToSync.Annotations[constants.MachineConfigDaemonPhaseAnnotationKey]
				reason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
			case constants.MachineConfigDaemonResuming:
				state = v1.MachineConfigPoolResuming
				phase = "MachineConfigDaemonResuming"
				reason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
			case constants.MachineConfigDaemonStateDegraded:
				state = v1.MachineConfigStateErrored
				reason = nodeToSync.Annotations[constants.MachineConfigDaemonReasonAnnotationKey]
				phase = "UpgradeErrored"

			}
		} else {
			// else they are equal... but we need to figure out: were we just updating though?
			wasUpdating := false
			existsAlready := false
			for _, s := range ms.Status.MostRecentState {
				if s.Name == nodeToSync.Name {
					existsAlready = true
					if s.State == v1.MachineConfigPoolUpdateCompleting || s.State == v1.MachineConfigPoolUpdatePreparing || s.State == v1.MachineConfigPoolUpdateInProgress || s.State == v1.MachineConfigPoolUpdatePostAction {
						wasUpdating = true
					}
				}
			}
			// if the MCO was updating, but no longer has mismatched configs, that means the update is complete
			if wasUpdating {
				state = v1.MachineConfigPoolUpdateComplete
				reason = "NodeUpgradeComplete"
				phase = fmt.Sprintf("Node has completed update to config %s", nodeToSync.Annotations[constants.CurrentMachineConfigAnnotationKey])
			} else if !existsAlready {
				// else if none of this is true BUT this item does not exist in the list, we need to add Ready.
				state = v1.MachineConfigPoolReady
				reason = "NodeReady"
				phase = fmt.Sprintf("Node is ready and in config %s", nodeToSync.Annotations[constants.CurrentMachineConfigAnnotationKey])
			} else {
				// else we are already Ready. No need to update
				return nil
			}
		}
		newCondition := v1.ProgressionCondition{
			Name:   nodeToSync.Name,
			State:  state,
			Phase:  phase,
			Reason: reason,
			Time:   metav1.Now(),
		}

		newMS.Status.Health = v1.Healthy
		if state == v1.MachineConfigStateErrored {
			newMS.Status.MostRecentError = reason
			newMS.Status.Health = v1.UnHealthy
		}

		c, _ := json.Marshal(newCondition)
		klog.Infof("new condition: %s", string(c))
		// update overall progression
		// remove a list item if too long, we don't want 100s of statuses

		if len(newMS.Status.ProgressionHistory) > 20 {
			newMS.Status.ProgressionHistory = newMS.Status.ProgressionHistory[1:]
		}

		prog := v1.ProgressionHistory{
			NameAndState: fmt.Sprintf("%s:%s", newCondition.Name, newCondition.State),
			Phase:        newCondition.Phase,
			Reason:       newCondition.Reason,
		}

		if len(newMS.Status.ProgressionHistory) == 0 || !equality.Semantic.DeepEqual(prog, newMS.Status.ProgressionHistory[len(newMS.Status.ProgressionHistory)-1]) {
			newMS.Status.ProgressionHistory = append(newMS.Status.ProgressionHistory, prog) //[node.Name] = append([]v1.ProgressionCondition{newCondition}, newState.Status.Progression[node.Name]...)
		}
		//newState.Status.Progression[newStateProgress] = newCondition

		// update most recent state per node
		//	newState.Status.MostRecentState[objName] = newCondition

		if newMS.Status.MostRecentState == nil {
			newMS.Status.MostRecentState = []v1.ProgressionCondition{}
		}

		// supposedly this is supposed to only allow one of each key :shrug:
		alreadyExists := false
		for i, currentMostRecentState := range newMS.Status.MostRecentState {
			if currentMostRecentState.Name == newCondition.Name {
				newMS.Status.MostRecentState[i] = newCondition
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			newMS.Status.MostRecentState = append(newMS.Status.MostRecentState, newCondition)
		}
		msArr = append(msArr, newMS)

	}
	return msArr
}
