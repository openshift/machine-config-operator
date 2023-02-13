package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	imagestreamv1 "github.com/openshift/api/image/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	// These patterns are intentionally left unterminated so they match all RHCOS 8 / 9 versions.
	rhelVersion8 string = "RHEL_VERSION=\"8"
	rhelVersion9 string = "RHEL_VERSION=\"9"
)

// Verifies that the OS on each node is identifiable using the mechanism the
// MCD does. This is done to ensure that we get an early warning if the
// contents of /etc/os-release or /usr/lib/os-release unexpectedly change.
//
// We do the same for the labels attached to the OS image associated with the
// release. This is done for the same early warning.
func TestOSDetection(t *testing.T) {
	cs := framework.NewClientSet("")
	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	osImageRelease, osImageReleaseSrc := getOSReleaseInfoForOSImage(t, cs, nodes.Items[0])

	for _, node := range nodes.Items {
		node := node
		t.Run("", func(t *testing.T) {
			t.Parallel()

			nodeOSRelease := helpers.GetOSReleaseForNode(t, cs, node)

			assert.Equal(t, nodeOSRelease.EtcContent, nodeOSRelease.LibContent)
			assert.True(t, nodeOSRelease.OS.IsCoreOSVariant(), "expected IsCoreOSVariant() to be true: %s", nodeOSRelease.EtcContent)
			assert.False(t, nodeOSRelease.OS.IsLikeTraditionalRHEL7(), "expected IsLikeTraditionalRHEL7() to be false: %s", nodeOSRelease.EtcContent)

			switch {
			case strings.Contains(nodeOSRelease.EtcContent, osrelease.RHCOS) && strings.Contains(nodeOSRelease.EtcContent, rhelVersion8):
				rhelVersion := nodeOSRelease.OS.Values()["RHEL_VERSION"]
				assertIsRHCOS8(t, nodeOSRelease.OS, nodeOSRelease.EtcContent)
				assertIsRHCOS8(t, osImageRelease, osImageReleaseSrc)
				t.Logf("Identified %s %s on node %s", osrelease.RHCOS, rhelVersion, node.Name)
			case strings.Contains(nodeOSRelease.EtcContent, osrelease.RHCOS) && strings.Contains(nodeOSRelease.EtcContent, rhelVersion9):
				rhelVersion := nodeOSRelease.OS.Values()["RHEL_VERSION"]
				assertIsRHCOS9(t, nodeOSRelease.OS, nodeOSRelease.EtcContent)
				assertIsRHCOS9(t, osImageRelease, osImageReleaseSrc)
				t.Logf("Identified %s %s on node %s", osrelease.RHCOS, rhelVersion, node.Name)
			case strings.Contains(nodeOSRelease.EtcContent, osrelease.SCOS):
				assertIsSCOS(t, nodeOSRelease.OS, nodeOSRelease.EtcContent)
				assertIsSCOS(t, osImageRelease, osImageReleaseSrc)
				t.Logf("Identified %s on node %s", osrelease.SCOS, node.Name)
			case strings.Contains(nodeOSRelease.EtcContent, osrelease.FCOS):
				assertIsFCOS(t, nodeOSRelease.OS, nodeOSRelease.EtcContent)
				assertIsFCOS(t, osImageRelease, osImageReleaseSrc)
				t.Logf("Identified %s on node %s", osrelease.FCOS, node.Name)
			default:
				t.Fatalf("unknown OS on node %s detected: %s", node.Name, nodeOSRelease.EtcContent)
			}
		})
	}
}

func assertIsRHCOS(t *testing.T, osr osrelease.OperatingSystem, source string) {
	t.Helper()

	assert.True(t, osr.IsEL(), "expected IsEL() to be true: %s", source)
	assert.False(t, osr.IsSCOS(), "expected IsSCOS() to be false: %s", source)
	assert.False(t, osr.IsFCOS(), "expected IsFCOS() to be false: %s", source)
}

func assertIsRHCOS8(t *testing.T, osr osrelease.OperatingSystem, source string) {
	t.Helper()

	assertIsRHCOS(t, osr, source)
	assert.False(t, osr.IsEL9(), "expected < RHCOS 9.0: %s", source)
}

func assertIsRHCOS9(t *testing.T, osr osrelease.OperatingSystem, source string) {
	t.Helper()

	assertIsRHCOS(t, osr, source)
	assert.True(t, osr.IsEL9(), "expected >= RHCOS 9.0+: %s", source)
}

func assertIsFCOS(t *testing.T, osr osrelease.OperatingSystem, source string) {
	t.Helper()

	assert.False(t, osr.IsEL(), "expected IsEL() to be false: %s", source)
	assert.False(t, osr.IsEL9(), "expected IsEL9() to be false: %s", source)
	assert.False(t, osr.IsSCOS(), "expected IsSCOS() to be false: %s", source)
	assert.True(t, osr.IsFCOS(), "expected IsFCOS() to be true: %s", source)
}

func assertIsSCOS(t *testing.T, osr osrelease.OperatingSystem, source string) {
	assert.True(t, osr.IsEL(), "expected IsEL() to be true: %s", source)
	assert.True(t, osr.IsEL9(), "expected IsEL9() to be true: %s", source)
	assert.False(t, osr.IsFCOS(), "expected IsFCOS() to be false: %s", source)
}

// Gets the image labels from the relevant OS image from the cluster release.
// There is probably a better way to do this, but this seemed to be the easiest
// that would work across multiple testing contexts (e.g., local developer / CI / etc.)
func getOSReleaseInfoForOSImage(t *testing.T, cs *framework.ClientSet, node corev1.Node) (osrelease.OperatingSystem, string) {
	t.Helper()

	authFile := "/var/lib/kubelet/config.json"

	version, err := cs.ClusterVersions().Get(context.TODO(), "version", metav1.GetOptions{})
	require.NoError(t, err)

	// Runs oc adm release info on a given node to get the release ImageStream.
	// We're only interested in the references object, so we use jsonpath to
	// limit output to just that. This is equivalent to:
	// $ oc adm release info "release-image" -o json | jq '.references'
	rawImagestream := helpers.ExecCmdOnNode(t, cs, node, []string{
		"chroot",
		"/rootfs",
		"oc",
		"adm",
		"release",
		"info",
		"--registry-config",
		authFile,
		"-o=jsonpath={.references}",
		version.Status.Desired.Image,
	}...)

	releaseImagestream := imagestreamv1.ImageStream{}

	require.NoError(t, json.Unmarshal([]byte(rawImagestream), &releaseImagestream))

	// These are the known OS image tag names.
	knownTags := sets.NewString("rhel-coreos-8", "rhel-coreos-9", "fedora-coreos", "centos-stream-coreos-9")
	knownTagMatch := ""
	osImagePullspec := ""

	// Locate the image pullspec based upon the known tag name.
	for _, tag := range releaseImagestream.Spec.Tags {
		if knownTags.Has(tag.Name) {
			knownTagMatch = tag.Name
			osImagePullspec = tag.From.Name
			break
		}
	}

	if osImagePullspec == "" {
		t.Fatalf("couldn't find a known OS image pullspec in release %s; known OS image pullspecs: %v", version.Status.Desired.Image, knownTags.List())
	} else {
		t.Logf("Found OS image %q matching tag %q in OCP / OKD release image %q. Will compare to what is on cluster nodes", osImagePullspec, knownTagMatch, version.Status.Desired.Image)
	}

	// Use skopeo on the node to get the labels for the OS image pullspec.
	// This is equivalent to:
	// $ skopeo inspect docker://<pullspec> | jq '.Labels'
	rawImageLabels := helpers.ExecCmdOnNode(t, cs, node, []string{
		"chroot",
		"/rootfs",
		"skopeo",
		"inspect",
		"--no-tags",
		"--authfile",
		authFile,
		fmt.Sprintf("docker://%s", osImagePullspec),
	}...)

	type imageLabels struct {
		Labels map[string]string
	}

	labels := imageLabels{}

	require.NoError(t, json.Unmarshal([]byte(rawImageLabels), &labels))

	osr, err := osrelease.InferFromOSImageLabels(labels.Labels)
	require.NoError(t, err)

	return osr, fmt.Sprintf("Labels for OS image %q: %v", osImagePullspec, labels.Labels)
}
