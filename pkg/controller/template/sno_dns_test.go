package template

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
						if strings.Contains(file.Path, "sno-coredns") || file.Path == "/usr/local/bin/sno-resolv-prepender.sh" {
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
						if unit.Name == "sno-resolv-prepender.service" {
							if unit.Contents == nil || !strings.Contains(*unit.Contents, "ExecStart=/usr/local/bin/sno-resolv-prepender.sh") || !strings.Contains(*unit.Contents, "Restart=on-failure") {
								t.Fatal("SNO DNS resolver unit does not reconcile the host resolver")
							}
							seenAssets[unit.Name] = true
							if mc.Labels[mcfgv1.MachineConfigRoleLabelKey] != "master" {
								t.Fatal("SNO DNS resolver unit rendered for non-master role")
							}
						}
						if unit.Name == "sno-resolv-prepender.path" {
							if unit.Contents == nil || !strings.Contains(*unit.Contents, "PathChanged=/run/NetworkManager") || !strings.Contains(*unit.Contents, "PathChanged=/etc/kubernetes/manifests/sno-coredns.yaml") {
								t.Fatal("SNO DNS resolver does not refresh after CoreDNS or NetworkManager changes")
							}
							seenAssets[unit.Name] = true
							if mc.Labels[mcfgv1.MachineConfigRoleLabelKey] != "master" {
								t.Fatal("SNO DNS path unit rendered for non-master role")
							}
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
						"/usr/local/bin/sno-resolv-prepender.sh":                         true,
						"sno-resolv-prepender.service":                                   true,
						"sno-resolv-prepender.path":                                      true,
					}
				}
				require.Equal(t, wantAssets, seenAssets, "rendered SNO DNS assets")
			})
		}
	}
}

func TestSNOCoreDNSResolverPlainFile(t *testing.T) {
	script := renderSNODNSResolver(t)
	testDir := t.TempDir()
	paths := prepareResolverTest(t, testDir, script)

	original := "nameserver 198.51.100.53\noptions timeout:2\n"
	writeTestFile(t, paths.resolvConf, original, 0o644)
	writeTestFile(t, paths.nmResolv, "nameserver 203.0.113.53\nsearch example.net\noptions attempts:2\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "192.0.2.10\n", 0o644)
	writeExecutable(t, filepath.Join(paths.binDir, "curl"), "#!/bin/sh\nexit 1\n")
	if _, err := resolverCommand(paths).CombinedOutput(); err == nil {
		t.Fatal("resolver did not wait for CoreDNS readiness")
	}
	if got := readTestFile(t, paths.resolvConf); got != original {
		t.Fatalf("resolver changed before CoreDNS became ready: got %q, want %q", got, original)
	}
	if _, err := os.Stat(paths.nmDropin); !os.IsNotExist(err) {
		t.Fatalf("resolver ownership changed before CoreDNS became ready: %v", err)
	}

	writeExecutable(t, filepath.Join(paths.binDir, "curl"), "#!/bin/sh\nexit 0\n")
	runResolver(t, paths)
	configured := readTestFile(t, paths.resolvConf)
	for _, want := range []string{"nameserver 192.0.2.10", "search example.com example.net", "options attempts:2"} {
		if !strings.Contains(configured, want) {
			t.Fatalf("configured resolver does not contain %q: %s", want, configured)
		}
	}
	if !strings.Contains(configured, "nameserver 203.0.113.53") {
		t.Fatalf("configured resolver does not retain the NetworkManager fallback: %s", configured)
	}
	if strings.Index(configured, "nameserver 192.0.2.10") > strings.Index(configured, "nameserver 203.0.113.53") {
		t.Fatalf("local CoreDNS is not the first resolver: %s", configured)
	}
	if _, err := os.Stat(paths.resolvedDropin); !os.IsNotExist(err) {
		t.Fatalf("active systemd-resolved was used for a regular resolv.conf: %v", err)
	}

	runResolver(t, paths)
	if got := readTestFile(t, paths.resolvConf); got != configured {
		t.Fatalf("repeated apply changed resolver: got %q, want %q", got, configured)
	}
}

func TestSNOCoreDNSResolverSystemdResolved(t *testing.T) {
	script := renderSNODNSResolver(t)
	testDir := t.TempDir()
	paths := prepareResolverTest(t, testDir, script)

	stubResolv := filepath.Join(paths.resolvedRuntimeDir, "stub-resolv.conf")
	writeTestFile(t, stubResolv, "nameserver 127.0.0.53\n", 0o644)
	if err := os.Symlink(stubResolv, paths.resolvConf); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, paths.nmResolv, "nameserver 203.0.113.53\nsearch example.net\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "ipv4"), "192.0.2.10\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "ipv6"), "2001:db8::10\n", 0o644)
	writeExecutable(t, filepath.Join(paths.binDir, "curl"), "#!/bin/sh\nexit 0\n")

	runResolver(t, paths)
	configured := readTestFile(t, paths.resolvedDropin)
	if !strings.Contains(configured, "DNS=192.0.2.10 2001:db8::10") || strings.Contains(configured, "203.0.113.53") {
		t.Fatalf("unexpected systemd-resolved configuration: %s", configured)
	}
	if target, err := os.Readlink(paths.resolvConf); err != nil || target != stubResolv {
		t.Fatalf("systemd-resolved symlink changed: target=%q err=%v", target, err)
	}
	if !strings.Contains(configured, "Domains=example.com example.net\n") || strings.Contains(configured, "~.") {
		t.Fatalf("unexpected systemd-resolved domain routing: %s", configured)
	}
	if _, err := os.Stat(paths.nmDropin); !os.IsNotExist(err) {
		t.Fatalf("resolved configuration changed NetworkManager ownership: %v", err)
	}

	runResolver(t, paths)
	if got := readTestFile(t, paths.resolvedDropin); got != configured {
		t.Fatalf("repeated apply changed resolved drop-in: got %q, want %q", got, configured)
	}
}

type resolverTestPaths struct {
	script             string
	binDir             string
	resolvedDropin     string
	resolvedRuntimeDir string
	nmResolv           string
	nmDropin           string
	resolvConf         string
	nodeIPDir          string
}

func renderSNODNSResolver(t *testing.T) string {
	t.Helper()
	cc := newControllerConfig("test-cluster")
	cc.Spec.DNS = &configv1.DNS{Spec: configv1.DNSSpec{BaseDomain: "example.com"}}
	cc.Spec.Infra.Status.PlatformStatus.Type = configv1.NonePlatformType
	cc.Spec.Infra.Status.ControlPlaneTopology = configv1.SingleReplicaTopologyMode
	fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]configv1.FeatureGateName{features.FeatureGateUnifiedClusterManagedDNSAndLB}, nil)
	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy":"dummy"}`), nil, fgHandler)
	require.NoError(t, err)
	for _, mc := range mcs {
		ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
		require.NoError(t, err)
		for _, file := range ignCfg.Storage.Files {
			if file.Path == "/usr/local/bin/sno-resolv-prepender.sh" {
				contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
				require.NoError(t, err)
				return string(contents)
			}
		}
	}
	t.Fatal("rendered MachineConfig is missing resolver script")
	return ""
}

func prepareResolverTest(t *testing.T, testDir, script string) resolverTestPaths {
	t.Helper()
	paths := resolverTestPaths{
		script:             filepath.Join(testDir, "sno-resolv-prepender.sh"),
		binDir:             filepath.Join(testDir, "bin"),
		resolvedDropin:     filepath.Join(testDir, "resolved.conf.d", "60-sno-internal-dns.conf"),
		resolvedRuntimeDir: filepath.Join(testDir, "run", "systemd", "resolve"),
		nmResolv:           filepath.Join(testDir, "run", "NetworkManager", "resolv.conf"),
		nmDropin:           filepath.Join(testDir, "run", "NetworkManager", "conf.d", "99-sno-internal-dns.conf"),
		resolvConf:         filepath.Join(testDir, "etc", "resolv.conf"),
		nodeIPDir:          filepath.Join(testDir, "run", "nodeip-configuration"),
	}
	for _, dir := range []string{paths.binDir, filepath.Dir(paths.resolvedDropin), paths.resolvedRuntimeDir, filepath.Dir(paths.nmResolv), filepath.Dir(paths.resolvConf), paths.nodeIPDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, paths.script, script)
	writeExecutable(t, filepath.Join(paths.binDir, "systemctl"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(paths.binDir, "restorecon"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(paths.binDir, "nmcli"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(paths.binDir, "busctl"), `#!/bin/sh
if [ -f "$SNO_DNS_NM_DROPIN" ] && grep -q '^rc-manager=unmanaged$' "$SNO_DNS_NM_DROPIN"; then
  echo 's "unmanaged"'
else
  echo 's "file"'
fi
`)
	return paths
}

func runResolver(t *testing.T, paths resolverTestPaths) {
	t.Helper()
	if output, err := resolverCommand(paths).CombinedOutput(); err != nil {
		t.Fatalf("resolver failed: %v\n%s", err, output)
	}
}

func resolverCommand(paths resolverTestPaths) *exec.Cmd {
	cmd := exec.Command(paths.script)
	cmd.Env = append(os.Environ(),
		"PATH="+paths.binDir+":"+os.Getenv("PATH"),
		"SNO_DNS_RESOLVED_DROPIN="+paths.resolvedDropin,
		"SNO_DNS_RESOLVED_RUNTIME_DIR="+paths.resolvedRuntimeDir,
		"SNO_DNS_NM_RESOLV="+paths.nmResolv,
		"SNO_DNS_NM_DROPIN="+paths.nmDropin,
		"SNO_DNS_RESOLV_CONF="+paths.resolvConf,
		"SNO_DNS_NODE_IP_DIR="+paths.nodeIPDir,
	)
	return cmd
}

func writeTestFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	writeTestFile(t, path, contents, 0o755)
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestSNOCoreDNSResolverNetworkManagerSymlink(t *testing.T) {
	paths := prepareResolverTest(t, t.TempDir(), renderSNODNSResolver(t))
	require.NoError(t, os.Symlink(paths.nmResolv, paths.resolvConf))
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "192.0.2.10\n", 0o644)
	writeExecutable(t, filepath.Join(paths.binDir, "curl"), "#!/bin/sh\nexit 0\n")
	for _, upstream := range []string{"198.51.100.53", "203.0.113.53"} {
		nmConfig := "nameserver " + upstream + "\n"
		writeTestFile(t, paths.nmResolv, nmConfig, 0o644)
		runResolver(t, paths)
		require.Equal(t, nmConfig, readTestFile(t, paths.nmResolv), "NetworkManager input must not be overwritten")
		require.Equal(t, "nameserver 192.0.2.10\n"+nmConfig+"search example.com\n", readTestFile(t, paths.resolvConf))
		require.Equal(t, "[main]\nrc-manager=unmanaged\n", readTestFile(t, paths.nmDropin))

		before, err := os.Stat(paths.resolvConf)
		require.NoError(t, err)
		runResolver(t, paths)
		after, err := os.Stat(paths.resolvConf)
		require.NoError(t, err)
		require.True(t, os.SameFile(before, after), "unchanged resolver was replaced")
	}
}

func TestSNOCoreDNSResolverDeduplicatesFallbacks(t *testing.T) {
	paths := prepareResolverTest(t, t.TempDir(), renderSNODNSResolver(t))
	writeTestFile(t, paths.resolvConf, "nameserver 198.51.100.53\n", 0o644)
	writeTestFile(t, paths.nmResolv, "nameserver 192.0.2.10\nnameserver 2001:db8::10\nnameserver 203.0.113.53\nnameserver 203.0.113.53\nnameserver 203.0.113.54\nsearch example.net example.com example.net\noptions attempts:2\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "2001:db8::10\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "ipv4"), "192.0.2.10\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "ipv6"), "2001:db8::10\n", 0o644)
	writeExecutable(t, filepath.Join(paths.binDir, "curl"), "#!/bin/sh\nexit 0\n")
	want := "nameserver 2001:db8::10\nnameserver 192.0.2.10\nnameserver 203.0.113.53\n# nameserver 203.0.113.54\noptions attempts:2\nsearch example.com example.net\n"
	for range 2 {
		runResolver(t, paths)
		require.Equal(t, want, readTestFile(t, paths.resolvConf))
	}
}

func TestSNOCoreDNSResolverRejectsUnsafeTakeover(t *testing.T) {
	script := renderSNODNSResolver(t)
	for _, reason := range []string{"ownership unchanged", "reload failure"} {
		t.Run(reason, func(t *testing.T) {
			paths := prepareResolverTest(t, t.TempDir(), script)
			original := "nameserver 198.51.100.53\n"
			writeTestFile(t, paths.resolvConf, original, 0o644)
			writeTestFile(t, paths.nmResolv, original, 0o644)
			writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "192.0.2.10\n", 0o644)
			writeExecutable(t, filepath.Join(paths.binDir, "curl"), "#!/bin/sh\nexit 0\n")
			switch reason {
			case "ownership unchanged":
				writeExecutable(t, filepath.Join(paths.binDir, "busctl"), "#!/bin/sh\necho 's \"file\"'\n")
			case "reload failure":
				writeExecutable(t, filepath.Join(paths.binDir, "nmcli"), "#!/bin/sh\nexit 1\n")
			}
			_, err := resolverCommand(paths).CombinedOutput()
			require.Error(t, err)
			require.Equal(t, original, readTestFile(t, paths.resolvConf))
		})
	}
}
