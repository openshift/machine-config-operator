package v1

import (
	"fmt"
	"sort"

	ign "github.com/coreos/ignition/config/v2_2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// simplifyLabels takes a slice of MachineConfigLabels and returns a slice. The input
// slice may contain an arbitrary number of labels and may have multiple exist and notexist
// entries. The output slice will container 0, 1, or 2 entries and no more than a single
// exists label set and a single notexist label set.
func simplifyLabels(labels []MachineConfigLabels) []MachineConfigLabels {
	exist := map[string]string{}
	notexist := map[string]string{}

	for _, mcl := range labels {
		for key, value := range mcl.Labels {
			if mcl.Exist {
				exist[key] = value
				if _, ok := notexist[key]; ok {
					delete(notexist, key)
				}
			} else {
				notexist[key] = value
				if _, ok := exist[key]; ok {
					delete(exist, key)
				}
			}
		}
	}
	out := []MachineConfigLabels{}
	if len(exist) > 0 {
		out = append(out, MachineConfigLabels{
			Exist:  true,
			Labels: exist,
		})
	}
	if len(notexist) > 0 {
		out = append(out, MachineConfigLabels{
			Exist:  false,
			Labels: notexist,
		})
	}
	return out
}

func mergeLabels(l1 []MachineConfigLabels, l2 []MachineConfigLabels) []MachineConfigLabels {
	out := append(l1, l2...)
	return simplifyLabels(out)
}

func removeTaint(mcts []MachineConfigTaint, taint MachineConfigTaint) []MachineConfigTaint {
	for i, mct := range mcts {
		if mct.Taint.Key == taint.Taint.Key {
			return append(mcts[:i], mcts[i+1:]...)
		}
	}
	return mcts
}

// Create a new list of taints, one by one, removing any previous taints with the
// same key.
func simplifyTaints(mcts []MachineConfigTaint) []MachineConfigTaint {
	out := []MachineConfigTaint{}
	for i, _ := range mcts {
		taint := &mcts[i]
		out = removeTaint(out, *taint)
		out = append(out, *taint)
	}
	return out
}

func mergeTaints(t1 []MachineConfigTaint, t2 []MachineConfigTaint) []MachineConfigTaint {
	out := append(t1, t2...)
	return simplifyTaints(out)
}

// MergeMachineConfigs combines multiple machineconfig objects into one object.
// It sorts all the configs in increasing order of their name.
// It uses the Ignition config from first object as base and appends all the rest.
// Kernel arguments are concatenated.
// It uses only the OSImageURL provided by the CVO and ignores any MC provided OSImageURL.
func MergeMachineConfigs(configs []*MachineConfig, osImageURL string) *MachineConfig {
	if len(configs) == 0 {
		return nil
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	outIgn := configs[0].Spec.Config
	for idx := 1; idx < len(configs); idx++ {
		outIgn = ign.Append(outIgn, configs[idx].Spec.Config)
	}
	kargs := []string{}
	labels := []MachineConfigLabels{}
	taints := []MachineConfigTaint{}
	for _, cfg := range configs {
		kargs = append(kargs, cfg.Spec.KernelArguments...)
		labels = mergeLabels(labels, cfg.Spec.Labels)
		taints = mergeTaints(taints, cfg.Spec.Taints)
	}

	return &MachineConfig{
		Spec: MachineConfigSpec{
			OSImageURL:      osImageURL,
			KernelArguments: kargs,
			Labels:          labels,
			Taints:          taints,
			Config:          outIgn,
		},
	}
}

// NewMachineConfigPoolCondition creates a new MachineConfigPool condition.
func NewMachineConfigPoolCondition(condType MachineConfigPoolConditionType, status corev1.ConditionStatus, reason, message string) *MachineConfigPoolCondition {
	return &MachineConfigPoolCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// GetMachineConfigPoolCondition returns the condition with the provided type.
func GetMachineConfigPoolCondition(status MachineConfigPoolStatus, condType MachineConfigPoolConditionType) *MachineConfigPoolCondition {
	for i := range status.Conditions {
		c := status.Conditions[i]
		if c.Type == condType {
			return &c
		}
	}
	return nil
}

// SetMachineConfigPoolCondition updates the MachineConfigPool to include the provided condition. If the condition that
// we are about to add already exists and has the same status and reason then we are not going to update.
func SetMachineConfigPoolCondition(status *MachineConfigPoolStatus, condition MachineConfigPoolCondition) {
	currentCond := GetMachineConfigPoolCondition(*status, condition.Type)
	if currentCond != nil && currentCond.Status == condition.Status && currentCond.Reason == condition.Reason {
		return
	}
	// Do not update lastTransitionTime if the status of the condition doesn't change.
	if currentCond != nil && currentCond.Status == condition.Status {
		condition.LastTransitionTime = currentCond.LastTransitionTime
	}
	newConditions := filterOutMachineConfigPoolCondition(status.Conditions, condition.Type)
	status.Conditions = append(newConditions, condition)
}

// RemoveMachineConfigPoolCondition removes the MachineConfigPool condition with the provided type.
func RemoveMachineConfigPoolCondition(status *MachineConfigPoolStatus, condType MachineConfigPoolConditionType) {
	status.Conditions = filterOutMachineConfigPoolCondition(status.Conditions, condType)
}

// filterOutCondition returns a new slice of MachineConfigPool conditions without conditions with the provided type.
func filterOutMachineConfigPoolCondition(conditions []MachineConfigPoolCondition, condType MachineConfigPoolConditionType) []MachineConfigPoolCondition {
	var newConditions []MachineConfigPoolCondition
	for _, c := range conditions {
		if c.Type == condType {
			continue
		}
		newConditions = append(newConditions, c)
	}
	return newConditions
}

// IsMachineConfigPoolConditionTrue returns true when the conditionType is present and set to `ConditionTrue`
func IsMachineConfigPoolConditionTrue(conditions []MachineConfigPoolCondition, conditionType MachineConfigPoolConditionType) bool {
	return IsMachineConfigPoolConditionPresentAndEqual(conditions, conditionType, corev1.ConditionTrue)
}

// IsMachineConfigPoolConditionFalse returns true when the conditionType is present and set to `ConditionFalse`
func IsMachineConfigPoolConditionFalse(conditions []MachineConfigPoolCondition, conditionType MachineConfigPoolConditionType) bool {
	return IsMachineConfigPoolConditionPresentAndEqual(conditions, conditionType, corev1.ConditionFalse)
}

// IsMachineConfigPoolConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsMachineConfigPoolConditionPresentAndEqual(conditions []MachineConfigPoolCondition, conditionType MachineConfigPoolConditionType, status corev1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// NewKubeletConfigCondition returns an instance of a KubeletConfigCondition
func NewKubeletConfigCondition(condType KubeletConfigStatusConditionType, status corev1.ConditionStatus, message string) *KubeletConfigCondition {
	return &KubeletConfigCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Message:            message,
	}
}

// NewContainerRuntimeConfigCondition returns an instance of a ContainerRuntimeConfigCondition
func NewContainerRuntimeConfigCondition(condType ContainerRuntimeConfigStatusConditionType, status corev1.ConditionStatus, message string) *ContainerRuntimeConfigCondition {
	return &ContainerRuntimeConfigCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Message:            message,
	}
}

// NewControllerConfigStatusCondition creates a new ControllerConfigStatus condition.
func NewControllerConfigStatusCondition(condType ControllerConfigStatusConditionType, status corev1.ConditionStatus, reason, message string) *ControllerConfigStatusCondition {
	return &ControllerConfigStatusCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

// GetControllerConfigStatusCondition returns the condition with the provided type.
func GetControllerConfigStatusCondition(status ControllerConfigStatus, condType ControllerConfigStatusConditionType) *ControllerConfigStatusCondition {
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
func SetControllerConfigStatusCondition(status *ControllerConfigStatus, condition ControllerConfigStatusCondition) {
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
func RemoveControllerConfigStatusCondition(status *ControllerConfigStatus, condType ControllerConfigStatusConditionType) {
	status.Conditions = filterOutControllerConfigStatusCondition(status.Conditions, condType)
}

// filterOutCondition returns a new slice of ControllerConfigStatus conditions without conditions with the provided type.
func filterOutControllerConfigStatusCondition(conditions []ControllerConfigStatusCondition, condType ControllerConfigStatusConditionType) []ControllerConfigStatusCondition {
	var newConditions []ControllerConfigStatusCondition
	for _, c := range conditions {
		if c.Type == condType {
			continue
		}
		newConditions = append(newConditions, c)
	}
	return newConditions
}

// IsControllerConfigStatusConditionTrue returns true when the conditionType is present and set to `ConditionTrue`
func IsControllerConfigStatusConditionTrue(conditions []ControllerConfigStatusCondition, conditionType ControllerConfigStatusConditionType) bool {
	return IsControllerConfigStatusConditionPresentAndEqual(conditions, conditionType, corev1.ConditionTrue)
}

// IsControllerConfigStatusConditionFalse returns true when the conditionType is present and set to `ConditionFalse`
func IsControllerConfigStatusConditionFalse(conditions []ControllerConfigStatusCondition, conditionType ControllerConfigStatusConditionType) bool {
	return IsControllerConfigStatusConditionPresentAndEqual(conditions, conditionType, corev1.ConditionFalse)
}

// IsControllerConfigStatusConditionPresentAndEqual returns true when conditionType is present and equal to status.
func IsControllerConfigStatusConditionPresentAndEqual(conditions []ControllerConfigStatusCondition, conditionType ControllerConfigStatusConditionType, status corev1.ConditionStatus) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// IsControllerConfigCompleted checks whether a ControllerConfig is completed by the Template Controller
func IsControllerConfigCompleted(ccName string, ccGetter func(string) (*ControllerConfig, error)) error {
	cur, err := ccGetter(ccName)
	if err != nil {
		return err
	}

	if cur.Generation != cur.Status.ObservedGeneration {
		return fmt.Errorf("status for ControllerConfig %s is being reported for %d, expecting it for %d", ccName, cur.Status.ObservedGeneration, cur.Generation)
	}

	completed := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, TemplateContollerCompleted)
	running := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, TemplateContollerRunning)
	failing := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, TemplateContollerFailing)
	if completed &&
		!running &&
		!failing {
		return nil
	}
	return fmt.Errorf("ControllerConfig has not completed: completed(%v) running(%v) failing(%v)", completed, running, failing)
}
