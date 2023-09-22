package v1

import (
	v1 "github.com/openshift/api/machineconfiguration/v1"
)

type MachineStateProgressionHistoryApplyConfiguration struct {
	State  *v1.StateProgress `json:"state"`
	Phase  *string           `json:"phase"`
	Reason *string           `json:"reason"`
}

func MachineStateProgressionHistory() *MachineStateProgressionHistoryApplyConfiguration {
	return &MachineStateProgressionHistoryApplyConfiguration{}
}

func (b *MachineStateProgressionHistoryApplyConfiguration) WithState(value v1.StateProgress) *MachineStateProgressionHistoryApplyConfiguration {
	b.State = &value
	return b
}

func (b *MachineStateProgressionHistoryApplyConfiguration) WithReason(value string) *MachineStateProgressionHistoryApplyConfiguration {
	b.Reason = &value
	return b
}
