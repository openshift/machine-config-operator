package state

import (
	"context"
	"fmt"
	"strings"

	v1 "github.com/openshift/api/machineconfiguration/v1"
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
	for _, syncFunc := range syncFuncs {
		objAndNamespace := strings.Split(obj, "/")
		if len(objAndNamespace) != 2 {
			klog.Infof("Obj: %s", obj)
			return fmt.Errorf("Object could not be split up, %s", obj)
		}
		var object interface{}
		var err error
		object, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
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
	return nil
}

// namespace/event
// namespace/machine-state-name
func (ctrl *Controller) syncMCC(obj interface{}) error {
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineState); !ok {
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
			return ctrl.WriteStatus("mcc-health", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	klog.Infof("no event component or MS name found.")
	return nil
}

func (ctrl *Controller) syncMCD(obj interface{}) error {
	//syncNode
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineState); !ok {
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
			return ctrl.WriteStatus("mcd-health", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	return nil
}

func (ctrl *Controller) syncMCS(obj interface{}) error {
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineState); !ok {
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}

	// we either have an event or a machineState
	if msToSync != nil && msToSync.Name == "mcs-health" {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "mcs-health" {
			return ctrl.WriteStatus("mcs-health", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	return nil
}

func (ctrl *Controller) syncMetrics(obj interface{}) error {
	// get requests
	// update metrics requested, or register, or degregister
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineState); !ok {
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}
	// we either have an event or a machineState
	if msToSync != nil && msToSync.Name == "metrics" {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "metrics" {
			return ctrl.WriteStatus("metrics", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	return nil

}

func (ctrl *Controller) syncUpgradingProgression(obj interface{}) error {
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineState); !ok {
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
					return ctrl.WriteStatus("upgrade-worker", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
				} else if eventToSync.Annotations["Pool"] == "master" {
					return ctrl.WriteStatus("upgrade-master", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
				}
			}
		}
	}
	return nil
}

func (ctrl *Controller) syncOperatorProgression(obj interface{}) error {
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var ok bool

	if msToSync, ok = obj.(*v1.MachineState); !ok {
		if eventToSync, ok = obj.(*corev1.Event); !ok {
			return fmt.Errorf("Could not get event or MS from object: %s", obj.(string))
		}
	}

	// we either have an event or a machineState
	if msToSync != nil && msToSync.Name == "operator-health" {
		return ctrl.ReadSpec(msToSync)
	} else if eventToSync != nil {
		if eventToSync.Source.Component == "operator-health" {
			return ctrl.WriteStatus("operator-health", eventToSync.Message, eventToSync.Reason, eventToSync.Annotations)
		}
	}
	return nil

}
