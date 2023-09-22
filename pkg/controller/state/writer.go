package state

import (
	"context"
	"encoding/json"
	"fmt"

	v1 "github.com/openshift/api/machineconfiguration/v1"
	machineconfigurationv1 "github.com/openshift/client-go/machineconfiguration/applyconfigurations/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
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

	newState.Status.Health = v1.Healthy
	if state == v1.MachineStateErrored {
		newState.Status.MostRecentError = reason
		newState.Status.Health = v1.UnHealthy
	}

	c, _ := json.Marshal(newCondition)
	klog.Infof("new condition: %s", string(c))
	// update overall progression
	// remove a list item if too long, we don't want 100s of statuses

	if len(newState.Status.ProgressionHistory) > 20 {
		newState.Status.ProgressionHistory = newState.Status.ProgressionHistory[1:]
	}
	newState.Status.ProgressionHistory = append(newState.Status.ProgressionHistory, v1.ProgressionHistory{
		NameAndType: fmt.Sprintf("%s:%s", newCondition.Kind, newCondition.Name),
		State:       newCondition.State,
		Phase:       newCondition.Phase,
		Reason:      newCondition.Reason,
	}) //[node.Name] = append([]v1.ProgressionCondition{newCondition}, newState.Status.Progression[node.Name]...)

	//newState.Status.Progression[newStateProgress] = newCondition

	// update most recent state per node
	//	newState.Status.MostRecentState[objName] = newCondition

	if newState.Status.MostRecentState == nil {
		newState.Status.MostRecentState = []v1.ProgressionCondition{}
	}

	// supposedly this is supposed to only allow one of each key :shrug:
	alreadyExists := false
	for i, currentMostRecentState := range newState.Status.MostRecentState {
		if currentMostRecentState.Name == newCondition.Name {
			newState.Status.MostRecentState[i] = newCondition
			alreadyExists = true
			break
		}
	}
	if !alreadyExists {
		newState.Status.MostRecentState = append(newState.Status.MostRecentState, newCondition)
	}

	s, _ := json.Marshal(newState.Status)
	klog.Infof("Updating Machine State Controller Status to %s", string(s))

	currJson, _ := json.Marshal(currState)
	newJson, _ := json.Marshal(newState)

	//machineStateApplyConfig, err := machineconfigurationv1.ExtractMachineStateStatus(newState, "machine-config-operator")
	if err != nil {
		klog.Errorf("Could not extract machine state: %w", err)
		return err
	}

	cfgApplyConfig := machineconfigurationv1.MachineStateConfig().WithKind(newState.Kind).WithName(newState.Name).WithAPIVersion(newState.APIVersion).WithResourceVersion(newState.ResourceVersion).WithUID(newState.UID)
	//progressionConditionApplyConfig := machineconfigurationv1.ProgressionCondition().WithKind(newCondition.Kind).WithName(newCondition.Name).WithPhase(newCondition.Name).WithReason(newCondition.Reason).WithState(newCondition.State).WithTime(newCondition.Time)
	progressionHistoryApplyConfigs := []*machineconfigurationv1.ProgressionHistoryApplyConfiguration{}
	for _, s := range newState.Status.ProgressionHistory {
		progressionHistoryApplyConfigs = append(progressionHistoryApplyConfigs, machineconfigurationv1.ProgressionHistory().WithNameAndType(s.NameAndType).WithPhase(s.Phase).WithReason(s.Reason).WithState(s.State))
	}
	progressionApplyConfigs := []*machineconfigurationv1.ProgressionConditionApplyConfiguration{}
	for _, s := range newState.Status.MostRecentState {
		progressionApplyConfigs = append(progressionApplyConfigs, machineconfigurationv1.ProgressionCondition().WithKind(s.Kind).WithName(s.Name).WithPhase(s.Phase).WithReason(s.Reason).WithState(s.State).WithTime(s.Time))
	}
	statusApplyConfig := machineconfigurationv1.MachineStateStatus().WithConfig(cfgApplyConfig).WithHealth(newState.Status.Health).WithMostRecentError(newState.Status.MostRecentError).WithMostRecentState(progressionApplyConfigs...).WithProgressionHistory(progressionHistoryApplyConfigs...)
	specApplyConfig := machineconfigurationv1.MachineStateSpec().WithConfig(cfgApplyConfig).WithKind(newState.Spec.Kind)
	msApplyConfig := machineconfigurationv1.MachineState(newState.Name).WithStatus(statusApplyConfig).WithSpec(specApplyConfig)
	//ctrl.Clients.Mcfgclient.MachineconfigurationV1().MachineStates().Patch(context.TODO(), newState.Name, types.MergePatchType)

	applyConfig, _ := json.Marshal(msApplyConfig)
	klog.Infof("Updating Machine State Controller apply config Status to %s", string(applyConfig))
	ms, err := ctrl.Clients.Mcfgclient.MachineconfigurationV1().MachineStates().ApplyStatus(context.TODO(), msApplyConfig, metav1.ApplyOptions{FieldManager: "machine-config-operator", Force: true})

	/*
		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(currJson, newJson, currJson)
		if err != nil {
			return err
		}
		ms, err := ctrl.Clients.Mcfgclient.MachineconfigurationV1().MachineStates().Patch(context.TODO(), newState.Name, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	*/

	if err != nil {

		klog.Errorf("Could not update machine state. Original config %s \n New Config %s : %w", string(currJson), string(newJson), err)
		return err
	}
	m, err := json.Marshal(ms)
	klog.Infof("MACHINESTATE: %s", string(m))
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
