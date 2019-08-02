package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ContainerRuntimeConfig describes a customized Container Runtime configuration.
type ContainerRuntimeConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec ContainerRuntimeConfigSpec `json:"spec"`
	// +optional
	Status ContainerRuntimeConfigStatus `json:"status"`
}

// ContainerRuntimeConfigSpec defines the desired state of ContainerRuntimeConfig
type ContainerRuntimeConfigSpec struct {
	MachineConfigPoolSelector *metav1.LabelSelector          `json:"machineConfigPoolSelector,omitempty"`
	ContainerRuntimeConfig    *ContainerRuntimeConfiguration `json:"containerRuntimeConfig,omitempty"`
}

// ContainerRuntimeConfiguration defines the tuneables of the container runtime
type ContainerRuntimeConfiguration struct {
	// pidsLimit specifies the maximum number of processes allowed in a container
	PidsLimit int64 `json:"pidsLimit,omitempty"`

	// logLevel specifies the verbosity of the logs based on the level it is set to.
	// Options are fatal, panic, error, warn, info, and debug.
	LogLevel string `json:"logLevel,omitempty"`

	// logSizeMax specifies the Maximum size allowed for the container log file.
	// Negative numbers indicate that no size limit is imposed.
	// If it is positive, it must be >= 8192 to match/exceed conmon's read buffer.
	LogSizeMax resource.Quantity `json:"logSizeMax"`

	// overlaySize specifies the maximum size of a container image.
	// This flag can be used to set quota on the size of container images. (default: 10GB)
	OverlaySize resource.Quantity `json:"overlaySize"`
}

// ContainerRuntimeConfigStatus defines the observed state of a ContainerRuntimeConfig
type ContainerRuntimeConfigStatus struct {
	// observedGeneration represents the generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions represents the latest available observations of current state.
	// +optional
	Conditions []ContainerRuntimeConfigCondition `json:"conditions"`
}

// ContainerRuntimeConfigCondition defines the state of the ContainerRuntimeConfig
type ContainerRuntimeConfigCondition struct {
	// type specifies the state of the operator's reconciliation functionality.
	Type ContainerRuntimeConfigStatusConditionType `json:"type"`

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

// ContainerRuntimeConfigStatusConditionType is the state of the operator's reconciliation functionality.
type ContainerRuntimeConfigStatusConditionType string

const (
	// ContainerRuntimeConfigSuccess designates a successful application of a ContainerRuntimeConfig CR.
	ContainerRuntimeConfigSuccess ContainerRuntimeConfigStatusConditionType = "Success"

	// ContainerRuntimeConfigFailure designates a failure applying a ContainerRuntimeConfig CR.
	ContainerRuntimeConfigFailure ContainerRuntimeConfigStatusConditionType = "Failure"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ContainerRuntimeConfigList is a list of ContainerRuntimeConfig resources
type ContainerRuntimeConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ContainerRuntimeConfig `json:"items"`
}
