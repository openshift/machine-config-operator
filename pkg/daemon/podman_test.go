package daemon

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetPodmanImageInfoByReference_Success(t *testing.T) {
	reference := "quay.io/openshift/test:latest"
	imageInfo := []PodmanImageInfo{
		{
			ID:     "abc123",
			Digest: "sha256:1234567890abcdef",
			RepoDigests: []string{
				"quay.io/openshift/test@sha256:1234567890abcdef",
				reference,
			},
		},
	}

	jsonOutput, err := json.Marshal(imageInfo)
	require.Nil(t, err)

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman images --format=json --filter reference=" + reference: jsonOutput,
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanImageInfoByReference(reference)

	assert.Nil(t, err)
	assert.NotNil(t, info)
	assert.Equal(t, "abc123", info.ID)
	assert.Equal(t, "sha256:1234567890abcdef", info.Digest)
	assert.Equal(t, reference, info.RepoDigest)
	assert.Len(t, info.RepoDigests, 2)
}

func TestGetPodmanImageInfoByReference_NoMatch(t *testing.T) {
	reference := "quay.io/openshift/nonexistent:latest"
	// If there are no matches podman returns an empty string
	jsonOutput := []byte("[]")

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman images --format=json --filter reference=" + reference: jsonOutput,
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanImageInfoByReference(reference)

	assert.Nil(t, err)
	assert.Nil(t, info)
}

func TestGetPodmanImageInfoByReference_CommandError(t *testing.T) {
	reference := "quay.io/openshift/test:latest"

	mock := &MockCommandRunner{
		outputs: map[string][]byte{},
		errors: map[string]error{
			"podman images --format=json --filter reference=" + reference: fmt.Errorf("podman command failed"),
		},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanImageInfoByReference(reference)

	assert.Error(t, err)
	assert.Nil(t, info)
	assert.Contains(t, err.Error(), "podman command failed")
}

func TestGetPodmanImageInfoByReference_InvalidJSON(t *testing.T) {
	reference := "quay.io/openshift/test:latest"

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman images --format=json --filter reference=" + reference: []byte("invalid json"),
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanImageInfoByReference(reference)

	assert.Error(t, err)
	assert.Nil(t, info)
	assert.Contains(t, err.Error(), "failed to decode podman image ls output")
}

func TestGetPodmanImageInfoByReference_RepoDigestMatching(t *testing.T) {
	reference := "quay.io/openshift/test@sha256:specific"
	imageInfo := []PodmanImageInfo{
		{
			ID:     "xyz789",
			Digest: "sha256:specific",
			RepoDigests: []string{
				"quay.io/openshift/test@sha256:other",
				reference,
				"quay.io/openshift/test@sha256:another",
			},
		},
	}

	jsonOutput, err := json.Marshal(imageInfo)
	require.Nil(t, err)

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman images --format=json --filter reference=" + reference: jsonOutput,
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanImageInfoByReference(reference)

	assert.Nil(t, err)
	assert.NotNil(t, info)
	assert.Equal(t, reference, info.RepoDigest)
}

func TestGetPodmanImageInfoByReference_NoRepoDigestMatch(t *testing.T) {
	reference := "quay.io/openshift/test:tag"
	imageInfo := []PodmanImageInfo{
		{
			ID:     "xyz789",
			Digest: "sha256:specific",
			RepoDigests: []string{
				"quay.io/openshift/test@sha256:other",
			},
		},
	}

	jsonOutput, err := json.Marshal(imageInfo)
	require.Nil(t, err)

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman images --format=json --filter reference=" + reference: jsonOutput,
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanImageInfoByReference(reference)

	assert.Nil(t, err)
	assert.NotNil(t, info)
	// RepoDigest should be empty since reference doesn't match any RepoDigests
	assert.Equal(t, "", info.RepoDigest)
}

func TestGetPodmanInfo_Success(t *testing.T) {
	podmanInfo := PodmanInfo{
		Store: PodmanStorageConfig{
			GraphDriverName: "overlay",
			GraphRoot:       "/var/lib/containers/storage",
		},
	}

	jsonOutput, err := json.Marshal(podmanInfo)
	require.Nil(t, err)

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman system info --format=json": jsonOutput,
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanInfo()

	assert.Nil(t, err)
	assert.NotNil(t, info)
	assert.Equal(t, "overlay", info.Store.GraphDriverName)
	assert.Equal(t, "/var/lib/containers/storage", info.Store.GraphRoot)
}

func TestGetPodmanInfo_CommandError(t *testing.T) {
	mock := &MockCommandRunner{
		outputs: map[string][]byte{},
		errors: map[string]error{
			"podman system info --format=json": fmt.Errorf("podman system info failed"),
		},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanInfo()

	assert.Error(t, err)
	assert.Nil(t, info)
	assert.Contains(t, err.Error(), "podman system info failed")
}

func TestGetPodmanInfo_InvalidJSON(t *testing.T) {
	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman system info --format=json": []byte("not valid json"),
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	info, err := podman.GetPodmanInfo()

	assert.Error(t, err)
	assert.Nil(t, info)
	assert.Contains(t, err.Error(), "failed to decode podman system info output")
}

// Assisted by: Cursor
func TestCreatePodmanContainer_Success(t *testing.T) {
	containerName := "test-container"
	imgURL := "quay.io/openshift/test:latest"
	additionalArgs := []string{"--net=none", "--annotation=org.openshift.machineconfigoperator.pivot=true"}
	expectedOutput := []byte("container-id-12345")

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman create --net=none --annotation=org.openshift.machineconfigoperator.pivot=true --name test-container quay.io/openshift/test:latest": expectedOutput,
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	output, err := podman.CreatePodmanContainer(additionalArgs, containerName, imgURL)

	assert.Nil(t, err)
	assert.Equal(t, expectedOutput, output)
}

// Assisted by: Cursor
func TestCreatePodmanContainer_NoAdditionalArgs(t *testing.T) {
	containerName := "test-container"
	imgURL := "quay.io/openshift/test:latest"
	expectedOutput := []byte("container-id-67890")

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman create --name test-container quay.io/openshift/test:latest": expectedOutput,
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	output, err := podman.CreatePodmanContainer(nil, containerName, imgURL)

	assert.Nil(t, err)
	assert.Equal(t, expectedOutput, output)
}

// Assisted by: Cursor
func TestCreatePodmanContainer_EmptyAdditionalArgs(t *testing.T) {
	containerName := "test-container"
	imgURL := "quay.io/openshift/test:latest"
	expectedOutput := []byte("container-id-empty")

	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"podman create --name test-container quay.io/openshift/test:latest": expectedOutput,
		},
		errors: map[string]error{},
	}

	podman := NewPodmanExec(mock)
	output, err := podman.CreatePodmanContainer([]string{}, containerName, imgURL)

	assert.Nil(t, err)
	assert.Equal(t, expectedOutput, output)
}

// Assisted by: Cursor
func TestCreatePodmanContainer_CommandError(t *testing.T) {
	containerName := "test-container"
	imgURL := "quay.io/openshift/test:latest"
	additionalArgs := []string{"--net=none"}

	mock := &MockCommandRunner{
		outputs: map[string][]byte{},
		errors: map[string]error{
			"podman create --net=none --name test-container quay.io/openshift/test:latest": fmt.Errorf("podman create failed"),
		},
	}

	podman := NewPodmanExec(mock)
	output, err := podman.CreatePodmanContainer(additionalArgs, containerName, imgURL)

	assert.Error(t, err)
	assert.Nil(t, output)
	assert.Contains(t, err.Error(), "podman create failed")
}
