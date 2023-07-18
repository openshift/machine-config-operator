package state

import (
	"context"
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	v1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

func (ctrl *Controller) WriteStatus(kind string, status, reason string, annos map[string]string) error {
	// write status and reason to a struct of some sort

	// get machine state with name. example "mcc-health"
	currState, err := ctrl.Clients.Mcfgclient.MachineconfigurationV1().MachineStates().Get(context.TODO(), kind, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Could not get machinestate %s: %w", kind, err)
		return err
	}

	newState := currState.DeepCopy()

	// the state we are in is placed in the annotations, convert it from a string
	state := v1.StateProgress(annos["state"])

	// pull out the object kind and name from the annotations as well
	var objKind, objName string
	if val, ok := annos["ObjectKind"]; ok {
		objKind = val
	}
	if val, ok := annos["ObjectName"]; ok {
		objName = val
	}
	newCondition := v1.ProgressionCondition{
		Kind:   v1.OperatorObject(objKind),
		Name:   objName,
		State:  state,
		Phase:  status,
		Reason: reason,
		Time:   metav1.Now(),
	}

	newState.Status.Healthy = true
	if state == v1.MachineConfigPoolUpdateErrored {
		newState.Status.MostRecentError = reason
		newState.Status.Healthy = false
	}

	c, _ := json.Marshal(newCondition)
	klog.Infof("new condition: %s", string(c))
	// update overall progression
	// remove a list item if too long, we don't want 100s of statuses
	if len(newState.Status.Progression) > 20 {
		newState.Status.Progression = newState.Status.Progression[1:]
	}
	newState.Status.Progression = append(newState.Status.Progression, newCondition) //[node.Name] = append([]v1.ProgressionCondition{newCondition}, newState.Status.Progression[node.Name]...)
	//newState.Status.Progression[newStateProgress] = newCondition

	// update most recent state per node
	//	newState.Status.MostRecentState[objName] = newCondition

	if newState.Status.MostRecentState == nil {
		newState.Status.MostRecentState = make(map[string]v1.ProgressionCondition)
	}
	newState.Status.MostRecentState[objName] = newCondition

	s, _ := json.Marshal(newState.Status)
	klog.Infof("Updating Machine State Controller Status to %s", string(s))
	_, err = ctrl.Clients.Mcfgclient.MachineconfigurationV1().MachineStates().UpdateStatus(context.TODO(), newState, metav1.UpdateOptions{})
	if err != nil {
		currJson, _ := json.Marshal(currState)
		newJson, _ := json.Marshal(newState)
		klog.Errorf("Could not update machine state. Original config %s \n New Config %s : %w", string(currJson), string(newJson), err)
		return err
	}
	return nil
}

// this is where metric application will live primarily. and also any options applied to other components
func (ctrl *Controller) ReadSpec(ms *v1.MachineState) error {
	var err error
	if ms != nil {
		/*	if ms.Kind == string(v1.UpdatingMetrics) {
				for _, metricToIgnore := range ms.Spec.Config.Ignore {
					klog.Infof("De-registering metric %s", metricToIgnore)
				}
				// somehow also apply the collection frequency
			}
		*/ //_, err = ctrl.Mcfgclient.MachineconfigurationV1().MachineStates().Update(context.TODO(), ms, metav1.UpdateOptions{})

	}
	if err != nil {
		klog.Errorf("Could not update MachineState: %s ... %w", ms.Name, err)
	}
	return err
}
