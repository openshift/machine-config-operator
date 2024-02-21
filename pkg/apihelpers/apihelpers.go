package apihelpers

// TODO(jkyros): This is here in its own package because it was with the API, but when we migrated the API, it couldn't go with
// it because it was only used in the MCO. I wanted to stuff it in common, but because of how our test suite is set up, that would
// have caused a dependency cycle, so now it's here by itself.

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewMachineConfigPoolCondition creates a new MachineConfigPool condition.
func NewMachineConfigPoolCondition(condType mcfgv1.MachineConfigPoolConditionType, status corev1.ConditionStatus, reason, message string) *mcfgv1.MachineConfigPoolCondition {
	return &mcfgv1.MachineConfigPoolCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// NewMachineConfigPoolCondition creates a new MachineConfigPool condition.
func NewMachineOSBuildCondition(condType string, status metav1.ConditionStatus, reason, message string) *metav1.Condition {
	return &metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// GetMachineConfigPoolCondition returns the condition with the provided type.
func GetMachineConfigPoolCondition(status mcfgv1.MachineConfigPoolStatus, condType mcfgv1.MachineConfigPoolConditionType) *mcfgv1.MachineConfigPoolCondition {
	// in case of sync errors, return the last condition that matches, not the first
	// this exists for redundancy and potential race conditions.
	var LatestState *mcfgv1.MachineConfigPoolCondition
	for i := range status.Conditions {
		c := status.Conditions[i]
		if c.Type == condType {
			LatestState = &c
		}
	}
	return LatestState
}

// SetMachineConfigPoolCondition updates the MachineConfigPool to include the provided condition. If the condition that
// we are about to add already exists and has the same status and reason then we are not going to update.
func SetMachineConfigPoolCondition(status *mcfgv1.MachineConfigPoolStatus, condition mcfgv1.MachineConfigPoolCondition) {
	currentCond := GetMachineConfigPoolCondition(*status, condition.Type)
	if currentCond != nil && currentCond.Status == condition.Status && currentCond.Reason == condition.Reason && currentCond.Message == condition.Message {
		return
	}
	// Do not update lastTransitionTime if the status of the condition doesn't change.
	if currentCond != nil && currentCond.Status == condition.Status {
		condition.LastTransitionTime = currentCond.LastTransitionTime
	}
	newConditions := filterOutMachineConfigPoolCondition(status.Conditions, condition.Type)
	status.Conditions = append(newConditions, condition)
}

// SetMachineConfigPoolCondition updates the MachineConfigPool to include the provided condition. If the condition that
// we are about to add already exists and has the same status and reason then we are not going to update.
func SetMachineOSBuildCondition(status *mcfgv1alpha1.MachineOSBuildStatus, condition metav1.Condition) {
	currentCond := GetMachineOSBuildCondition(*status, mcfgv1alpha1.BuildProgress(condition.Type))
	if currentCond != nil && currentCond.Status == condition.Status && currentCond.Reason == condition.Reason && currentCond.Message == condition.Message {
		return
	}
	// Do not update lastTransitionTime if the status of the condition doesn't change.
	if currentCond != nil && currentCond.Status == condition.Status {
		condition.LastTransitionTime = currentCond.LastTransitionTime
	}

	// this may not be necessary
	newConditions := filterOutMachineOSBuildCondition(status.Conditions, condition.Type)
	status.Conditions = append(newConditions, condition)
}

// RemoveMachineConfigPoolCondition removes the MachineConfigPool condition with the provided type.
func RemoveMachineConfigPoolCondition(status *mcfgv1.MachineConfigPoolStatus, condType mcfgv1.MachineConfigPoolConditionType) {
	status.Conditions = filterOutMachineConfigPoolCondition(status.Conditions, condType)
}

// filterOutCondition returns a new slice of MachineConfigPool conditions without conditions with the provided type.
func filterOutMachineConfigPoolCondition(conditions []mcfgv1.MachineConfigPoolCondition, condType mcfgv1.MachineConfigPoolConditionType) []mcfgv1.MachineConfigPoolCondition {
	var newConditions []mcfgv1.MachineConfigPoolCondition
	for _, c := range conditions {
		if c.Type == condType {
			continue
		}
		newConditions = append(newConditions, c)
	}
	return newConditions
}

// filterOutCondition returns a new slice of MachineConfigPool conditions without conditions with the provided type.
func filterOutMachineOSBuildCondition(conditions []metav1.Condition, condType string) []metav1.Condition {
	var newConditions []metav1.Condition
	for _, c := range conditions {
		if c.Type == condType {
			continue
		}
		newConditions = append(newConditions, c)
	}
	return newConditions
}

func IsMachineOSBuildConditionTrue(conditions []metav1.Condition, conditionType mcfgv1alpha1.BuildProgress) bool {
	return IsMachineOSBuildConditionPresentAndEqual(conditions, conditionType, metav1.ConditionTrue)
}

// IsMachineOSBuildConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsMachineOSBuildConditionPresentAndEqual(conditions []metav1.Condition, conditionType mcfgv1alpha1.BuildProgress, status metav1.ConditionStatus) bool {
	for _, condition := range conditions {
		if mcfgv1alpha1.BuildProgress(condition.Type) == conditionType {
			return condition.Status == status
		}
	}
	return false
}

func GetMachineOSBuildCondition(status mcfgv1alpha1.MachineOSBuildStatus, condType mcfgv1alpha1.BuildProgress) *metav1.Condition {
	// in case of sync errors, return the last condition that matches, not the first
	// this exists for redundancy and potential race conditions.
	var LatestState *metav1.Condition
	for i := range status.Conditions {
		c := status.Conditions[i]
		if mcfgv1alpha1.BuildProgress(c.Type) == condType {
			LatestState = &c
		}
	}
	return LatestState
}

// IsMachineConfigPoolConditionTrue returns true when the conditionType is present and set to `ConditionTrue`
func IsMachineConfigPoolConditionTrue(conditions []mcfgv1.MachineConfigPoolCondition, conditionType mcfgv1.MachineConfigPoolConditionType) bool {
	return IsMachineConfigPoolConditionPresentAndEqual(conditions, conditionType, corev1.ConditionTrue)
}

// IsMachineConfigPoolConditionFalse returns true when the conditionType is present and set to `ConditionFalse`
func IsMachineConfigPoolConditionFalse(conditions []mcfgv1.MachineConfigPoolCondition, conditionType mcfgv1.MachineConfigPoolConditionType) bool {
	return IsMachineConfigPoolConditionPresentAndEqual(conditions, conditionType, corev1.ConditionFalse)
}

// IsMachineConfigPoolConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsMachineConfigPoolConditionPresentAndEqual(conditions []mcfgv1.MachineConfigPoolCondition, conditionType mcfgv1.MachineConfigPoolConditionType, status corev1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// NewKubeletConfigCondition returns an instance of a KubeletConfigCondition
func NewKubeletConfigCondition(condType mcfgv1.KubeletConfigStatusConditionType, status corev1.ConditionStatus, message string) *mcfgv1.KubeletConfigCondition {
	return &mcfgv1.KubeletConfigCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Message:            message,
	}
}

func NewCondition(condType string, status metav1.ConditionStatus, reason, message string) *metav1.Condition {
	return &metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// NewContainerRuntimeConfigCondition returns an instance of a ContainerRuntimeConfigCondition
func NewContainerRuntimeConfigCondition(condType mcfgv1.ContainerRuntimeConfigStatusConditionType, status corev1.ConditionStatus, message string) *mcfgv1.ContainerRuntimeConfigCondition {
	return &mcfgv1.ContainerRuntimeConfigCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Message:            message,
	}
}

// NewControllerConfigStatusCondition creates a new ControllerConfigStatus condition.
func NewControllerConfigStatusCondition(condType mcfgv1.ControllerConfigStatusConditionType, status corev1.ConditionStatus, reason, message string) *mcfgv1.ControllerConfigStatusCondition {
	return &mcfgv1.ControllerConfigStatusCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// GetControllerConfigStatusCondition returns the condition with the provided type.
func GetControllerConfigStatusCondition(status mcfgv1.ControllerConfigStatus, condType mcfgv1.ControllerConfigStatusConditionType) *mcfgv1.ControllerConfigStatusCondition {
	for i := range status.Conditions {
		c := status.Conditions[i]
		if c.Type == condType {
			return &c
		}
	}
	return nil
}

// SetControllerConfigStatusCondition updates the ControllerConfigStatus to include the provided condition. If the condition that
// we are about to add already exists and has the same status and reason then we are not going to update.
func SetControllerConfigStatusCondition(status *mcfgv1.ControllerConfigStatus, condition mcfgv1.ControllerConfigStatusCondition) {
	currentCond := GetControllerConfigStatusCondition(*status, condition.Type)
	if currentCond != nil && currentCond.Status == condition.Status && currentCond.Reason == condition.Reason {
		return
	}
	// Do not update lastTransitionTime if the status of the condition doesn't change.
	if currentCond != nil && currentCond.Status == condition.Status {
		condition.LastTransitionTime = currentCond.LastTransitionTime
	}
	newConditions := filterOutControllerConfigStatusCondition(status.Conditions, condition.Type)
	status.Conditions = append(newConditions, condition)
}

// RemoveControllerConfigStatusCondition removes the ControllerConfigStatus condition with the provided type.
func RemoveControllerConfigStatusCondition(status *mcfgv1.ControllerConfigStatus, condType mcfgv1.ControllerConfigStatusConditionType) {
	status.Conditions = filterOutControllerConfigStatusCondition(status.Conditions, condType)
}

// filterOutCondition returns a new slice of ControllerConfigStatus conditions without conditions with the provided type.
func filterOutControllerConfigStatusCondition(conditions []mcfgv1.ControllerConfigStatusCondition, condType mcfgv1.ControllerConfigStatusConditionType) []mcfgv1.ControllerConfigStatusCondition {
	var newConditions []mcfgv1.ControllerConfigStatusCondition
	for _, c := range conditions {
		if c.Type == condType {
			continue
		}
		newConditions = append(newConditions, c)
	}
	return newConditions
}

// IsControllerConfigStatusConditionTrue returns true when the conditionType is present and set to `ConditionTrue`
func IsControllerConfigStatusConditionTrue(conditions []mcfgv1.ControllerConfigStatusCondition, conditionType mcfgv1.ControllerConfigStatusConditionType) bool {
	return IsControllerConfigStatusConditionPresentAndEqual(conditions, conditionType, corev1.ConditionTrue)
}

// IsControllerConfigStatusConditionFalse returns true when the conditionType is present and set to `ConditionFalse`
func IsControllerConfigStatusConditionFalse(conditions []mcfgv1.ControllerConfigStatusCondition, conditionType mcfgv1.ControllerConfigStatusConditionType) bool {
	return IsControllerConfigStatusConditionPresentAndEqual(conditions, conditionType, corev1.ConditionFalse)
}

// IsControllerConfigStatusConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsControllerConfigStatusConditionPresentAndEqual(conditions []mcfgv1.ControllerConfigStatusCondition, conditionType mcfgv1.ControllerConfigStatusConditionType, status corev1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// IsControllerConfigCompleted checks whether a ControllerConfig is completed by the Template Controller
func IsControllerConfigCompleted(ccName string, ccGetter func(string) (*mcfgv1.ControllerConfig, error)) error {
	cur, err := ccGetter(ccName)
	if err != nil {
		return err
	}

	if cur.Generation != cur.Status.ObservedGeneration {
		return fmt.Errorf("status for ControllerConfig %s is being reported for %d, expecting it for %d", ccName, cur.Status.ObservedGeneration, cur.Generation)
	}

	completed := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, mcfgv1.TemplateControllerCompleted)
	running := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, mcfgv1.TemplateControllerRunning)
	failing := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, mcfgv1.TemplateControllerFailing)
	if completed &&
		!running &&
		!failing {
		return nil
	}
	return fmt.Errorf("ControllerConfig has not completed: completed(%v) running(%v) failing(%v)", completed, running, failing)
}
