package operator

import (
	"os"
	"path/filepath"
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
			result := getPlatformManifests(nil, c.platformName, c.lbType, c.vipManagement)

			hasKeepalived := false
			hasFRRK8s := false
			hasCoredns := false
			hasKubeVIPAPI := false
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
			}

			assert.Equal(t, c.expectKeepalived, hasKeepalived, "keepalived manifest presence")
			assert.Equal(t, c.expectFRRK8s, hasFRRK8s, "frr-k8s manifest presence")
			assert.Equal(t, c.expectCoredns, hasCoredns, "coredns manifest presence")
			assert.Equal(t, c.expectKubeVIPAPI, hasKubeVIPAPI, "kube-vip-api manifest presence")
		})
	}
}

func TestFillBGPVIPConfig(t *testing.T) {
	dir := t.TempDir()
	peersJSON := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513}],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"]}`
	cmYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: bgp-vip-config
  namespace: openshift-network-operator
data:
  config.json: '` + peersJSON + `'
`
	path := filepath.Join(dir, "bgp-vip-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cmYAML), 0o644))

	deps := &BootstrapDependencies{}
	require.NoError(t, deps.fillBGPVIPConfig(path), "fillBGPVIPConfig")
	assert.Equal(t, peersJSON, deps.BGPVIPPeersJSON, "BGPVIPPeersJSON must be the compacted config.json payload")

	// Missing file is tolerated (optional dependency).
	deps2 := &BootstrapDependencies{}
	require.NoError(t, deps2.fillBGPVIPConfig(filepath.Join(dir, "nonexistent.yaml")), "missing file must not error")
	assert.Empty(t, deps2.BGPVIPPeersJSON, "expected empty for missing file")

	// Malformed config.json payload must be rejected.
	badCMYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: bgp-vip-config
  namespace: openshift-network-operator
data:
  config.json: '{not json'
`
	badPath := filepath.Join(dir, "bgp-vip-config-bad.yaml")
	require.NoError(t, os.WriteFile(badPath, []byte(badCMYAML), 0o644))

	deps3 := &BootstrapDependencies{}
	assert.Error(t, deps3.fillBGPVIPConfig(badPath), "malformed config.json must error")
	assert.Empty(t, deps3.BGPVIPPeersJSON, "expected empty for malformed config.json")

	// A present ConfigMap without a config.json payload must be rejected
	// (mirrors the day-2 sync behavior).
	emptyCMYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: bgp-vip-config
  namespace: openshift-network-operator
data: {}
`
	emptyPath := filepath.Join(dir, "bgp-vip-config-empty.yaml")
	require.NoError(t, os.WriteFile(emptyPath, []byte(emptyCMYAML), 0o644))

	deps4 := &BootstrapDependencies{}
	assert.Error(t, deps4.fillBGPVIPConfig(emptyPath), "empty config.json payload must error")
	assert.Empty(t, deps4.BGPVIPPeersJSON, "expected empty for empty config.json")
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
