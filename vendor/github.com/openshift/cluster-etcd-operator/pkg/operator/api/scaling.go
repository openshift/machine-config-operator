package api

import (
	v1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type EtcdScaling struct {
	Metadata *metav1.ObjectMeta `json:"metadata,omitempty"`
	Members  []Member           `json:"members,omitempty"`
	// deprecated pending removal
	PodFQDN string `json:"podFQDN,omitempty"`
}

type Member struct {
	ID         uint64            `json:"ID,omitempty"`
	Name       string            `json:"name,omitempty"`
	PeerURLS   []string          `json:"peerURLs,omitempty"`
	ClientURLS []string          `json:"clientURLs,omitempty"`
	Conditions []MemberCondition `json:"conditions,omitempty"`
}

type MemberCondition struct {
	// type describes the current condition
	Type MemberConditionType `json:"type"`
	// status is the status of the condition (True, False, Unknown)
	Status v1.ConditionStatus `json:"status"`
	// timestamp for the last update to this condition
	// +optional
	LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty"`
	// reason is the reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty"`
	// message is a human-readable explanation containing details about
	// the transition
	// +optional
	Message string `json:"message,omitempty"`
}

type MemberConditionType string

const (
	// Ready indicated the member is part of the cluster and endpoint is Ready
	MemberReady MemberConditionType = "Ready"
	// Unknown indicated the member condition is unknown and requires further observations to verify
	MemberUnknown MemberConditionType = "Unknown"
	// Degraded indicates the member pod is in a degraded state and should be restarted
	MemberDegraded MemberConditionType = "Degraded"
	// Remove indicates the member should be removed from the cluster
	MemberRemove MemberConditionType = "Remove"
	// MemberAdd is a member who is ready to join cluster but currently has not
	MemberAdd MemberConditionType = "Add"
)

func GetMemberCondition(status string) MemberConditionType {
	switch {
	case status == string(MemberReady):
		return MemberReady
	case status == string(MemberRemove):
		return MemberRemove
	case status == string(MemberUnknown):
		return MemberUnknown
	case status == string(MemberDegraded):
		return MemberDegraded
	}
	return ""
}
