package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// OperatorStatusList is a list of OperatorStatus resources.
// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type OperatorStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []OperatorStatus `json:"items"`
}

// OperatorStatus is the Custom Resource object which holds the current state
// of an operator. This object is used by operators to convey their state to
// the rest of the cluster.
// +genclient
// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type OperatorStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	// Condition describes the state of the operator's reconciliation
	// functionality.
	Condition OperatorStatusCondition `json:"condition"`

	// Version indicates which version of the operator updated the current
	// status object.
	Version string `json:"version"`

	// LasteUpdate is the time of the last update to the current status object.
	LastUpdate metav1.Time `json:"lastUpdate"`

	// Extension contains any additional status information specific to the
	// operator which owns this status object.
	Extension runtime.RawExtension `json:"extension"`
}

// OperatorStatusCondition represents the state of the operator's
// reconciliation functionality.
type OperatorStatusCondition struct {
	// Type specifies the state of the operator's reconciliation functionality.
	Type OperatorStatusConditionType `json:"type"`

	// Message provides any additional information about the current condition.
	// This is only to be consumed by humans.
	Message string `json:"message"`
}

// OperatorStatusConditionType is the state of the operator's reconciliation
// functionality.
type OperatorStatusConditionType string

const (
	// OperatorStatusConditionTypeWaiting indicates that the operator isn't
	// running its reconciliation functionality. This may be because a
	// dependency or other prerequisite hasn't been satisfied.
	OperatorStatusConditionTypeWaiting OperatorStatusConditionType = "Waiting"

	// OperatorStatusConditionTypeWorking indicates that the operator is
	// actively reconciling its operands.
	OperatorStatusConditionTypeWorking OperatorStatusConditionType = "Working"

	// OperatorStatusConditionTypeDone indicates that the operator has finished
	// reconciling its operands and is waiting for changes.
	OperatorStatusConditionTypeDone OperatorStatusConditionType = "Done"

	// OperatorStatusConditionTypeDegraded indicates that the operator has
	// encountered an error that is preventing it from working properly.
	OperatorStatusConditionTypeDegraded OperatorStatusConditionType = "Degraded"
)
