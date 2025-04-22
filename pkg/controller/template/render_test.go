package template

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"k8s.io/client-go/kubernetes/scheme"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func TestCloudProvider(t *testing.T) {
	dummyTemplate := []byte(`{{cloudProvider .}}`)

	cases := []struct {
		platform          configv1.PlatformType
		featureGateAccess featuregates.FeatureGateAccess
		res               string
	}{{
		platform: configv1.AWSPlatformType,
		res:      "external",
	}, {
		platform: configv1.OpenStackPlatformType,
		res:      "external",
	}, {
		platform: configv1.AzurePlatformType,
		res:      "external",
	}, {
		platform: configv1.GCPPlatformType,
		res:      "external",
	}, {
		platform: configv1.VSpherePlatformType,
		res:      "external",
	}, {
		platform: configv1.PowerVSPlatformType,
		res:      "external",
	}, {
		platform: configv1.NutanixPlatformType,
		res:      "external",
	}, {
		platform: configv1.BareMetalPlatformType,
		res:      "",
	}, {
		platform: configv1.LibvirtPlatformType,
		res:      "",
	}, {
		platform: configv1.KubevirtPlatformType,
		res:      "external",
	}, {
		platform: configv1.NonePlatformType,
		res:      "",
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Infra: &configv1.Infrastructure{
						Status: configv1.InfrastructureStatus{
							Platform: c.platform,
							PlatformStatus: &configv1.PlatformStatus{
								Type: c.platform,
							},
						},
					},
				},
			}

			got, err := renderTemplate(RenderConfig{&config.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, name, dummyTemplate)
			if err != nil {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.res {
				t.Fatalf("mismatch got: %s want: %s", got, c.res)
			}
		})
	}
}

func TestCredentialProviderConfigFlag(t *testing.T) {
	dummyTemplate := []byte(`{{credentialProviderConfigFlag .}}`)

	testCases := []struct {
		platform configv1.PlatformType
		res      string
	}{
		{
			platform: configv1.AWSPlatformType,
			res:      "--image-credential-provider-bin-dir=/usr/libexec/kubelet-image-credential-provider-plugins --image-credential-provider-config=/etc/kubernetes/credential-providers/ecr-credential-provider.yaml",
		},
		{
			platform: configv1.AzurePlatformType,
			res:      "--image-credential-provider-bin-dir=/usr/libexec/kubelet-image-credential-provider-plugins --image-credential-provider-config=/etc/kubernetes/credential-providers/acr-credential-provider.yaml",
		},
		{
			platform: configv1.GCPPlatformType,
			res:      "--image-credential-provider-bin-dir=/usr/libexec/kubelet-image-credential-provider-plugins --image-credential-provider-config=/etc/kubernetes/credential-providers/gcr-credential-provider.yaml",
		},
		{
			platform: configv1.VSpherePlatformType,
			res:      "",
		},
		{
			platform: configv1.OpenStackPlatformType,
			res:      "",
		},
		{
			platform: configv1.BareMetalPlatformType,
			res:      "",
		},
	}

	for idx, c := range testCases {
		name := fmt.Sprintf("case #%d: %s", idx, c.platform)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Infra: &configv1.Infrastructure{
						Status: configv1.InfrastructureStatus{
							Platform: c.platform,
							PlatformStatus: &configv1.PlatformStatus{
								Type: c.platform,
							},
						},
					},
				},
			}

			got, err := renderTemplate(RenderConfig{&config.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, name, dummyTemplate)
			if err != nil {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.res {
				t.Fatalf("mismatch got: %s want: %s", got, c.res)
			}
		})
	}
}

func TestSkipMissing(t *testing.T) {
	dummyTemplate := `{{skip "%s"}}`

	cases := []struct {
		key string
		err bool
		res string
	}{{
		key: "",
		err: true,
		res: "",
	}, {
		key: "2two",
		err: true,
		res: "",
	}, {
		key: "test index",
		err: true,
		res: "",
	}, {
		key: "index",
		err: false,
		res: "{{.index}}",
	},
	}

	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			tmpl := []byte(fmt.Sprintf(dummyTemplate, c.key))
			got, err := renderTemplate(RenderConfig{}, name, tmpl)
			if err != nil && !c.err {
				t.Fatalf("expected nil error %v", err)
			}

			if string(got) != c.res {
				t.Fatalf("mismatch got: %s want: %s", got, c.res)
			}
		})
	}
}

const templateDir = "../../../templates"

var (
	configs = map[string]string{
		"aws":                    "./test_data/controller_config_aws.yaml",
		"baremetal":              "./test_data/controller_config_baremetal.yaml",
		"baremetal-arbiter":      "./test_data/controller_config_baremetal_arbiter.yaml",
		"gcp":                    "./test_data/controller_config_gcp.yaml",
		"openstack":              "./test_data/controller_config_openstack.yaml",
		"libvirt":                "./test_data/controller_config_libvirt.yaml",
		"mtu-migration":          "./test_data/controller_config_mtu_migration.yaml",
		"none":                   "./test_data/controller_config_none.yaml",
		"external":               "./test_data/controller_config_external.yaml",
		"vsphere":                "./test_data/controller_config_vsphere.yaml",
		"kubevirt":               "./test_data/controller_config_kubevirt.yaml",
		"powervs":                "./test_data/controller_config_powervs.yaml",
		"nutanix":                "./test_data/controller_config_nutanix.yaml",
		"network-forwarding-sdn": "./test_data/controller_config_forwarding_sdn.yaml",
		"network-forwarding-ovn": "./test_data/controller_config_forwarding_ovn.yaml",
		"gcp-custom-dns":         "./test_data/controller_config_gcp_custom_dns.yaml",
		"gcp-default-dns":        "./test_data/controller_config_gcp_default_dns.yaml",
	}
)

func TestInvalidPlatform(t *testing.T) {
	controllerConfig, err := controllerConfigFromFile(configs["aws"])
	if err != nil {
		t.Fatalf("failed to get controllerconfig config: %v", err)
	}

	expectErr := func(err error, want string) {
		t.Helper()
		if err == nil {
			t.Fatalf("expect err %s, got nil", want)
		}
		if err.Error() != want {
			t.Fatalf("expect err %s, got %s", want, err.Error())
		}
	}

	// we must treat unrecognized constants as "none"
	controllerConfig.Spec.Infra.Status.PlatformStatus.Type = "_bad_"
	_, err = generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, templateDir)
	if err != nil {
		t.Errorf("expect nil error, got: %v", err)
	}

	// explicitly blocked
	controllerConfig.Spec.Infra.Status.PlatformStatus.Type = "_base"
	_, err = generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, templateDir)
	expectErr(err, "failed to create MachineConfig for role master: platform _base unsupported")
}

func TestGenerateMachineConfigs(t *testing.T) {
	for test, config := range configs {
		controllerConfig, err := controllerConfigFromFile(config)
		if err != nil {
			t.Fatalf("failed to get controllerconfig config: %v", err)
		}

		cfgs, err := generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, templateDir)
		if err != nil {
			t.Fatalf("failed to generate machine configs: %v", err)
		}

		foundPullSecretMaster := false
		foundPullSecretWorker := false
		foundKubeletUnitMaster := false
		foundKubeletUnitWorker := false
		foundMTUMigrationMaster := false
		foundMTUMigrationWorker := false
		foundIPForwarding := false

		for _, cfg := range cfgs {
			if cfg.Labels == nil {
				t.Fatal("non-nil labels expected")
			}

			role, ok := cfg.Labels[mcfgv1.MachineConfigRoleLabelKey]
			if !ok || role == "" {
				t.Fatal("role label missing")
			}

			ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
			if err != nil {
				t.Errorf("Failed to parse Ignition config for %s, %s, error: %v", config, cfg.Name, err)
			}
			if role == masterRole {
				if !foundPullSecretMaster {
					foundPullSecretMaster = findIgnFile(ign.Storage.Files, "/var/lib/kubelet/config.json", t)
				}
				if !foundKubeletUnitMaster {
					foundKubeletUnitMaster = findIgnUnit(ign.Systemd.Units, "kubelet.service", t)
				}
				if !foundMTUMigrationMaster {
					foundMTUMigrationMaster = findIgnFile(ign.Storage.Files, "/usr/local/bin/mtu-migration.sh", t)
					foundMTUMigrationMaster = foundMTUMigrationMaster || findIgnFile(ign.Storage.Files, "/etc/systemd/system/mtu-migration.service", t)
				}
			} else if role == workerRole {
				if !foundPullSecretWorker {
					foundPullSecretWorker = findIgnFile(ign.Storage.Files, "/var/lib/kubelet/config.json", t)
				}
				if !foundKubeletUnitWorker {
					foundKubeletUnitWorker = findIgnUnit(ign.Systemd.Units, "kubelet.service", t)
				}
				if !foundMTUMigrationWorker {
					foundMTUMigrationWorker = findIgnFile(ign.Storage.Files, "/usr/local/bin/mtu-migration.sh", t)
					foundMTUMigrationWorker = foundMTUMigrationWorker || findIgnFile(ign.Storage.Files, "/etc/systemd/system/mtu-migration.service", t)
				}
			} else if role == arbiterRole {
				// arbiter role currently follows master output
				if !foundPullSecretMaster {
					foundPullSecretMaster = findIgnFile(ign.Storage.Files, "/var/lib/kubelet/config.json", t)
				}
				if !foundKubeletUnitMaster {
					foundKubeletUnitMaster = findIgnUnit(ign.Systemd.Units, "kubelet.service", t)
				}
				if !foundMTUMigrationMaster {
					foundMTUMigrationMaster = findIgnFile(ign.Storage.Files, "/usr/local/bin/mtu-migration.sh", t)
					foundMTUMigrationMaster = foundMTUMigrationMaster || findIgnFile(ign.Storage.Files, "/etc/systemd/system/mtu-migration.service", t)
				}
			} else {
				t.Fatalf("Unknown role %s", role)
			}

			foundIPForwarding = foundIPForwarding || findIgnFile(ign.Storage.Files, "/etc/sysctl.d/forward.conf", t)
		}

		if !foundPullSecretMaster {
			t.Errorf("Failed to find pull secret for master")
		}
		if !foundKubeletUnitMaster {
			t.Errorf("Failed to find kubelet unit for master")
		}
		if !foundPullSecretWorker {
			t.Errorf("Failed to find pull secret for worker")
		}
		if !foundKubeletUnitWorker {
			t.Errorf("Failed to find kubelet unit for worker")
		}
		if foundIPForwarding && controllerConfig.Spec.NetworkType != "OpenShiftSDN" {
			t.Errorf("IP forwarding file mismatch. Was rendered: %t for network type: %s",
				foundIPForwarding, controllerConfig.Spec.NetworkType)
		}

		if test == "mtu-migration" {
			if !foundMTUMigrationMaster {
				t.Errorf("Failed to find mtu-migration files for master")
			}
			if !foundMTUMigrationWorker {
				t.Errorf("Failed to find mtu-migration files for worker")
			}
		} else {
			if foundMTUMigrationMaster {
				t.Errorf("Found mtu-migration files for master")
			}
			if foundMTUMigrationWorker {
				t.Errorf("Found mtu-migration files for worker")
			}
		}
	}
}

func TestGetPaths(t *testing.T) {
	APIIntLBIP := configv1.IP("10.10.10.4")
	APILBIP := configv1.IP("196.78.125.4")
	IngressLBIP1 := configv1.IP("196.78.125.5")
	IngressLBIP2 := configv1.IP("10.10.10.5")

	cases := []struct {
		platform configv1.PlatformType
		topology configv1.TopologyMode
		res      []string
	}{{
		platform: configv1.AWSPlatformType,
		res:      []string{strings.ToLower(string(configv1.AWSPlatformType))},
		topology: configv1.HighlyAvailableTopologyMode,
	}, {
		platform: configv1.BareMetalPlatformType,
		res:      []string{strings.ToLower(string(configv1.BareMetalPlatformType)), "on-prem"},
		topology: configv1.HighlyAvailableTopologyMode,
	}, {
		platform: configv1.GCPPlatformType,
		res:      []string{strings.ToLower(string(configv1.GCPPlatformType))},
		topology: configv1.HighlyAvailableTopologyMode,
	}, {

		platform: configv1.NonePlatformType,
		res:      []string{strings.ToLower(string(configv1.NonePlatformType)), sno},
		topology: configv1.SingleReplicaTopologyMode,
	}}
	for idx, c := range cases {
		name := fmt.Sprintf("case #%d", idx)
		t.Run(name, func(t *testing.T) {
			config := &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Infra: &configv1.Infrastructure{
						Status: configv1.InfrastructureStatus{
							Platform: c.platform,
							PlatformStatus: &configv1.PlatformStatus{
								Type: c.platform,
							},
							ControlPlaneTopology: c.topology,
						},
					},
				},
			}
			if c.platform == configv1.GCPPlatformType {
				config = &mcfgv1.ControllerConfig{
					Spec: mcfgv1.ControllerConfigSpec{
						Infra: &configv1.Infrastructure{
							Status: configv1.InfrastructureStatus{
								Platform: c.platform,
								PlatformStatus: &configv1.PlatformStatus{
									Type: c.platform,
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
								ControlPlaneTopology: c.topology,
							},
						},
					},
				}
			}
			c.res = append(c.res, platformBase)

			got := getPaths(&RenderConfig{&config.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, config.Spec.Platform)
			if reflect.DeepEqual(got, c.res) {
				t.Fatalf("mismatch got: %s want: %s", got, c.res)
			}
		})
	}
}

func controllerConfigFromFile(path string) (*mcfgv1.ControllerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cci, _, err := scheme.Codecs.UniversalDecoder().Decode(data, nil, &mcfgv1.ControllerConfig{})
	if err != nil {
		return nil, fmt.Errorf("unable to decode ControllerConfig manifest: %w", err)
	}
	cc, ok := cci.(*mcfgv1.ControllerConfig)
	if !ok {
		return nil, fmt.Errorf("expected manifest to decode into *mcfgv1.ControllerConfig, got %T", cci)
	}
	return cc, nil
}

func findIgnFile(files []ign3types.File, path string, t *testing.T) bool {
	for _, f := range files {
		if f.Path == path {
			return true
		}
	}
	return false
}

func findIgnUnit(units []ign3types.Unit, name string, t *testing.T) bool {
	for _, u := range units {
		if u.Name == name {
			return true
		}
	}
	return false
}

func verifyIgn(actual [][]byte, dir string, t *testing.T) {

	expected := make(map[string][]byte)
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path != dir {
				return fmt.Errorf("unexpected dir: %w", err)
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		expected[path] = data
		return nil
	}); err != nil {
		t.Fatalf("failed to walk dir: %v", err)
	}

	for _, a := range actual {
		var found bool
		for key, d := range expected {
			if bytes.Equal(d, a) {
				found = true
				delete(expected, key)
			}
		}

		if !found {
			t.Errorf("can't find actual file %v:\n%v", dir, string(a))
		}
	}

	for key := range expected {
		t.Errorf("can't find expected file:\n%v", key)
	}
}
