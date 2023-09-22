package v1

import (
	v1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type MachineStateProgressionConditionApplyConfiguration struct {
	Kind    *v1.OperatorObject                                 `json:"kind"`
	State   *v1.StateProgress                                  `json:"state"`
	Name    *string                                            `json:"name"`
	Phase   *string                                            `json:"phase"`
	Reason  *string                                            `json:"reason"`
	Time    *metav1.Time                                       `json:"time"`
	History []MachineStateProgressionHistoryApplyConfiguration `json:"history"`
}

func MachineStateProgressionCondition() *MachineStateProgressionConditionApplyConfiguration {
	return &MachineStateProgressionConditionApplyConfiguration{}
}

func (b *MachineStateProgressionConditionApplyConfiguration) WithKind(value v1.OperatorObject) *MachineStateProgressionConditionApplyConfiguration {
	b.Kind = &value
	return b
}

func (b *MachineStateProgressionConditionApplyConfiguration) WithState(value v1.StateProgress) *MachineStateProgressionConditionApplyConfiguration {
	b.State = &value
	return b
}

func (b *MachineStateProgressionConditionApplyConfiguration) WithName(value string) *MachineStateProgressionConditionApplyConfiguration {
	b.Name = &value
	return b
}

func (b *MachineStateProgressionConditionApplyConfiguration) WithReason(value string) *MachineStateProgressionConditionApplyConfiguration {
	b.Reason = &value
	return b
}

func (b *MachineStateProgressionConditionApplyConfiguration) WithTime(value metav1.Time) *MachineStateProgressionConditionApplyConfiguration {
	b.Time = &value
	return b
}

func (b *MachineStateProgressionConditionApplyConfiguration) WithProgressionHistory(value []MachineStateProgressionHistoryApplyConfiguration) *MachineStateProgressionConditionApplyConfiguration {
	for _, val := range value {
		b.History = append(b.History, val)
	}
	return b
}
