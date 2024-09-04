package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Verifies that the OS on each node is identifiable using the mechanism the
// MCD does. This is done to ensure that we get an early warning if the
// contents of /etc/os-release or /usr/lib/os-release unexpectedly change.
func TestOSDetection(t *testing.T) {
	cs := framework.NewClientSet("")
	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	for _, node := range nodes.Items {
		node := node
		t.Run("", func(t *testing.T) {
			t.Parallel()

			nodeOSRelease := helpers.GetOSReleaseForNode(t, cs, node)

			assert.Equal(t, nodeOSRelease.EtcContent, nodeOSRelease.LibContent)
			assert.True(t, nodeOSRelease.OS.IsCoreOSVariant(), "expected IsCoreOSVariant() to be true: %s", nodeOSRelease.EtcContent)
			assert.False(t, nodeOSRelease.OS.IsLikeTraditionalRHEL7(), "expected IsLikeTraditionalRHEL7() to be false: %s", nodeOSRelease.EtcContent)

			switch {
			case strings.Contains(nodeOSRelease.EtcContent, osrelease.RHCOS):
				assertRHCOS(t, nodeOSRelease, node)
			case strings.Contains(nodeOSRelease.EtcContent, osrelease.SCOS):
				assertSCOS(t, nodeOSRelease, node)
			case strings.Contains(nodeOSRelease.EtcContent, osrelease.FCOS):
				assertFCOS(t, nodeOSRelease, node)
			default:
				t.Fatalf("unknown OS on node %s detected: %s", node.Name, nodeOSRelease.EtcContent)
			}
		})
	}
}

func assertRHCOS(t *testing.T, nodeOSRelease helpers.NodeOSRelease, node corev1.Node) {
	assert.True(t, nodeOSRelease.OS.IsEL(), "expected IsEL() to be true: %s", nodeOSRelease.EtcContent)
	assert.False(t, nodeOSRelease.OS.IsSCOS(), "expected IsSCOS() to be false: %s", nodeOSRelease.EtcContent)
	assert.False(t, nodeOSRelease.OS.IsFCOS(), "expected IsFCOS() to be false: %s", nodeOSRelease.EtcContent)

	rhelVersion := nodeOSRelease.OS.OSRelease().ADDITIONAL_FIELDS["RHEL_VERSION"]

	if strings.Contains(nodeOSRelease.EtcContent, "RHEL_VERSION=\"8.") { // This pattern intentionally unterminated so it matches all RHCOS 8 versions
		t.Logf("Identified %s %s on node %s", osrelease.RHCOS, rhelVersion, node.Name)
		assert.False(t, nodeOSRelease.OS.IsEL9(), "expected < RHCOS 9.0: %s", nodeOSRelease.EtcContent)
	}

	if strings.Contains(nodeOSRelease.EtcContent, "RHEL_VERSION=\"9.") { // This pattern intentionally unterminated so it matches all RHCOS 9 versions
		t.Logf("Identified %s %s on node %s", osrelease.RHCOS, rhelVersion, node.Name)
		assert.True(t, nodeOSRelease.OS.IsEL9(), "expected >= RHCOS 9.0+: %s", nodeOSRelease.EtcContent)
	}
}

func assertSCOS(t *testing.T, nodeOSRelease helpers.NodeOSRelease, node corev1.Node) {
	t.Logf("Identified %s on node %s", osrelease.SCOS, node.Name)
	assert.True(t, nodeOSRelease.OS.IsEL(), "expected IsEL() to be true: %s", nodeOSRelease.EtcContent)
	assert.True(t, nodeOSRelease.OS.IsEL9(), "expected IsEL9() to be true: %s", nodeOSRelease.EtcContent)
	assert.False(t, nodeOSRelease.OS.IsFCOS(), "expected IsFCOS() to be false: %s", nodeOSRelease.EtcContent)
}

func assertFCOS(t *testing.T, nodeOSRelease helpers.NodeOSRelease, node corev1.Node) {
	t.Logf("Identified OS %s on node %s", osrelease.FCOS, node.Name)
	assert.False(t, nodeOSRelease.OS.IsEL(), "expected IsEL() to be false: %s", nodeOSRelease.EtcContent)
	assert.False(t, nodeOSRelease.OS.IsEL9(), "expected IsEL9() to be false: %s", nodeOSRelease.EtcContent)
	assert.False(t, nodeOSRelease.OS.IsSCOS(), "expected IsSCOS() to be false: %s", nodeOSRelease.EtcContent)
	assert.True(t, nodeOSRelease.OS.IsFCOS(), "expected IsFCOS() to be true: %s", nodeOSRelease.EtcContent)
}
