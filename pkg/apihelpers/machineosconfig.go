package apihelpers

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewMachineOSConfigCondition creates a new MachineOSConfig condition.
func NewMachineOSConfigCondition(condType string, status metav1.ConditionStatus, reason, message string) *metav1.Condition {
	return &metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

func GetMachineOSConfigCondition(status mcfgv1.MachineOSConfigStatus, condType string) *metav1.Condition {
	// in case of sync errors, return the last condition that matches, not the first
	// this exists for redundancy and potential race conditions.
	var LatestState *metav1.Condition
	for i := range status.Conditions {
		c := status.Conditions[i]
		if c.Type == condType {
			LatestState = &c
		}
	}
	return LatestState
}

// SetMachineOSConfigCondition updates the MachineOSConfig to include the provided condition. If the condition that
// we are about to add already exists and has the same status and reason then we are not going to update.
func SetMachineOSConfigCondition(status *mcfgv1.MachineOSConfigStatus, condition metav1.Condition) {
	currentCond := GetMachineOSConfigCondition(*status, condition.Type)
	if currentCond != nil && currentCond.Status == condition.Status && currentCond.Reason == condition.Reason && currentCond.Message == condition.Message {
		return
	}
	// Do not update lastTransitionTime if the status of the condition doesn't change.
	if currentCond != nil && currentCond.Status == condition.Status {
		condition.LastTransitionTime = currentCond.LastTransitionTime
	}

	// this may not be necessary
	newConditions := filterOutMachineOSConfigCondition(status.Conditions, condition.Type)
	newConditions = append(newConditions, condition)
	status.Conditions = newConditions
}

// RemoveMachineOSConfigCondition removes the MachineOSConfig condition with the provided type.
func RemoveMachineOSConfigCondition(status *mcfgv1.MachineOSConfigStatus, condType string) {
	status.Conditions = filterOutMachineOSConfigCondition(status.Conditions, condType)
}

// filterOutMachineOSConfigCondition returns a new slice of MachineOSConfig conditions without conditions with the provided type.
func filterOutMachineOSConfigCondition(conditions []metav1.Condition, condType string) []metav1.Condition {
	var newConditions []metav1.Condition
	for _, c := range conditions {
		if c.Type == condType {
			continue
		}
		newConditions = append(newConditions, c)
	}
	return newConditions
}

func IsMachineOSConfigConditionTrue(conditions []metav1.Condition, conditionType string) bool {
	return IsMachineOSConfigConditionPresentAndEqual(conditions, conditionType, metav1.ConditionTrue)
}

// IsMachineOSConfigConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsMachineOSConfigConditionPresentAndEqual(conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}
