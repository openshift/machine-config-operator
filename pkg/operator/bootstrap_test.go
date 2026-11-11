package operator

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetPlatformManifests(t *testing.T) {
	cases := []struct {
		name             string
		platformName     string
		lbType           configv1.PlatformLoadBalancerType
		vipManagement    configv1.VIPManagementType
		expectKeepalived bool
		expectFRRK8s     bool
		expectCoredns    bool
		expectKubeVIPAPI bool
		dualStackVIPs    bool
		expectSecondary  bool
	}{
		{
			name:             "baremetal default LB, no BGP",
			platformName:     "baremetal",
			lbType:           configv1.LoadBalancerTypeOpenShiftManagedDefault,
			vipManagement:    "",
			expectKeepalived: true,
			expectFRRK8s:     false,
			expectCoredns:    true,
			expectKubeVIPAPI: false,
		},
		{
			name:             "baremetal default LB, BGP enabled",
			platformName:     "baremetal",
			lbType:           configv1.LoadBalancerTypeOpenShiftManagedDefault,
			vipManagement:    "BGP",
			expectKeepalived: false,
			expectFRRK8s:     true,
			expectCoredns:    true,
			expectKubeVIPAPI: true,
		},
		{
			name:             "baremetal user-managed LB, no BGP",
			platformName:     "baremetal",
			lbType:           configv1.LoadBalancerTypeUserManaged,
			vipManagement:    "",
			expectKeepalived: false,
			expectFRRK8s:     false,
			expectCoredns:    true,
			expectKubeVIPAPI: false,
		},
		{
			name:             "baremetal user-managed LB, BGP enabled",
			platformName:     "baremetal",
			lbType:           configv1.LoadBalancerTypeUserManaged,
			vipManagement:    "BGP",
			expectKeepalived: false,
			expectFRRK8s:     false,
			expectCoredns:    true,
			expectKubeVIPAPI: false,
		},
		{
			name:             "baremetal empty LB type, no BGP",
			platformName:     "baremetal",
			lbType:           "",
			vipManagement:    "",
			expectKeepalived: true,
			expectFRRK8s:     false,
			expectCoredns:    true,
			expectKubeVIPAPI: false,
		},
		{
			name:             "baremetal empty LB type, BGP enabled",
			platformName:     "baremetal",
			lbType:           "",
			vipManagement:    "BGP",
			expectKeepalived: false,
			expectFRRK8s:     true,
			expectCoredns:    true,
			expectKubeVIPAPI: true,
		},
		{
			name:             "baremetal BGP enabled, dual-stack VIPs",
			platformName:     "baremetal",
			lbType:           "",
			vipManagement:    "BGP",
			dualStackVIPs:    true,
			expectKeepalived: false,
			expectFRRK8s:     true,
			expectCoredns:    true,
			expectKubeVIPAPI: true,
			expectSecondary:  true,
		},
		{
			name:             "openstack default LB, no BGP",
			platformName:     "openstack",
			lbType:           configv1.LoadBalancerTypeOpenShiftManagedDefault,
			vipManagement:    "",
			expectKeepalived: true,
			expectFRRK8s:     false,
			expectCoredns:    true,
			expectKubeVIPAPI: false,
		},
		{
			name:             "gcp cloud platform",
			platformName:     "gcp",
			lbType:           configv1.LoadBalancerTypeUserManaged,
			vipManagement:    "",
			expectKeepalived: false,
			expectFRRK8s:     false,
			expectCoredns:    true,
			expectKubeVIPAPI: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := getPlatformManifests(nil, c.platformName, c.lbType, c.vipManagement, c.dualStackVIPs)

			hasKeepalived := false
			hasFRRK8s := false
			hasCoredns := false
			hasKubeVIPAPI := false
			hasSecondary := false
			for _, m := range result {
				if m.name == "manifests/on-prem/keepalived.yaml" {
					hasKeepalived = true
				}
				if m.name == "manifests/on-prem/0000-frr-k8s.yaml" {
					hasFRRK8s = true
				}
				if m.name == "manifests/on-prem/coredns.yaml" || m.name == "manifests/cloud-platform-alt-dns/coredns.yaml" {
					hasCoredns = true
				}
				if m.name == "manifests/on-prem/0010-kube-vip-api.yaml" {
					hasKubeVIPAPI = true
				}
				if m.name == "manifests/on-prem/0011-kube-vip-api-secondary.yaml" {
					hasSecondary = true
				}
			}

			assert.Equal(t, c.expectKeepalived, hasKeepalived, "keepalived manifest presence")
			assert.Equal(t, c.expectFRRK8s, hasFRRK8s, "frr-k8s manifest presence")
			assert.Equal(t, c.expectCoredns, hasCoredns, "coredns manifest presence")
			assert.Equal(t, c.expectKubeVIPAPI, hasKubeVIPAPI, "kube-vip-api manifest presence")
			assert.Equal(t, c.expectSecondary, hasSecondary, "kube-vip-api-secondary manifest presence")
		})
	}
}

func TestFillBGPVIPConfig(t *testing.T) {
	dir := t.TempDir()
	expectedJSON := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513}],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"]}`
	crYAML := `apiVersion: machineconfiguration.openshift.io/v1alpha1
kind: BGPVIPConfig
metadata:
  name: cluster
spec:
  localASN: 64512
  defaultPeers:
  - peerAddress: 192.168.111.1
    peerASN: 64513
`
	infra := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.BareMetalPlatformType,
				BareMetal: &configv1.BareMetalPlatformStatus{
					APIServerInternalIPs: []string{"192.168.111.5"},
					IngressIPs:           []string{"192.168.111.4"},
				},
			},
		},
	}
	path := filepath.Join(dir, "bgp-vip-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(crYAML), 0o644))

	deps := &BootstrapDependencies{Infrastructure: infra}
	require.NoError(t, deps.fillBGPVIPConfig(path), "fillBGPVIPConfig")
	assert.Equal(t, expectedJSON, deps.BGPVIPPeersJSON, "BGPVIPPeersJSON must match the Dev Preview payload schema")
	assert.Equal(t, expectedJSON, deps.BGPVIPPeersFileJSON, "without passwords the bootstrap peers file is the transport, byte for byte")

	// Missing file is tolerated (optional dependency).
	deps2 := &BootstrapDependencies{Infrastructure: infra}
	require.NoError(t, deps2.fillBGPVIPConfig(filepath.Join(dir, "nonexistent.yaml")), "missing file must not error")
	assert.Empty(t, deps2.BGPVIPPeersJSON, "expected empty for missing file")

	// A manifest that is not a BGPVIPConfig must be rejected.
	badPath := filepath.Join(dir, "bgp-vip-config-bad.yaml")
	require.NoError(t, os.WriteFile(badPath, []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: bgp-vip-config\n"), 0o644))
	deps3 := &BootstrapDependencies{Infrastructure: infra}
	assert.Error(t, deps3.fillBGPVIPConfig(badPath), "non-BGPVIPConfig manifest must error")
	assert.Empty(t, deps3.BGPVIPPeersJSON, "expected empty for wrong manifest kind")

	// Malformed YAML must be rejected.
	malformedPath := filepath.Join(dir, "bgp-vip-config-malformed.yaml")
	require.NoError(t, os.WriteFile(malformedPath, []byte("{not yaml"), 0o644))
	deps4 := &BootstrapDependencies{Infrastructure: infra}
	assert.Error(t, deps4.fillBGPVIPConfig(malformedPath), "malformed manifest must error")
	assert.Empty(t, deps4.BGPVIPPeersJSON, "expected empty for malformed manifest")
}

// TestFillBGPVIPConfigPasswordSecret locks the bootstrap file-based password
// handling: the ControllerConfig transport only references the Secret, and
// the bootstrap node's peers file resolves it from the installer-copied
// Secret manifest sitting next to the CR manifest.
func TestFillBGPVIPConfigPasswordSecret(t *testing.T) {
	crYAML := `apiVersion: machineconfiguration.openshift.io/v1alpha1
kind: BGPVIPConfig
metadata:
  name: cluster
spec:
  localASN: 64512
  defaultPeers:
  - peerAddress: 192.168.111.1
    peerASN: 64513
    passwordSecret:
      name: bgp-peer-pw
`
	secretYAML := `apiVersion: v1
kind: Secret
metadata:
  name: bgp-peer-pw
  namespace: openshift-config
type: kubernetes.io/basic-auth
stringData:
  password: s3cret
`
	infra := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.BareMetalPlatformType,
				BareMetal: &configv1.BareMetalPlatformStatus{
					APIServerInternalIPs: []string{"192.168.111.5"},
					IngressIPs:           []string{"192.168.111.4"},
				},
			},
		},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-bgp-vip-02-config.yml")
	require.NoError(t, os.WriteFile(path, []byte(crYAML), 0o644))
	// The Secret is found by namespace/name, not by file name; other
	// manifests in the directory (here: a same-named Secret in another
	// namespace, and a ConfigMap) are skipped.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "some-other-name.yaml"), []byte(secretYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-decoy.yaml"), []byte(strings.Replace(secretYAML, "namespace: openshift-config", "namespace: openshift-machine-config-operator", 1)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b-configmap.yaml"), []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: bgp-peer-pw\n  namespace: openshift-config\ndata:\n  password: nope\n"), 0o644))

	deps := &BootstrapDependencies{Infrastructure: infra}
	require.NoError(t, deps.fillBGPVIPConfig(path), "fillBGPVIPConfig")
	assert.Equal(t, simplePeersTransport, deps.BGPVIPPeersJSON, "the ControllerConfig transport only carries the Secret reference")
	assert.NotContains(t, deps.BGPVIPPeersJSON, "s3cret")
	assert.Equal(t, legacySimplePeersFile, deps.BGPVIPPeersFileJSON, "the bootstrap peers file must be the pre-change payload, byte for byte")

	// A referenced Secret whose manifest file is missing must error.
	dir2 := t.TempDir()
	path2 := filepath.Join(dir2, "bgp-vip-config.yaml")
	require.NoError(t, os.WriteFile(path2, []byte(crYAML), 0o644))
	deps2 := &BootstrapDependencies{Infrastructure: infra}
	err := deps2.fillBGPVIPConfig(path2)
	require.Error(t, err, "missing Secret manifest must error")
	assert.Contains(t, err.Error(), "openshift-config/bgp-peer-pw")
	assert.Contains(t, err.Error(), "defaultPeers[0]")
	assert.Empty(t, deps2.BGPVIPPeersJSON)
	assert.Empty(t, deps2.BGPVIPPeersFileJSON)

	// A Secret of the wrong type must be rejected.
	dir3 := t.TempDir()
	path3 := filepath.Join(dir3, "bgp-vip-config.yaml")
	require.NoError(t, os.WriteFile(path3, []byte(crYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir3, "bgp-vip-peer-secret-bgp-peer-pw.yaml"),
		[]byte("apiVersion: v1\nkind: Secret\nmetadata:\n  name: bgp-peer-pw\ntype: Opaque\nstringData:\n  password: s3cret\n"), 0o644))
	deps3 := &BootstrapDependencies{Infrastructure: infra}
	assert.Error(t, deps3.fillBGPVIPConfig(path3), "non-basic-auth Secret must error")
	assert.Empty(t, deps3.BGPVIPPeersJSON)
	assert.Empty(t, deps3.BGPVIPPeersFileJSON)
}

// TestBuildSpecBGPImageValidation locks the bootstrap hard-fail: rendering a
// BGP VIP management cluster without the frr-k8s/kube-vip images must error
// at render time rather than emit static pod manifests with empty images.
func TestBuildSpecBGPImageValidation(t *testing.T) {
	bgpInfra := &configv1.Infrastructure{
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.BareMetalPlatformType,
				BareMetal: &configv1.BareMetalPlatformStatus{
					VIPManagement:        configv1.VIPManagementTypeBGP,
					APIServerInternalIPs: []string{"192.168.111.5"},
					IngressIPs:           []string{"192.168.111.4"},
				},
			},
			EtcdDiscoveryDomain: "tt.testing",
		},
	}
	deps := func(infra *configv1.Infrastructure) *BootstrapDependencies {
		return &BootstrapDependencies{
			Infrastructure: infra,
			Network: &configv1.Network{
				Spec: configv1.NetworkSpec{ServiceNetwork: []string{"192.168.1.0/24"}}},
			DNS: &configv1.DNS{
				Spec: configv1.DNSSpec{BaseDomain: "tt.testing"}},
		}
	}
	imgs := &ctrlcommon.Images{}

	if _, err := buildSpec(deps(bgpInfra), imgs, ""); err == nil {
		t.Fatal("expected an error for BGP VIP management without frr-k8s/kube-vip images")
	}

	imgs.FRRK8s = "frr-k8s-image"
	imgs.KubeVip = "kube-vip-image"
	if _, err := buildSpec(deps(bgpInfra), imgs, ""); err != nil {
		t.Fatalf("unexpected error with images present: %v", err)
	}

	// VIP-less BGP falls back to keepalived; missing images must not fail.
	noVIPs := bgpInfra.DeepCopy()
	noVIPs.Status.PlatformStatus.BareMetal.APIServerInternalIPs = nil
	if _, err := buildSpec(deps(noVIPs), &ctrlcommon.Images{}, ""); err != nil {
		t.Fatalf("unexpected error for VIP-less BGP without images: %v", err)
	}
}

// TestBuildSpecBGPVIPPasswordOnlyInPeersFile locks where the resolved
// password may appear in the bootstrap output: in the bootstrap node's
// frr-peers.json only, never in the ControllerConfig manifest.
func TestBuildSpecBGPVIPPasswordOnlyInPeersFile(t *testing.T) {
	deps := &BootstrapDependencies{
		Infrastructure: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.BareMetalPlatformType,
					BareMetal: &configv1.BareMetalPlatformStatus{
						VIPManagement:        configv1.VIPManagementTypeBGP,
						APIServerInternalIPs: []string{"192.168.111.5"},
						IngressIPs:           []string{"192.168.111.4"},
					},
				},
				EtcdDiscoveryDomain: "tt.testing",
			},
		},
		Network:             &configv1.Network{Spec: configv1.NetworkSpec{ServiceNetwork: []string{"192.168.1.0/24"}}},
		DNS:                 &configv1.DNS{Spec: configv1.DNSSpec{BaseDomain: "tt.testing"}},
		BGPVIPPeersJSON:     simplePeersTransport,
		BGPVIPPeersFileJSON: legacySimplePeersFile,
	}
	imgs := &ctrlcommon.Images{}
	imgs.FRRK8s = "frr-k8s-image"
	imgs.KubeVip = "kube-vip-image"
	config, err := buildSpec(deps, imgs, "")
	require.NoError(t, err)

	cc, err := renderAsset(config, "manifests/machineconfigcontroller/controllerconfig.yaml")
	require.NoError(t, err)
	assert.NotContains(t, string(cc), "s3cret", "the bootstrap ControllerConfig must not carry the password")
	assert.Contains(t, string(cc), "passwordSecret")

	peersFile, err := renderAsset(config, "manifests/on-prem/frr-peers.json.tmpl")
	require.NoError(t, err)
	assert.Equal(t, legacySimplePeersFile, string(peersFile), "the bootstrap node's peers file is the pre-change payload, byte for byte")
}

// TestBootstrapFRRPeersFileMode locks the mode of the bootstrap node's
// frr-peers.json: 0600 like the MachineConfig-delivered file, while the other
// bootstrap assets keep the default mode.
func TestBootstrapFRRPeersFileMode(t *testing.T) {
	var peers, sibling *manifest
	for _, m := range getPlatformManifests(nil, "baremetal", configv1.LoadBalancerTypeOpenShiftManagedDefault, configv1.VIPManagementTypeBGP, false) {
		switch m.filename {
		case "baremetal/static-pod-resources/frr-k8s/frr-peers.json":
			peers = &m
		case "baremetal/static-pod-resources/frr-k8s/frr.conf.tmpl":
			sibling = &m
		}
	}
	require.NotNil(t, peers)
	require.NotNil(t, sibling)
	assert.Equal(t, os.FileMode(0o600), peers.mode)
	assert.Zero(t, sibling.mode, "other assets use the default mode")

	dir := t.TempDir()
	require.NoError(t, writeManifest(dir, *peers, []byte(legacySimplePeersFile)))
	require.NoError(t, writeManifest(dir, *sibling, []byte("frr")))
	info, err := os.Stat(filepath.Join(dir, peers.filename))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(filepath.Join(dir, peers.filename))
	require.NoError(t, err)
	assert.Equal(t, legacySimplePeersFile, string(data))
	info, err = os.Stat(filepath.Join(dir, sibling.filename))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644)&^currentUmask(t), info.Mode().Perm())

	// A pre-existing, looser file (a re-run into a used directory) is
	// tightened as well.
	require.NoError(t, os.Chmod(filepath.Join(dir, peers.filename), 0o644))
	require.NoError(t, writeManifest(dir, *peers, []byte(legacySimplePeersFile)))
	info, err = os.Stat(filepath.Join(dir, peers.filename))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// currentUmask reports the process umask, which os.WriteFile applies to the
// default mode.
func currentUmask(t *testing.T) os.FileMode {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe")
	require.NoError(t, os.WriteFile(path, nil, 0o777))
	info, err := os.Stat(path)
	require.NoError(t, err)
	return 0o777 &^ info.Mode().Perm()
}

// TestFillBGPVIPConfigRejectsUnsafePassword: a password Secret manifest that
// is not valid, printable UTF-8 fails the bootstrap render without echoing
// the password.
func TestFillBGPVIPConfigRejectsUnsafePassword(t *testing.T) {
	crYAML := "apiVersion: machineconfiguration.openshift.io/v1alpha1\nkind: BGPVIPConfig\nmetadata:\n  name: cluster\nspec:\n  localASN: 64512\n  defaultPeers:\n  - peerAddress: 192.168.111.1\n    peerASN: 64513\n    passwordSecret:\n      name: bgp-peer-pw\n"
	// base64 of "SYNTHETIC LEAK-\u0085-MARKER"
	secretYAML := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: bgp-peer-pw\n  namespace: openshift-config\ntype: kubernetes.io/basic-auth\ndata:\n  password: U1lOVEhFVElDIExFQUstwoUtTUFSS0VS\n"
	infra := &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{
		Type:      configv1.BareMetalPlatformType,
		BareMetal: &configv1.BareMetalPlatformStatus{APIServerInternalIPs: []string{"192.168.111.5"}, IngressIPs: []string{"192.168.111.4"}},
	}}}
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-bgp-vip-02-config.yml")
	require.NoError(t, os.WriteFile(path, []byte(crYAML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bgp-peer-pw-secret.yaml"), []byte(secretYAML), 0o644))

	deps := &BootstrapDependencies{Infrastructure: infra}
	err := deps.fillBGPVIPConfig(path)
	require.Error(t, err, "an unsafe password must fail the bootstrap render")
	assert.NotContains(t, err.Error(), "SYNTHETIC LEAK")
	assert.Empty(t, deps.BGPVIPPeersJSON)
	assert.Empty(t, deps.BGPVIPPeersFileJSON)
}

// TestWriteManifestErrorPathsKeepPrimaryError: when the write, or the chmod
// of an explicit mode, fails, writeManifest returns that error; the close
// result is joined to it rather than replacing or hiding it.
func TestWriteManifestErrorPathsKeepPrimaryError(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full is not available")
	}
	t.Run("write failure", func(t *testing.T) {
		// Writes to /dev/full fail with ENOSPC.
		err := writeManifest("/dev", manifest{filename: "full"}, []byte("data"))
		require.Error(t, err)
		assert.ErrorIs(t, err, syscall.ENOSPC)
	})
	t.Run("chmod failure", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("needs a non-root user, for whom changing /dev/full's mode fails")
		}
		err := writeManifest("/dev", manifest{filename: "full", mode: bgpVIPPeersFileMode}, []byte("data"))
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrPermission)
	})
}
