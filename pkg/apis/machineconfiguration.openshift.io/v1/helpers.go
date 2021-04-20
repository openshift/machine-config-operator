package v1

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// proxyTestImage will be skopeo inspect through http proxy if set for proxy validation
const proxyTestImage = "quay.io/podman/stable"

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

	completed := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, TemplateControllerCompleted)
	running := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, TemplateControllerRunning)
	failing := IsControllerConfigStatusConditionTrue(cur.Status.Conditions, TemplateControllerFailing)
	if completed &&
		!running &&
		!failing {
		return nil
	}
	return fmt.Errorf("ControllerConfig has not completed: completed(%v) running(%v) failing(%v)", completed, running, failing)
}

// ProxyValidation inspects the proxyTestImage to validate if the http proxy is valid.
func ProxyValidation(proxy *configv1.ProxyStatus) error {
	if proxy == nil {
		return nil
	}
	return skopeoInspectUseProxy(proxyTestImage, proxy.HTTPProxy, proxy.HTTPSProxy)
}

// skopeoInspectUseProxy uses skopeo to inspect the proxyTestImage, through the http(s) proxy if exists.
// returns error if the inspection fails.
func skopeoInspectUseProxy(imageURL, httpProxy, httpsProxy string) error {
	// proxy error from docker registry starts with proxyErr
	proxyErr := "proxyconnect"
	skopeo, err := exec.LookPath("skopeo")
	if err != nil {
		return err
	}
	imageSpec := fmt.Sprintf("docker://%s", imageURL)
	var envs []string
	if httpProxy != "" {
		envs = append(envs, fmt.Sprintf("HTTP_PROXY=%s", httpProxy))
	}
	if httpsProxy != "" {
		envs = append(envs, fmt.Sprintf("HTTPS_PROXY=%s", httpsProxy))
	}

	args := []string{"inspect", imageSpec}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, skopeo, args...)
	cmd.Env = envs
	rawOut, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(rawOut), proxyErr) {
			return fmt.Errorf("invalid http proxy: %w: error running %s %s: %s", err, skopeo, strings.Join(args, " "), string(rawOut))
		}
		return fmt.Errorf("%w: error running %s %s: %s", err, skopeo, strings.Join(args, " "), string(rawOut))
	}
	return nil
}
