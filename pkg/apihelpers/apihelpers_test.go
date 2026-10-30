package apihelpers

import (
	"testing"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultClusterPoliciesFRRPeersFile locks the default NodeDisruptionPolicy
// for the BGP VIP peers file: after the bootstrap-to-CRD handover the on-disk
// file only matters at early boot, so day-2 edits must not disrupt nodes.
func TestDefaultClusterPoliciesFRRPeersFile(t *testing.T) {
	merged := MergeClusterPolicies(opv1.NodeDisruptionPolicyConfig{})

	var found *opv1.NodeDisruptionPolicyStatusFile
	for i := range merged.Files {
		if merged.Files[i].Path == "/etc/kubernetes/static-pod-resources/frr-k8s/frr-peers.json" {
			found = &merged.Files[i]
			break
		}
	}
	require.NotNil(t, found, "default cluster policies must cover the frr-peers.json path")
	require.Len(t, found.Actions, 1)
	assert.Equal(t, opv1.NoneStatusAction, found.Actions[0].Type)
}

// TestDefaultClusterPoliciesUserOverride ensures a user-defined policy for the
// frr-peers.json path takes precedence over the default None action.
func TestDefaultClusterPoliciesUserOverride(t *testing.T) {
	userPolicies := opv1.NodeDisruptionPolicyConfig{
		Files: []opv1.NodeDisruptionPolicySpecFile{
			{
				Path: "/etc/kubernetes/static-pod-resources/frr-k8s/frr-peers.json",
				Actions: []opv1.NodeDisruptionPolicySpecAction{
					{Type: opv1.RebootSpecAction},
				},
			},
		},
	}
	merged := MergeClusterPolicies(userPolicies)

	count := 0
	for _, file := range merged.Files {
		if file.Path == "/etc/kubernetes/static-pod-resources/frr-k8s/frr-peers.json" {
			count++
			require.Len(t, file.Actions, 1)
			assert.Equal(t, opv1.RebootStatusAction, file.Actions[0].Type)
		}
	}
	assert.Equal(t, 1, count, "user override must replace, not duplicate, the default entry")
}
