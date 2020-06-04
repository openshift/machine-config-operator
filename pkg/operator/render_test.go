package operator

import (
	"fmt"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

func TestClusterDNSIP(t *testing.T) {
	tests := []struct {
		Range  string
		Output string
		Error  bool
	}{{
		Range:  "192.168.2.0/20",
		Output: "192.168.0.10",
	}, {
		Range:  "2001:db8::/32",
		Output: "2001:db8::a",
	}, {
		Range: "192.168.1.254/32",
		Error: true,
	}}
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			desc := fmt.Sprintf("clusterDNSIP(%#v)", test.Range)
			gotIP, err := clusterDNSIP(test.Range)
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed: %s", desc, err.Error())
				}
			}
			if gotIP != test.Output {
				t.Fatalf("%s failed: got = %s want = %s", desc, gotIP, test.Output)
			}
		})
	}
}

func TestIsSingleStackIPv6(t *testing.T) {
	tests := []struct {
		Ranges []string
		Output bool
		Error  bool
	}{{
		Ranges: []string{"192.168.2.0/20"},
		Output: false,
	}, {
		Ranges: []string{"2001:db8::/32"},
		Output: true,
	}, {
		Ranges: []string{"192.168.2.0/20", "2001:db8::/32"},
		Output: false,
	}, {
		Ranges: []string{"2001:db8::/32", "192.168.2.0/20"},
		Output: false,
	}, {
		Ranges: []string{"192.168.1.254/32"},
		Error:  true,
	}}
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			desc := fmt.Sprintf("isSingleStackIPv6(%#v)", test.Ranges)
			ipv6, err := isSingleStackIPv6(test.Ranges)
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed: %s", desc, err.Error())
				}
			}
			if ipv6 != test.Output {
				t.Fatalf("%s failed: got = %t want = %t", desc, ipv6, test.Output)
			}
		})
	}
}

func TestRenderAsset(t *testing.T) {
	tests := []struct {
		Path         string
		RenderConfig *renderConfig
		FindExpected string
		Error        bool
	}{{
		// Simple test
		Path: "manifests/machineconfigcontroller/clusterrolebinding.yaml",
		RenderConfig: &renderConfig{
			TargetNamespace: "testing-namespace",
		},
		FindExpected: "namespace: testing-namespace",
	}, {
		// Nested field test
		Path: "manifests/machineconfigcontroller/deployment.yaml",
		RenderConfig: &renderConfig{
			TargetNamespace: "testing-namespace",
			Images: &RenderConfigImages{
				MachineConfigOperator: "{MCO: PLACEHOLDER}",
			},
		},
		FindExpected: "image: {MCO: PLACEHOLDER}",
	}, {
		// Render same template as previous test
		// But with a template field missing
		Path: "manifests/machineconfigcontroller/deployment.yaml",
		RenderConfig: &renderConfig{
			TargetNamespace: "testing-namespace",
		},
		Error: true,
	}, {
		// Bad path, will cause asset error
		Path:  "BAD PATH",
		Error: true,
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			desc := fmt.Sprintf("Path(%#v), RenderConfig(%#v)", test.Path, test.RenderConfig)
			buf, err := renderAsset(test.RenderConfig, test.Path)
			// The template lib will throw an err if a template field is missing
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed: %s", desc, err.Error())
				} else {
					return
				}
			}
			if buf == nil || len(buf) == 0 {
				t.Fatalf("Buffer is empty")
			}
			// Verify that the buf can be converted back into a string safely
			str := fmt.Sprintf("%s", buf)
			if str == "" || len(str) == 0 {
				t.Fatalf("Buffer is not a valid string!")
			}
			// Verify that any FindExpected values are actually in the string
			if test.FindExpected != "" {
				if !strings.Contains(str, test.FindExpected) {
					t.Fatalf("Rendered template does not contain expected values: %s, \nGot: %s", test.FindExpected, str)
				}
			}
		})
	}
}

func TestCreateDiscoveredControllerConfigSpec(t *testing.T) {
	tests := []struct {
		Infra   *configv1.Infrastructure
		Network *configv1.Network
		Proxy   *configv1.Proxy
		Error   bool
	}{{
		Infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
				EtcdDiscoveryDomain: "tt.testing",
			}},
		Network: &configv1.Network{
			Spec: configv1.NetworkSpec{ServiceNetwork: []string{"192.168.1.1/24"}}},
		Proxy: &configv1.Proxy{
			Status: configv1.ProxyStatus{
				HTTPProxy: "test.proxy"}},
	}, {
		Infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
				EtcdDiscoveryDomain: "tt.testing",
			}},
		Network: &configv1.Network{
			Spec: configv1.NetworkSpec{ServiceNetwork: []string{"192.168.1.1/99999999"}}},
		Error: true,
	}, {
		Infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus:      &configv1.PlatformStatus{},
				EtcdDiscoveryDomain: "tt.testing",
			}},
		Network: &configv1.Network{
			Spec: configv1.NetworkSpec{ServiceNetwork: []string{"192.168.1.1/24"}}},
	}, {
		Infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
				EtcdDiscoveryDomain: "tt.testing",
			}},
		Network: &configv1.Network{
			Spec: configv1.NetworkSpec{ServiceNetwork: []string{}}},
		Error: true,
	}, {
		// Test old Infra.Status.Platform field instead of Infra.Status.PlatformStatus
		Infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				Platform:            configv1.AWSPlatformType,
				EtcdDiscoveryDomain: "tt.testing",
			},
		},
		Network: &configv1.Network{
			Spec: configv1.NetworkSpec{ServiceNetwork: []string{"192.168.1.1/24"}}},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			desc := fmt.Sprintf("Infra(%#v), Network(%#v)", test.Infra, test.Network)
			controllerConfigSpec, err := createDiscoveredControllerConfigSpec(test.Infra, test.Network, test.Proxy)
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed: %s", desc, err.Error())
				} else {
					// If Err flag is true and err is found, stop testing
					return
				}
			}
			if controllerConfigSpec == nil {
				t.Fatalf("Controller config spec did not get initialized")
			} else if controllerConfigSpec.Infra.Status.PlatformStatus.Type == "" {
				t.Fatalf("Error setting controller config platform")
			}
			etcdDomain := controllerConfigSpec.Infra.Status.EtcdDiscoveryDomain
			testDomain := test.Infra.Status.EtcdDiscoveryDomain
			if etcdDomain != testDomain {
				t.Fatalf("%s failed: got = %s want = %s", desc, etcdDomain, testDomain)
			}
			if test.Proxy != nil {
				testURL := test.Proxy.Status.HTTPProxy
				controllerURL := controllerConfigSpec.Proxy.HTTPProxy
				if controllerURL != testURL {
					t.Fatalf("%s failed: got = %s want = %s", desc, controllerURL, testURL)
				}
			}
		})
	}

}
