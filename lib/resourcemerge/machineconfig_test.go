package resourcemerge

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

// TestEnsureControllerConfigSpecBGPVIPPeersJSON guards day-2 propagation of
// the BGP VIP peers payload: live validation showed that without this merge
// the ControllerConfig silently kept the stale peer list after CR edits.
func TestEnsureControllerConfigSpecBGPVIPPeersJSON(t *testing.T) {
	tests := []struct {
		name             string
		existing         string
		required         string
		expectedModified bool
		expected         string
	}{
		{
			name:             "day-2 change is merged",
			existing:         `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513}]}`,
			required:         `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513},{"peerAddress":"192.168.111.9","peerASN":64520}]}`,
			expectedModified: true,
			expected:         `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513},{"peerAddress":"192.168.111.9","peerASN":64520}]}`,
		},
		{
			name:             "identical payload is a no-op",
			existing:         `{"localASN":64512}`,
			required:         `{"localASN":64512}`,
			expectedModified: false,
			expected:         `{"localASN":64512}`,
		},
		{
			name:             "empty required holds the last good payload",
			existing:         `{"localASN":64512}`,
			required:         "",
			expectedModified: false,
			expected:         `{"localASN":64512}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			existing := mcfgv1.ControllerConfigSpec{BGPVIPPeersJSON: test.existing, Infra: &configv1.Infrastructure{}}
			required := mcfgv1.ControllerConfigSpec{BGPVIPPeersJSON: test.required, Infra: &configv1.Infrastructure{}}
			modified := false
			ensureControllerConfigSpec(&modified, &existing, required)
			if modified != test.expectedModified {
				t.Errorf("modified: expected %v, got %v", test.expectedModified, modified)
			}
			if existing.BGPVIPPeersJSON != test.expected {
				t.Errorf("BGPVIPPeersJSON: expected %q, got %q", test.expected, existing.BGPVIPPeersJSON)
			}
		})
	}
}
