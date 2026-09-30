package template

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"text/template"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/require"
)

func TestIsSNOCoreDNSEnabledTemplateFunc(t *testing.T) {
	cc := newControllerConfig("test-cluster")
	cc.Spec.Infra.Status.PlatformStatus.Type = configv1.NonePlatformType
	cc.Spec.Infra.Status.ControlPlaneTopology = configv1.SingleReplicaTopologyMode
	on := ctrlcommon.NewFeatureGatesHardcodedHandler([]configv1.FeatureGateName{features.FeatureGateUnifiedClusterManagedDNSAndLB}, nil)
	off := ctrlcommon.NewFeatureGatesHardcodedHandler(nil, nil)
	for _, tc := range []struct {
		name    string
		config  *mcfgv1.ControllerConfigSpec
		handler ctrlcommon.FeatureGatesHandler
		want    bool
	}{
		{"enabled", &cc.Spec, on, true},
		{"disabled", &cc.Spec, off, false},
		{"no handler", &cc.Spec, nil, false},
		{"empty config", nil, on, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderTemplate(RenderConfig{
				ControllerConfigSpec: tc.config,
				FeatureGatesHandler:  tc.handler,
			}, tc.name, []byte(`{{if isSNOCoreDNSEnabled}}true{{else}}false{{end}}`))
			require.NoError(t, err)
			require.Equal(t, strconv.FormatBool(tc.want), string(got))
		})
	}
}

func TestSNOCoreDNSGate(t *testing.T) {
	cases := []struct {
		name     string
		enabled  bool
		platform configv1.PlatformType
		topology configv1.TopologyMode
		want     bool
	}{
		{"enabled None SNO", true, configv1.NonePlatformType, configv1.SingleReplicaTopologyMode, true},
		{"disabled None SNO", false, configv1.NonePlatformType, configv1.SingleReplicaTopologyMode, false},
		{"enabled non-None SNO", true, configv1.AWSPlatformType, configv1.SingleReplicaTopologyMode, false},
		{"disabled non-None SNO", false, configv1.AWSPlatformType, configv1.SingleReplicaTopologyMode, false},
		{"enabled None non-SNO", true, configv1.NonePlatformType, configv1.HighlyAvailableTopologyMode, false},
		{"disabled None non-SNO", false, configv1.NonePlatformType, configv1.HighlyAvailableTopologyMode, false},
		{"enabled non-None non-SNO", true, configv1.AWSPlatformType, configv1.HighlyAvailableTopologyMode, false},
		{"disabled non-None non-SNO", false, configv1.AWSPlatformType, configv1.HighlyAvailableTopologyMode, false},
	}

	for _, tc := range cases {
		for _, family := range []mcfgv1.IPFamiliesType{mcfgv1.IPFamiliesIPv4, mcfgv1.IPFamiliesIPv6, mcfgv1.IPFamiliesDualStack, mcfgv1.IPFamiliesDualStackIPv6Primary} {
			t.Run(tc.name+"/"+string(family), func(t *testing.T) {
				cc := newControllerConfig("test-cluster")
				cc.Spec.DNS = &configv1.DNS{Spec: configv1.DNSSpec{BaseDomain: "example.com"}}
				cc.Spec.Infra.Status.PlatformStatus.Type = tc.platform
				cc.Spec.Infra.Status.ControlPlaneTopology = tc.topology
				cc.Spec.IPFamilies = family

				enabled := []configv1.FeatureGateName{}
				if tc.enabled {
					enabled = append(enabled, features.FeatureGateUnifiedClusterManagedDNSAndLB)
				}
				fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler(enabled, nil)

				got, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy":"dummy"}`), nil, fgHandler)
				if err != nil {
					t.Fatal(err)
				}
				bootstrap, err := RunBootstrap(templateDir, cc, []byte(`{"dummy":"dummy"}`), nil, fgHandler)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, bootstrap) {
					t.Fatal("bootstrap output differs from controller output")
				}

				disabled, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy":"dummy"}`), nil, nil)
				require.NoError(t, err)
				baseline := map[string]*mcfgv1.MachineConfig{}
				for _, mc := range disabled {
					baseline[mc.Name] = mc
				}

				hasCoreDNS := false
				hasForcedDNSFix := false
				seenAssets := map[string]bool{}
				for _, mc := range got {
					if !tc.want || mc.Labels[mcfgv1.MachineConfigRoleLabelKey] != masterRole {
						require.Equal(t, baseline[mc.Name], mc, "MachineConfig changed outside enabled master configuration")
					}
					ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
					if err != nil {
						t.Fatal(err)
					}
					for _, file := range ignCfg.Storage.Files {
						contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
						if err != nil {
							t.Fatal(err)
						}
						if tc.want && (strings.Contains(string(contents), "--api-vip") || strings.Contains(string(contents), "--ingress-vip") || strings.Contains(string(contents), "keepalived") || strings.Contains(string(contents), "haproxy")) {
							t.Fatal("SNO CoreDNS assets contain VIP or load-balancer dependencies")
						}
						if tc.want && file.Path == "/etc/kubernetes/manifests/sno-coredns.yaml" {
							for _, expected := range []string{"corednsmonitor", "--discover-node-ip", "/run/NetworkManager"} {
								if !strings.Contains(string(contents), expected) {
									t.Fatalf("SNO CoreDNS monitor is missing %q", expected)
								}
							}
						}
						if file.Path == "/etc/kubernetes/manifests/sno-coredns.yaml" {
							hasCoreDNS = true
						}
						if file.Path == "/etc/NetworkManager/dispatcher.d/forcedns-rhel9-fix" {
							hasForcedDNSFix = true
						}
						if file.Path == "/etc/kubernetes/static-pod-resources/sno-coredns/Corefile.tmpl" {
							runtimeTemplate, err := template.New("Corefile").Parse(string(contents))
							if err != nil {
								t.Fatal(err)
							}
							data := struct {
								DNSAddresses []struct{ Address, RecordType string }
								DNSUpstreams []string
							}{
								DNSAddresses: []struct{ Address, RecordType string }{{"192.0.2.10", "A"}, {"2001:db8::10", "AAAA"}},
								DNSUpstreams: []string{"192.0.2.53"},
							}
							var rendered bytes.Buffer
							if err := runtimeTemplate.Execute(&rendered, data); err != nil {
								t.Fatal(err)
							}
							for _, expected := range []string{"IN A 192.0.2.10", "IN AAAA 2001:db8::10", "{{ .Name }}"} {
								if !strings.Contains(rendered.String(), expected) {
									t.Fatalf("rendered Corefile does not contain %q:\n%s", expected, rendered.String())
								}
							}
						}
						if strings.Contains(file.Path, "sno-coredns") {
							seenAssets[file.Path] = true
							if mc.Labels[mcfgv1.MachineConfigRoleLabelKey] != "master" {
								t.Fatalf("SNO DNS asset %s rendered for non-master role", file.Path)
							}
						}
					}
					for _, unit := range ignCfg.Systemd.Units {
						if unit.Name == "nodeip-configuration.service" {
							dualStack := family == mcfgv1.IPFamiliesDualStack || family == mcfgv1.IPFamiliesDualStackIPv6Primary
							require.NotNil(t, unit.Contents)
							require.Equal(t, dualStack, strings.Contains(*unit.Contents, "--dual-stack"))
							preferIPv6 := family == mcfgv1.IPFamiliesIPv6 || family == mcfgv1.IPFamiliesDualStackIPv6Primary
							require.Equal(t, preferIPv6, strings.Contains(*unit.Contents, "--prefer-ipv6"))
						}
					}
				}
				if hasCoreDNS != tc.want {
					t.Fatalf("CoreDNS rendered: got %t, want %t", hasCoreDNS, tc.want)
				}
				if want := tc.topology == configv1.SingleReplicaTopologyMode; hasForcedDNSFix != want {
					t.Fatalf("legacy forcedns fix rendered: got %t, want %t", hasForcedDNSFix, want)
				}
				wantAssets := map[string]bool{}
				if tc.want {
					wantAssets = map[string]bool{
						"/etc/kubernetes/manifests/sno-coredns.yaml":                     true,
						"/etc/kubernetes/static-pod-resources/sno-coredns/Corefile.tmpl": true,
					}
				}
				require.Equal(t, wantAssets, seenAssets, "rendered SNO DNS assets")
			})
		}
	}
}
