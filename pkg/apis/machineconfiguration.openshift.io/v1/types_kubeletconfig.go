package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KubeletConfig describes a customized Kubelet configuration.
type KubeletConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec KubeletConfigSpec `json:"spec"`
	// +optional
	Status KubeletConfigStatus `json:"status"`
}

// KubeletConfigSpec defines the desired state of KubeletConfig
type KubeletConfigSpec struct {
	MachineConfigPoolSelector *metav1.LabelSelector `json:"machineConfigPoolSelector,omitempty"`
	KubeletConfig             *runtime.RawExtension `json:"kubeletConfig,omitempty"`
}

// KubeletConfigStatus defines the observed state of a KubeletConfig
type KubeletConfigStatus struct {
	// observedGeneration represents the generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represents the latest available observations of current state.
	// +optional
	Conditions []KubeletConfigCondition `json:"conditions"`
}

// KubeletConfigCondition defines the state of the KubeletConfig
type KubeletConfigCondition struct {
	// type specifies the state of the operator's reconciliation functionality.
	Type KubeletConfigStatusConditionType `json:"type"`

	// status of the condition, one of True, False, Unknown.
	Status corev1.ConditionStatus `json:"status"`

	// lastTransitionTime is the time of the last update to the current status object.
	// +nullable
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`

	// reason is the reason for the condition's last transition.  Reasons are PascalCase
	Reason string `json:"reason,omitempty"`

	// message provides additional information about the current condition.
	// This is only to be consumed by humans.
	Message string `json:"message,omitempty"`
}

// KubeletConfigStatusConditionType is the state of the operator's reconciliation functionality.
type KubeletConfigStatusConditionType string

const (
	// KubeletConfigSuccess designates a successful application of a KubeletConfig CR.
	KubeletConfigSuccess KubeletConfigStatusConditionType = "Success"

	// KubeletConfigFailure designates a failure applying a KubeletConfig CR.
	KubeletConfigFailure KubeletConfigStatusConditionType = "Failure"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KubeletConfigList is a list of KubeletConfig resources
type KubeletConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []KubeletConfig `json:"items"`
}
