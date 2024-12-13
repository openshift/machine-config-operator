package operator

import (
	"fmt"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	mcfgv1resourceread "github.com/openshift/machine-config-operator/lib/resourceread"
	"github.com/openshift/machine-config-operator/manifests"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestClusterDNSIP(t *testing.T) {
	t.Parallel()
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
		idx := idx
		test := test
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			t.Parallel()
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

func TestIPFamilies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Ranges []string
		Output mcfgv1.IPFamiliesType
		Error  bool
	}{{
		Ranges: []string{"192.168.2.0/20"},
		Output: mcfgv1.IPFamiliesIPv4,
	}, {
		Ranges: []string{"2001:db8::/32"},
		Output: mcfgv1.IPFamiliesIPv6,
	}, {
		Ranges: []string{"192.168.2.0/20", "2001:db8::/32"},
		Output: mcfgv1.IPFamiliesDualStack,
	}, {
		Ranges: []string{"2001:db8::/32", "192.168.2.0/20"},
		Output: mcfgv1.IPFamiliesDualStackIPv6Primary,
	}, {
		Ranges: []string{},
		Error:  true,
	}}
	for idx, test := range tests {
		idx := idx
		test := test
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			t.Parallel()
			desc := fmt.Sprintf("ipFamilies(%#v)", test.Ranges)
			families, err := ipFamilies(test.Ranges)
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed: %s", desc, err.Error())
				}
			}
			if families != test.Output {
				t.Fatalf("%s failed: got = %s want = %s", desc, families, test.Output)
			}
		})
	}
}

// Smoke test to render all manifests to validate if they can be rendered or
// not and if they can be read into the appropriate structs.
// TODO: Consolidate this and the TestRenderAsset tests
func TestRenderAllManifests(t *testing.T) {
	t.Parallel()

	allManifests, err := manifests.AllManifests()
	require.NoError(t, err)

	renderConfig := &renderConfig{
		TargetNamespace: "testing-namespace",
		Images: &ctrlcommon.RenderConfigImages{
			MachineConfigOperator: "mco-operator-image",
			KubeRbacProxy:         "kube-rbac-proxy-image",
			KeepalivedBootstrap:   "keepalived-bootstrap-image",
		},
		ControllerConfig: mcfgv1.ControllerConfigSpec{
			DNS: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "local",
				},
			},
			Proxy: &configv1.ProxyStatus{
				HTTPSProxy: "https://i.am.a.proxy.server",
				NoProxy:    "*",
			},
			Infra: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{
								"1.1.1.1",
							},
						},
						Type: configv1.BareMetalPlatformType,
					},
				},
			},
		},
	}

	// Some of the templates accept a different schema type that doesn't have a
	// known type, but contains elements of the above renderConfig.
	untypedRenderConfig := struct {
		Role             string
		PointerConfig    string
		ControllerConfig mcfgv1.ControllerConfigSpec
		Images           *ctrlcommon.RenderConfigImages
	}{
		Role:             "control-plane",
		PointerConfig:    "cG9pbnRlci1jb25maWctZGF0YQo=", // This must be Base64-encoded
		ControllerConfig: renderConfig.ControllerConfig,
		Images:           renderConfig.Images,
	}

	// These manifest templates are rendered with the untypedRenderConfig above.
	untypedCases := sets.NewString(
		"userdata_secret.yaml",
		"on-prem/coredns.yaml",
		"on-prem/keepalived.yaml",
		"on-prem/coredns-corefile.tmpl")

	// These are files in the manifest directory that we should ignore in this test.
	ignored := sets.NewString("manifests.go")
	ignored = ignored.Insert("cloud-platform-alt-dns/coredns.yaml")
	ignored = ignored.Insert("cloud-platform-alt-dns/coredns-corefile.tmpl")

	for _, manifestPath := range allManifests {
		manifestPath := manifestPath
		// Skip files that we should ignore for this test.
		if ignored.Has(manifestPath) {
			continue
		}

		t.Run(manifestPath, func(t *testing.T) {
			t.Parallel()

			var buf []byte
			var err error
			var desc string

			// Determine if this manifest is rendered with the renderConfig or the untypedConfig.
			if untypedCases.Has(manifestPath) {
				desc = fmt.Sprintf("Path(%#v), UntypedRenderData(%#v)", manifestPath, untypedRenderConfig)
				buf, err = renderUntypedAsset(untypedRenderConfig, manifestPath)
			} else {
				desc = fmt.Sprintf("Path(%#v), RenderConfig(%#v)", manifestPath, renderConfig)
				buf, err = renderAsset(renderConfig, manifestPath)
			}

			// The template lib will throw an err if a template field is missing
			if err != nil {
				t.Logf("%s failed: %s", desc, err.Error())
				t.Fail()
			}
			if buf == nil || len(buf) == 0 {
				t.Log("Buffer is empty!")
				t.Fail()
			}
			// Verify that the buf can be converted back into a string safely
			str := fmt.Sprintf("%s", buf)
			if str == "" || len(str) == 0 {
				t.Log("Buffer is not a valid string!")
				t.Fail()
			}

			// If we have a .yaml extension, we have a kube manifest. We should
			// ensure that it can be read into the appropriate data structure to
			// ensure there are no rendering errors.
			if strings.HasSuffix(manifestPath, ".yaml") {
				assertRenderedCanBeRead(t, buf)
			}
		})
	}
}

func TestRenderAsset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Path         string
		RenderConfig *renderConfig
		// Substrings expected to be present in the rendered template.
		FindExpected []string
		// Substrings expected not to be present in the rendered template.
		NotFindExpected []string
		Error           bool
	}{
		{
			// Simple test
			Path: "manifests/machineconfigcontroller/clusterrolebinding.yaml",
			RenderConfig: &renderConfig{
				TargetNamespace: "testing-namespace",
			},
			FindExpected: []string{"namespace: testing-namespace"},
		},
		{
			// Nested field test
			Path: "manifests/machineconfigcontroller/deployment.yaml",
			RenderConfig: &renderConfig{
				TargetNamespace: "testing-namespace",
				ReleaseVersion:  "4.8.0-rc.0",
				Images: &ctrlcommon.RenderConfigImages{
					MachineConfigOperator: "mco-operator-image",
				},
			},
			FindExpected: []string{
				"image: mco-operator-image",
				"--payload-version=4.8.0-rc.0",
			},
		},
		{
			// Render same template as previous test
			// But with a template field missing
			Path: "manifests/machineconfigcontroller/deployment.yaml",
			RenderConfig: &renderConfig{
				TargetNamespace: "testing-namespace",
			},
			Error: true,
		},
		{
			// Test that machineconfigdaemon DaemonSets are rendered correctly with proxy config
			Path: "manifests/machineconfigdaemon/daemonset.yaml",
			RenderConfig: &renderConfig{
				TargetNamespace:  "testing-namespace",
				ReleaseVersion:   "4.8.0-rc.0",
				Images: &ctrlcommon.RenderConfigImages{
					MachineConfigOperator: "mco-operator-image",
					KubeRbacProxy:         "kube-rbac-proxy-image",
				},
				ControllerConfig: mcfgv1.ControllerConfigSpec{
					Proxy: &configv1.ProxyStatus{
						HTTPSProxy: "https://i.am.a.proxy.server",
						NoProxy:    "*", // See: https://bugzilla.redhat.com/show_bug.cgi?id=1947066
					},
				},
			},
			FindExpected: []string{
				"image: mco-operator-image",
				"image: kube-rbac-proxy-image",
				"- name: HTTPS_PROXY\n            value: https://i.am.a.proxy.server",
				"- name: NO_PROXY\n            value: \"*\"", // Ensure the * is quoted: "*": https://bugzilla.redhat.com/show_bug.cgi?id=1947066
				"--payload-version=4.8.0-rc.0",
			},
			NotFindExpected: []string{
				"- mountPath: /run/pool-1/secret-1\n            name: secret-1",
				"- mountPath: /run/pool-2/secret-2\n            name: secret-2",
				"- secret:\n            secretName: secret-1\n          name: secret-1",
				"- secret:\n            secretName: secret-2\n          name: secret-2",
			},
		},
		{
			// Bad path, will cause asset error
			Path:  "BAD PATH",
			Error: true,
		},
		{
			Path: "manifests/bootstrap-pod-v2.yaml",
			RenderConfig: &renderConfig{
				TargetNamespace: "testing-namespace",
				ReleaseVersion:  "4.8.0-rc.0",
				Images: &ctrlcommon.RenderConfigImages{
					MachineConfigOperator: "mco-operator-image",
				},
			},
			FindExpected: []string{
				"--payload-version=4.8.0-rc.0",
			},
		},
		// Tests that the MCD DaemonSet gets MachineOSConfig secrets mounted into it.
		/* TODO Remove this test
		{
			Path: "manifests/machineconfigdaemon/daemonset.yaml",
			RenderConfig: &renderConfig{
				TargetNamespace: "testing-namespace",
				ReleaseVersion:  "4.16.0-rc.1",
				Images: &ctrlcommon.RenderConfigImages{
					MachineConfigOperator: "mco-operator-image",
					KubeRbacProxy:         "kube-rbac-proxy-image",
				},
				MachineOSConfigs: []*mcfgv1.MachineOSConfig{
					{
						Spec: mcfgv1.MachineOSConfigSpec{
							MachineConfigPool: mcfgv1.MachineConfigPoolReference{
								Name: "pool-1",
							},
							BuildOutputs: mcfgv1.BuildOutputs{
								CurrentImagePullSecret: mcfgv1.ImageSecretObjectReference{
									Name: "secret-1",
								},
							},
						},
					},
					{
						Spec: mcfgv1.MachineOSConfigSpec{
							MachineConfigPool: mcfgv1.MachineConfigPoolReference{
								Name: "pool-2",
							},
							BuildOutputs: mcfgv1.BuildOutputs{
								CurrentImagePullSecret: mcfgv1.ImageSecretObjectReference{
									Name: "secret-2",
								},
							},
						},
					},
				},
			},
			FindExpected: []string{
				"- mountPath: /run/secrets/os-image-pull-secrets/pool-1\n            name: secret-1",
				"- mountPath: /run/secrets/os-image-pull-secrets/pool-2\n            name: secret-2",
				"- secret:\n            secretName: secret-1\n          name: secret-1",
				"- secret:\n            secretName: secret-2\n          name: secret-2",
			},
		},
		*/
	}

	for idx, test := range tests {
		idx := idx
		test := test

		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			t.Parallel()
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

			// Verify that all FindExpected values are actually in the rendered string
			for _, itemToFind := range test.FindExpected {
				assert.Contains(t, str, itemToFind)
			}

			// Verify that none of the NotFindExpected values are in the rendered string
			for _, itemNotToFind := range test.NotFindExpected {
				assert.NotContains(t, str, itemNotToFind)
			}

			// Verify that the rendered template can be read.
			// This aims to prevent bugs similar to https://bugzilla.redhat.com/show_bug.cgi?id=1947066
			assertRenderedCanBeRead(t, buf)
		})
	}
}

func TestCreateDiscoveredControllerConfigSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Infra   *configv1.Infrastructure
		Network *configv1.Network
		Proxy   *configv1.Proxy
		DNS     *configv1.DNS
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
		DNS: &configv1.DNS{
			Spec: configv1.DNSSpec{BaseDomain: "tt.testing"}},
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
		DNS: &configv1.DNS{
			Spec: configv1.DNSSpec{BaseDomain: "tt.testing"}},
		Error: true,
	}, {
		Infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus:      &configv1.PlatformStatus{},
				EtcdDiscoveryDomain: "tt.testing",
			}},
		Network: &configv1.Network{
			Spec: configv1.NetworkSpec{ServiceNetwork: []string{"192.168.1.1/24"}}},
		DNS: &configv1.DNS{
			Spec: configv1.DNSSpec{BaseDomain: "tt.testing"}},
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
		DNS: &configv1.DNS{
			Spec: configv1.DNSSpec{BaseDomain: "tt.testing"}},
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
		DNS: &configv1.DNS{
			Spec: configv1.DNSSpec{BaseDomain: "tt.testing"}},
	}}

	for idx, test := range tests {
		idx := idx
		test := test
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			t.Parallel()
			desc := fmt.Sprintf("Infra(%#v), Network(%#v)", test.Infra, test.Network)
			controllerConfigSpec, err := createDiscoveredControllerConfigSpec(test.Infra, test.Network, test.Proxy, test.DNS)
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

func assertRenderedCanBeRead(t *testing.T, rendered []byte) {
	assert.NotPanics(t, func() {
		// Do a trial decoding into an untyped struct so we can look up the object
		// kind. There is probably a more efficient and straightforward way to do
		// this that I am unaware of.
		initialDecoded := map[string]interface{}{}
		require.NoError(t, yaml.Unmarshal(rendered, &initialDecoded))
		kind := initialDecoded["kind"].(string)

		switch kind {
		case "ClusterRole":
			resourceread.ReadClusterRoleV1OrDie(rendered)
		case "ClusterRoleBinding":
			resourceread.ReadClusterRoleBindingV1OrDie(rendered)
		case "ConfigMap":
			resourceread.ReadConfigMapV1OrDie(rendered)
		case "ControllerConfig":
			mcfgv1resourceread.ReadControllerConfigV1OrDie(rendered)
		case "CustomResourceDefinition":
			resourceread.ReadCustomResourceDefinitionV1OrDie(rendered)
		case "DaemonSet":
			resourceread.ReadDaemonSetV1OrDie(rendered)
		case "Deployment":
			resourceread.ReadDeploymentV1OrDie(rendered)
		case "MachineConfigPool":
			mcfgv1resourceread.ReadMachineConfigPoolV1OrDie(rendered)
		case "Pod":
			resourceread.ReadPodV1OrDie(rendered)
		case "RoleBinding":
			resourceread.ReadRoleBindingV1OrDie(rendered)
		case "Secret":
			resourceread.ReadSecretV1OrDie(rendered)
		case "ServiceAccount":
			resourceread.ReadServiceAccountV1OrDie(rendered)
		}
	})
}

// Renders an asset which takes an untyped render config.
func renderUntypedAsset(data interface{}, path string) ([]byte, error) {
	ar := newAssetRenderer(path)

	if err := ar.read(); err != nil {
		return nil, err
	}

	ar.addTemplateFuncs()

	return ar.render(data)
}

func TestRenderCloudAltDNSManifests(t *testing.T) {
	t.Parallel()

	allManifests, err := manifests.AllManifests()
	require.NoError(t, err)

	APIIntLBIP := configv1.IP("10.10.10.4")
	APILBIP := configv1.IP("196.78.125.4")
	IngressLBIP1 := configv1.IP("196.78.125.5")
	IngressLBIP2 := configv1.IP("10.10.10.5")
	renderConfig := &renderConfig{
		TargetNamespace: "testing-namespace",
		Images: &ctrlcommon.RenderConfigImages{
			MachineConfigOperator: "mco-operator-image",
			KubeRbacProxy:         "kube-rbac-proxy-image",
			KeepalivedBootstrap:   "keepalived-bootstrap-image",
		},
		ControllerConfig: mcfgv1.ControllerConfigSpec{
			DNS: &configv1.DNS{
				Spec: configv1.DNSSpec{
					BaseDomain: "local",
				},
			},
			Proxy: &configv1.ProxyStatus{
				HTTPSProxy: "https://i.am.a.proxy.server",
				NoProxy:    "*",
			},
			Infra: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.GCPPlatformType,
						GCP: &configv1.GCPPlatformStatus{
							CloudLoadBalancerConfig: &configv1.CloudLoadBalancerConfig{
								DNSType: configv1.ClusterHostedDNSType,
								ClusterHosted: &configv1.CloudLoadBalancerIPs{
									APIIntLoadBalancerIPs: []configv1.IP{
										APIIntLBIP,
									},
									APILoadBalancerIPs: []configv1.IP{
										APILBIP,
									},
									IngressLoadBalancerIPs: []configv1.IP{
										IngressLBIP1,
										IngressLBIP2,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Some of the templates accept a different schema type that doesn't have a
	// known type, but contains elements of the above renderConfig.
	untypedRenderConfig := struct {
		Role             string
		PointerConfig    string
		ControllerConfig mcfgv1.ControllerConfigSpec
		Images           *ctrlcommon.RenderConfigImages
	}{
		Role:             "control-plane",
		PointerConfig:    "cG9pbnRlci1jb25maWctZGF0YQo=", // This must be Base64-encoded
		ControllerConfig: renderConfig.ControllerConfig,
		Images:           renderConfig.Images,
	}

	// These manifest templates are rendered with the untypedRenderConfig above.
	untypedCases := sets.NewString(
		"cloud-platform-alt-dns/coredns.yaml",
		"cloud-platform-alt-dns/coredns-corefile.tmpl")

	// These are files in the manifest directory that we should ignore in this test.
	ignored := sets.NewString("manifests.go")

	for _, manifestPath := range allManifests {
		manifestPath := manifestPath
		// Skip files that we should ignore for this test.
		if ignored.Has(manifestPath) {
			continue
		}

		// Skip if this is not a manifest for alternate DNS for cloud config
		if !strings.Contains(manifestPath, "cloud-platform-alt-dns") {
			continue
		}

		t.Run(manifestPath, func(t *testing.T) {
			t.Parallel()

			var buf []byte
			var err error
			var desc string

			// Determine if this manifest is rendered with the renderConfig or the untypedConfig.
			if untypedCases.Has(manifestPath) {
				desc = fmt.Sprintf("Path(%#v), UntypedRenderData(%#v)", manifestPath, untypedRenderConfig)
				buf, err = renderUntypedAsset(untypedRenderConfig, manifestPath)
			} else {
				desc = fmt.Sprintf("Path(%#v), RenderConfig(%#v)", manifestPath, renderConfig)
				buf, err = renderAsset(renderConfig, manifestPath)
			}

			// The template lib will throw an err if a template field is missing
			if err != nil {
				t.Logf("%s failed: %s", desc, err.Error())
				t.Fail()
			}
			if buf == nil || len(buf) == 0 {
				t.Log("Buffer is empty!")
				t.Fail()
			}
			// Verify that the buf can be converted back into a string safely
			str := fmt.Sprintf("%s", buf)
			if str == "" || len(str) == 0 {
				t.Log("Buffer is not a valid string!")
				t.Fail()
			}

			t.Logf("%s was rendered to be : %s", manifestPath, buf)

			// If we have a .yaml extension, we have a kube manifest. We should
			// ensure that it can be read into the appropriate data structure to
			// ensure there are no rendering errors.
			if strings.HasSuffix(manifestPath, ".yaml") {
				assertRenderedCanBeRead(t, buf)
			}
		})
	}
}
