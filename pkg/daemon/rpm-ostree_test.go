package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

func TestParseVersion(t *testing.T) {
	verdata := `rpm-ostree:
  Version: '2022.10'
  Git: 6b302116c969397fd71899e3b9bb3b8c100d1af9
  Features:
   - rust
   - compose
   - rhsm
`
	var q RpmOstreeVersionData
	if err := yaml.UnmarshalStrict([]byte(verdata), &q); err != nil {
		panic(err)
	}

	assert.Equal(t, "2022.10", q.Root.Version)
	assert.Contains(t, q.Root.Features, "rust")
	assert.NotContains(t, q.Root.Features, "container")
}

// rpmOstreeVersionYAML builds a fake `rpm-ostree --version` YAML payload for
// the given Version field, mirroring real-world output.
func rpmOstreeVersionYAML(version string) []byte {
	return []byte(fmt.Sprintf(`rpm-ostree:
  Version: '%s'
  Git: 6b302116c969397fd71899e3b9bb3b8c100d1af9
  Features:
   - container
`, version))
}

func TestSupportsContainerStorageRebase(t *testing.T) {
	tests := []struct {
		name     string
		output   []byte
		cmdErr   error
		expected bool
	}{
		{
			// The exact version shipped in both the 4.13 and 4.14 RHCOS boot
			// images (confirmed via their commitmeta.json rpmdb.pkglist:
			// rpm-ostree-2023.3-1.el9_2 and rpm-ostree-2023.3-2.el9_2
			// respectively), which reproduces OCPBUGS-86768.
			name:     "4.13/4.14 shipped version is not new enough",
			output:   rpmOstreeVersionYAML("2023.3"),
			expected: false,
		},
		{
			name:     "well below the fixed version",
			output:   rpmOstreeVersionYAML("2022.10"),
			expected: false,
		},
		{
			name:     "exactly the fixed version",
			output:   rpmOstreeVersionYAML("2023.5"),
			expected: true,
		},
		{
			name:     "above the fixed version",
			output:   rpmOstreeVersionYAML("2023.6"),
			expected: true,
		},
		{
			name:     "unparseable version defaults to not supported",
			output:   rpmOstreeVersionYAML("not-a-version"),
			expected: false,
		},
		{
			name:     "command execution failure defaults to not supported",
			cmdErr:   errors.New("rpm-ostree: command not found"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// SupportsContainerStorageRebase caches its result behind a
			// package-level sync.Once (matching podmanSupportsSigstore and
			// skopeoVersionSupportsMultiArchSigstore); reset both the guard
			// and the cached value so each case is independently evaluated.
			rpmOstreeContainerStorageRebaseChecked = sync.Once{}
			rpmOstreeContainerStorageRebaseSupported = false

			mock := &MockCommandRunner{
				outputs: map[string][]byte{},
				errors:  map[string]error{},
			}
			if tt.cmdErr != nil {
				mock.errors["rpm-ostree --version"] = tt.cmdErr
			} else {
				mock.outputs["rpm-ostree --version"] = tt.output
			}

			r := &RpmOstreeClient{commandRunner: mock}
			assert.Equal(t, tt.expected, r.SupportsContainerStorageRebase())
		})
	}
}

func TestContainerStoragePolicyRequiresPatch(t *testing.T) {
	tests := []struct {
		name     string
		policy   string
		expected bool
	}{
		{
			name:     "typical permissive policy does not need a temporary policy",
			policy:   `{"default":[{"type":"insecureAcceptAnything"}]}`,
			expected: false,
		},
		{
			name:     "restrictive default needs a temporary policy",
			policy:   `{"default":[{"type":"reject"}]}`,
			expected: true,
		},
		{
			name:     "containers-storage transport rules need an exact exception",
			policy:   `{"default":[{"type":"insecureAcceptAnything"}],"transports":{"containers-storage":{"another-image":[{"type":"reject"}]}}}`,
			expected: true,
		},
		{
			name:     "additional default requirements are restrictive",
			policy:   `{"default":[{"type":"insecureAcceptAnything"},{"type":"reject"}]}`,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := parseContainerStoragePolicy([]byte(tt.policy))
			require.NoError(t, err)
			assert.Equal(t, tt.expected, containerStoragePolicyRequiresPatch(policy))
		})
	}
}

func TestAddContainerStoragePolicyException(t *testing.T) {
	const (
		policyInput = `{
			"default": [{"type":"reject"}],
			"transports": {
				"docker": {
					"registry.example.com": [{"type":"insecureAcceptAnything"}]
				},
				"containers-storage": {
					"existing-image": [{"type":"reject"}]
				}
			}
		}`
		policyKey = "[overlay@/var/lib/containers/storage]quay.io/example/os@sha256:repo@image-id"
	)

	policy, err := parseContainerStoragePolicy([]byte(policyInput))
	require.NoError(t, err)

	patched, err := addContainerStoragePolicyException(policy, policyKey)
	require.NoError(t, err)

	var output map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(patched, &output))
	require.JSONEq(t, `[{"type":"reject"}]`, string(output["default"]))

	var transports map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(output["transports"], &transports))
	require.JSONEq(t, `[{"type":"insecureAcceptAnything"}]`, string(transports["docker"]["registry.example.com"]))
	require.JSONEq(t, `[{"type":"reject"}]`, string(transports[imagePolicyTransportContainerStorage]["existing-image"]))
	require.JSONEq(t, `[{"type":"insecureAcceptAnything"}]`, string(transports[imagePolicyTransportContainerStorage][policyKey]))
	require.NotContains(t, transports[imagePolicyTransportContainerStorage], "")
}

func TestAddContainerStoragePolicyExceptionRejectsEmptyKey(t *testing.T) {
	policy, err := parseContainerStoragePolicy([]byte(`{"default":[{"type":"reject"}]}`))
	require.NoError(t, err)

	_, err = addContainerStoragePolicyException(policy, "")
	assert.ErrorContains(t, err, "policy key must not be empty")
}

func TestPatchPoliciesForContainerStorage_RestrictivePolicy(t *testing.T) {
	const (
		repoDigest = "quay.io/openshift/os@sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
		imageID    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	hostPolicy := []byte(`{
		"default": [{"type":"reject"}],
		"transports": {
			"docker": {
				"quay.io": [{"type":"insecureAcceptAnything"}]
			}
		}
	}`)
	tmpDir := t.TempDir()
	paths := temporalPolicyFilePaths{
		hostPolicyPath:      filepath.Join(tmpDir, "host-policy.json"),
		temporaryPolicyPath: filepath.Join(tmpDir, "run", "policy.json"),
		dropinPath:          filepath.Join(tmpDir, "systemd", "temporal-policy.conf"),
	}
	require.NoError(t, os.WriteFile(paths.hostPolicyPath, hostPolicy, 0o600))

	reloadCount := 0
	paths.systemdReload = func() error {
		reloadCount++
		return nil
	}
	mockPodman := &MockPodmanInterface{
		getPodmanInfoFunc: func() (*PodmanInfo, error) {
			return &PodmanInfo{Store: PodmanStorageConfig{
				GraphDriverName: "overlay",
				GraphRoot:       "/var/lib/containers/storage",
			}}, nil
		},
	}
	client := &RpmOstreeClient{podmanInterface: mockPodman}
	imageInfo := &PodmanImageInfo{ID: imageID, RepoDigest: repoDigest}

	require.NoError(t, client.patchPoliciesForContainerStorageWithPaths(imageInfo, paths))
	assert.Equal(t, 1, reloadCount)

	// The host policy is only read; the temporary exception must not weaken it.
	unchangedHostPolicy, err := os.ReadFile(paths.hostPolicyPath)
	require.NoError(t, err)
	assert.Equal(t, hostPolicy, unchangedHostPolicy)

	temporaryPolicy, err := os.ReadFile(paths.temporaryPolicyPath)
	require.NoError(t, err)
	var patched struct {
		Default []struct {
			Type string `json:"type"`
		} `json:"default"`
		Transports map[string]map[string][]struct {
			Type string `json:"type"`
		} `json:"transports"`
	}
	require.NoError(t, json.Unmarshal(temporaryPolicy, &patched))
	require.Len(t, patched.Default, 1)
	assert.Equal(t, "reject", patched.Default[0].Type)
	assert.Equal(t, "insecureAcceptAnything", patched.Transports["docker"]["quay.io"][0].Type)

	expectedKey := "[overlay@/var/lib/containers/storage]" + repoDigest + "@" + imageID
	storageRules := patched.Transports[imagePolicyTransportContainerStorage]
	require.Len(t, storageRules, 1)
	require.Contains(t, storageRules, expectedKey)
	assert.Equal(t, "insecureAcceptAnything", storageRules[expectedKey][0].Type)
	assert.NotContains(t, storageRules, "")

	dropin, err := os.ReadFile(paths.dropinPath)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("[Service]\nBindReadOnlyPaths=%s:%s", paths.temporaryPolicyPath, paths.hostPolicyPath), string(dropin))

	require.NoError(t, cleanupTemporalOstreePolicyFilesWithPaths(paths))
	assert.Equal(t, 2, reloadCount)
	_, err = os.Stat(paths.dropinPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(paths.temporaryPolicyPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestBuildTransportPolicyKey(t *testing.T) {
	key, err := buildTransportPolicyKey(
		&PodmanInfo{Store: PodmanStorageConfig{GraphDriverName: "overlay", GraphRoot: "/var/lib/containers/storage"}},
		"quay.io/example/os@sha256:repo",
		&PodmanImageInfo{RepoDigest: "quay.io/example/os@sha256:repo", ID: "sha256:image"},
	)
	require.NoError(t, err)
	assert.Equal(t, "[overlay@/var/lib/containers/storage]quay.io/example/os@sha256:repo@sha256:image", key)

	tagKey, err := buildTransportPolicyKey(
		&PodmanInfo{Store: PodmanStorageConfig{GraphDriverName: "overlay", GraphRoot: "/var/lib/containers/storage"}},
		"quay.io/example/os:stable",
		&PodmanImageInfo{ID: "sha256:image"},
	)
	require.NoError(t, err)
	assert.Equal(t, "[overlay@/var/lib/containers/storage]quay.io/example/os:stable@sha256:image", tagKey)

	tests := []struct {
		name      string
		podman    *PodmanInfo
		reference string
		image     *PodmanImageInfo
		errString string
	}{
		{name: "missing image metadata", podman: &PodmanInfo{}, reference: "repo", errString: "podman image information is required"},
		{name: "missing image reference", podman: &PodmanInfo{}, image: &PodmanImageInfo{ID: "id"}, errString: "image reference is required"},
		{name: "missing image ID", podman: &PodmanInfo{}, reference: "repo", image: &PodmanImageInfo{}, errString: "image ID is required"},
		{name: "missing storage metadata", reference: "repo", image: &PodmanImageInfo{ID: "id"}, errString: "storage information is required"},
		{name: "missing graph driver", podman: &PodmanInfo{Store: PodmanStorageConfig{GraphRoot: "/storage"}}, reference: "repo", image: &PodmanImageInfo{ID: "id"}, errString: "storage driver is required"},
		{name: "missing graph root", podman: &PodmanInfo{Store: PodmanStorageConfig{GraphDriverName: "overlay"}}, reference: "repo", image: &PodmanImageInfo{ID: "id"}, errString: "graph root is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildTransportPolicyKey(tt.podman, tt.reference, tt.image)
			assert.ErrorContains(t, err, tt.errString)
		})
	}
}

func TestLayeredImportPolicyMountArgs(t *testing.T) {
	assert.Empty(t, layeredImportPolicyMountArgs(""))
	assert.Equal(t,
		[]string{"-v", "/run/mco-layered-deploy-test/policy.json:" + imagePolicyFilePath + ":ro"},
		layeredImportPolicyMountArgs("/run/mco-layered-deploy-test/policy.json"),
	)
}

func TestNormalizeCalVerForSemver(t *testing.T) {
	assert.Equal(t, "2023.3.0", normalizeCalVerForSemver("2023.3"))
	assert.Equal(t, "2023.5.0", normalizeCalVerForSemver("2023.5"))
	// Already dotted-tri (or with more components): left untouched.
	assert.Equal(t, "2023.5.1", normalizeCalVerForSemver("2023.5.1"))
}
