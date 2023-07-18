package state

import (
	"context"
	"fmt"
	"strings"

	v1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
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
		if out := syncFunc.fn(obj); out != nil {
			return out
		}
	}
	return nil
}

// namespace/event
// namespace/machine-state-name
func (ctrl *Controller) syncMCC(obj string) error {
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var err error

	objAndNamespace := strings.Split(obj, "/")
	if len(objAndNamespace) != 2 {
		klog.Infof("Obj: %s", obj)
		return fmt.Errorf("Object could not be split up, %s", obj)
	}
	msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
	if err != nil {
		eventToSync, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
		if err != nil {
			return err
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
	return nil
	/*
		watchchannel := watcher.ResultChan()
		var msToSync *v1.MachineState
		var err error
		if len(ms) > 0 {
			msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), ms, metav1.GetOptions{})
			if err != nil {
				return err
			}
		}
		for event := range watchchannel {
			obj, ok := event.Object.(*corev1.Event)
			if !ok {
				continue
			}
			//	dn.daemonHealthEvents.Event(dn.node, corev1.EventTypeNormal, "GotNode", "Getting node for MCD")
			msg := obj.Message
			reason := obj.Reason
			annos := obj.Annotations

			ctrl.WriteStatus(v1.ControllerState, msg, reason, annos)

			//there is
			// obj.Message
			// obj.Type (normal)
			// obj.Reason
			// obj.
			// if message is of a type?
			// we need to typify these events somehow
			// the mco currently seems to use the reason to do this
		}
		//node
		//	//getMCP
		//	//applyMCForPool
		//	//sync Ctrl/Pool status
		//render
		//	//getMCP
		//	//applyGenMCToPool
		//	//syncStatus
		//template
		//	//getCConfig
		//	//syncCerts
		//	//applyMCForCConfig
		//	//syncStatus
		//kubelet
		//	// getKubeletCfgs for pool
		//	// get MC assoc. with KubeletCfg
		//	// update annos of KubeletCfg, Update assoc MC.
		return ctrl.ReadSpec(msToSync)
	*/
}

func (ctrl *Controller) syncMCD(obj string) error {
	//syncNode
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var err error

	objAndNamespace := strings.Split(obj, "/")
	if len(objAndNamespace) != 2 {
		klog.Infof("Obj: %s", obj)
		return fmt.Errorf("Object could not be split up, %s", obj)
	}
	msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
	if err != nil {
		eventToSync, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
		if err != nil {
			return err
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
	/*
	   	if len(ms) > 0 {
	   		msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), ms, metav1.GetOptions{})
	   		if err != nil {
	   			return err
	   		}
	   	}

	   watchchannel := watcher.ResultChan()

	   	for event := range watchchannel {
	   		obj, ok := event.Object.(*corev1.Event)
	   		if !ok {
	   			continue
	   		}
	   		//	dn.daemonHealthEvents.Event(dn.node, corev1.EventTypeNormal, "GotNode", "Getting node for MCD")
	   		msg := obj.Message
	   		reason := obj.Reason
	   		annos := obj.Annotations

	   		ctrl.WriteStatus(v1.DaemonState, msg, reason, annos)

	   }
	*/
}

func (ctrl *Controller) syncMCS(obj string) error {

	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var err error

	objAndNamespace := strings.Split(obj, "/")
	if len(objAndNamespace) != 2 {
		klog.Infof("Obj: %s", obj)
		return fmt.Errorf("Object could not be split up, %s", obj)
	}
	msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
	if err != nil {
		eventToSync, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
		if err != nil {
			return err
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
	/*
		watchchannel := watcher.ResultChan()
		var msToSync *v1.MachineState
		var err error
		if len(ms) > 0 {
			msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), ms, metav1.GetOptions{})
			if err != nil {
				return err
			}
		}
		for event := range watchchannel {
			obj, ok := event.Object.(*corev1.Event)
			if !ok {
				continue
			}
			//	dn.daemonHealthEvents.Event(dn.node, corev1.EventTypeNormal, "GotNode", "Getting node for MCD")
			msg := obj.Message
			reason := obj.Reason
			annos := obj.Annotations

			ctrl.WriteStatus(v1.ServerState, msg, reason, annos)

		}
		return ctrl.ReadSpec(msToSync)
	*/
}

func (ctrl *Controller) syncMetrics(obj string) error {
	// get requests
	// update metrics requested, or register, or degregister
	/*
		watchchannel := watcher.ResultChan()
		var msToSync *v1.MachineState
		var err error
		if len(ms) > 0 {
			msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), ms, metav1.GetOptions{})
			if err != nil {
				return err
			}
		}
		for event := range watchchannel {
			obj, ok := event.Object.(*corev1.Event)
			if !ok {
				continue
			}
			//	dn.daemonHealthEvents.Event(dn.node, corev1.EventTypeNormal, "GotNode", "Getting node for MCD")
			msg := obj.Message
			reason := obj.Reason
			annos := obj.Annotations

			ctrl.WriteStatus(v1.UpdatingMetrics, msg, reason, annos)

			// msg == DeregisterMetric
			// reason == MCC_State

		}
		return ctrl.ReadSpec(msToSync)
	*/
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var err error

	objAndNamespace := strings.Split(obj, "/")
	if len(objAndNamespace) != 2 {
		klog.Infof("Obj: %s", obj)
		return fmt.Errorf("Object could not be split up, %s", obj)
	}
	msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
	if err != nil {
		eventToSync, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
		if err != nil {
			return err
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

func (ctrl *Controller) syncUpgradingProgression(obj string) error {
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var err error

	objAndNamespace := strings.Split(obj, "/")
	if len(objAndNamespace) != 2 {
		klog.Infof("Obj: %s", obj)
		return fmt.Errorf("Object could not be split up, %s", obj)
	}
	msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
	if err != nil {
		eventToSync, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
		if err != nil {
			return err
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
	/*
		watchchannel := watcher.ResultChan()
		var msToSync *v1.MachineState
		var err error
		// API item of our kind to sync
		if len(ms) > 0 {
			msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), ms, metav1.GetOptions{})
			if err != nil {
				return err
			}
		}
		for event := range watchchannel {
			obj, ok := event.Object.(*corev1.Event)
			if !ok {
				continue
			}
			//	dn.daemonHealthEvents.Event(dn.node, corev1.EventTypeNormal, "GotNode", "Getting node for MCD")
			msg := obj.Message
			reason := obj.Reason
			annos := obj.Annotations

			ctrl.WriteStatus(v1.UpgradeProgression, msg, reason, annos)

		}
		return ctrl.ReadSpec(msToSync)
	*/
}

func (ctrl *Controller) syncOperatorProgression(obj string) error {
	var msToSync *v1.MachineState
	var eventToSync *corev1.Event
	var err error

	objAndNamespace := strings.Split(obj, "/")
	if len(objAndNamespace) != 2 {
		klog.Infof("Obj: %s", obj)
		return fmt.Errorf("Object could not be split up, %s", obj)
	}
	msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
	if err != nil {
		eventToSync, err = ctrl.Kubeclient.CoreV1().Events("openshift-machine-config-operator").Get(context.TODO(), objAndNamespace[1], metav1.GetOptions{})
		if err != nil {
			return err
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
	/*
		watchchannel := watcher.ResultChan()
		var msToSync *v1.MachineState
		var err error
		// API item of our kind to sync
		if len(ms) > 0 {
			msToSync, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), ms, metav1.GetOptions{})
			if err != nil {
				return err
			}
		}
		for event := range watchchannel {
			obj, ok := event.Object.(*corev1.Event)
			if !ok {
				continue
			}
			//	dn.daemonHealthEvents.Event(dn.node, corev1.EventTypeNormal, "GotNode", "Getting node for MCD")
			msg := obj.Message
			reason := obj.Reason
			annos := obj.Annotations

			ctrl.WriteStatus(v1.OperatorProgression, msg, reason, annos)

		}
		return ctrl.ReadSpec(msToSync)
	*/
}
