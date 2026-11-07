package template

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, tc := range []struct {
		name    string
		config  *mcfgv1.ControllerConfigSpec
		handler ctrlcommon.FeatureGatesHandler
		want    bool
	}{
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
		t.Run(tc.name, func(t *testing.T) {
			cc := newControllerConfig("test-cluster")
			cc.Spec.DNS = &configv1.DNS{Spec: configv1.DNSSpec{BaseDomain: "example.com"}}
			cc.Spec.Infra.Status.PlatformStatus.Type = tc.platform
			cc.Spec.Infra.Status.ControlPlaneTopology = tc.topology

			enabled := []configv1.FeatureGateName{}
			if tc.enabled {
				enabled = append(enabled, features.FeatureGateUnifiedClusterManagedDNSAndLB)
			}
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler(enabled, nil)

			got, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy":"dummy"}`), nil, fgHandler)
			require.NoError(t, err)

			disabled, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy":"dummy"}`), nil, nil)
			require.NoError(t, err)
			baseline := map[string]*mcfgv1.MachineConfig{}
			for _, mc := range disabled {
				baseline[mc.Name] = mc
			}

			seenAssets := map[string]bool{}
			for _, mc := range got {
				if !tc.want || mc.Labels[mcfgv1.MachineConfigRoleLabelKey] != masterRole {
					require.Equal(t, baseline[mc.Name], mc, "MachineConfig changed outside enabled master configuration")
				}
				ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
				require.NoError(t, err)

				var assets []string
				for _, file := range ignCfg.Storage.Files {
					if strings.Contains(file.Path, "sno-coredns") || file.Path == "/usr/local/bin/sno-resolv-prepender.sh" || file.Path == "/etc/NetworkManager/conf.d/99-sno-internal-dns.conf" {
						assets = append(assets, file.Path)
					}
				}
				for _, unit := range ignCfg.Systemd.Units {
					if unit.Name == "sno-resolv-prepender.service" || unit.Name == "sno-resolv-prepender.path" {
						assets = append(assets, unit.Name)
					}
				}
				for _, name := range assets {
					seenAssets[name] = true
					require.Equal(t, masterRole, mc.Labels[mcfgv1.MachineConfigRoleLabelKey], "SNO DNS asset %s rendered outside master", name)
				}
			}

			wantAssets := map[string]bool{}
			if tc.want {
				wantAssets = map[string]bool{
					"/etc/kubernetes/manifests/sno-coredns.yaml":                     true,
					"/etc/kubernetes/static-pod-resources/sno-coredns/Corefile.tmpl": true,
					"/etc/NetworkManager/conf.d/99-sno-internal-dns.conf":            true,
					"/usr/local/bin/sno-resolv-prepender.sh":                         true,
					"sno-resolv-prepender.service":                                   true,
					"sno-resolv-prepender.path":                                      true,
				}
			}
			require.Equal(t, wantAssets, seenAssets, "rendered SNO DNS assets")
		})
	}
}

func TestSNOCoreDNSCorefile(t *testing.T) {
	runtimeTemplate, err := template.New("Corefile").Parse(renderSNODNSFile(t, "/etc/kubernetes/static-pod-resources/sno-coredns/Corefile.tmpl"))
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		addresses []struct{ Address, RecordType string }
		upstreams []string
		want      []string
		notWant   []string
	}{
		{"ipv4", []struct{ Address, RecordType string }{{"192.0.2.10", "A"}}, []string{"192.0.2.53"}, []string{"IN A 192.0.2.10", "forward . 192.0.2.53 {"}, []string{"IN AAAA"}},
		{"dual-stack", []struct{ Address, RecordType string }{{"192.0.2.10", "A"}, {"2001:db8::10", "AAAA"}}, []string{"192.0.2.53"}, []string{"IN A 192.0.2.10", "IN AAAA 2001:db8::10"}, nil},
		{"no upstreams", []struct{ Address, RecordType string }{{"192.0.2.10", "A"}}, nil, []string{"IN A 192.0.2.10", "}\n    cache 30"}, []string{"forward", "policy sequential"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := struct {
				DNSAddresses []struct{ Address, RecordType string }
				DNSUpstreams []string
			}{
				DNSAddresses: tc.addresses,
				DNSUpstreams: tc.upstreams,
			}
			var rendered bytes.Buffer
			require.NoError(t, runtimeTemplate.Execute(&rendered, data))
			out := rendered.String()

			for _, want := range append([]string{"{{ .Name }}", "bufsize 512", "template IN ANY example.com {"}, tc.want...) {
				require.Contains(t, out, want)
			}
			for _, notWant := range tc.notWant {
				require.NotContains(t, out, notWant)
			}

			// CoreDNS uses the first matching template, so address answers must precede the empty catch-all.
			catchAll := strings.Index(out, "template IN ANY example.com")
			require.Less(t, strings.LastIndex(out, "answer "), catchAll, "address answers must precede the empty catch-all")
			if len(tc.upstreams) > 0 {
				require.Less(t, catchAll, strings.Index(out, "forward ."), "empty catch-all must precede forward")
			}
		})
	}
}

func TestSNOCoreDNSResolverPlainFile(t *testing.T) {
	script := renderSNODNSFile(t, "/usr/local/bin/sno-resolv-prepender.sh")
	testDir := t.TempDir()
	paths := prepareResolverTest(t, testDir, script)

	writeTestFile(t, paths.resolvConf, "nameserver 198.51.100.53\noptions timeout:2\n", 0o644)
	writeTestFile(t, paths.nmResolv, "nameserver 203.0.113.53\nsearch example.net\noptions attempts:2\n", 0o644)
	_, err := resolverCommand(paths).CombinedOutput()
	require.Error(t, err, "resolver succeeded without node IPs")
	require.Equal(t, "nameserver 203.0.113.53\noptions attempts:2\nsearch example.com example.net\n", readTestFile(t, paths.resolvConf), "upstreams must be usable before node IPs exist")

	writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "192.0.2.10\n", 0o644)
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
	script := renderSNODNSFile(t, "/usr/local/bin/sno-resolv-prepender.sh")
	testDir := t.TempDir()
	paths := prepareResolverTest(t, testDir, script)

	stubResolv := filepath.Join(paths.resolvedRuntimeDir, "stub-resolv.conf")
	writeTestFile(t, stubResolv, "nameserver 127.0.0.53\n", 0o644)
	if err := os.Symlink(stubResolv, paths.resolvConf); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, paths.nmResolv, "nameserver 203.0.113.53\nsearch example.net\n", 0o644)
	_, err := resolverCommand(paths).CombinedOutput()
	require.Error(t, err, "resolver succeeded without node IPs")
	require.NoFileExists(t, paths.resolvedDropin)

	writeTestFile(t, filepath.Join(paths.nodeIPDir, "ipv4"), "192.0.2.10\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "ipv6"), "2001:db8::10\n", 0o644)

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

	runResolver(t, paths)
	if got := readTestFile(t, paths.resolvedDropin); got != configured {
		t.Fatalf("repeated apply changed resolved drop-in: got %q, want %q", got, configured)
	}
}

func TestSNOCoreDNSResolverWithoutNetworkManagerNameserver(t *testing.T) {
	script := renderSNODNSFile(t, "/usr/local/bin/sno-resolv-prepender.sh")
	for _, resolved := range []bool{false, true} {
		t.Run("resolved="+strconv.FormatBool(resolved), func(t *testing.T) {
			paths := prepareResolverTest(t, t.TempDir(), script)
			writeTestFile(t, paths.nmResolv, "search example.net\noptions attempts:2\n", 0o644)
			writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "192.0.2.10\n", 0o644)

			if resolved {
				stubResolv := filepath.Join(paths.resolvedRuntimeDir, "stub-resolv.conf")
				writeTestFile(t, stubResolv, "nameserver 127.0.0.53\n", 0o644)
				require.NoError(t, os.Symlink(stubResolv, paths.resolvConf))
			} else {
				writeTestFile(t, paths.resolvConf, "nameserver 198.51.100.53\n", 0o644)
			}

			runResolver(t, paths)
			if resolved {
				require.Equal(t, "[Resolve]\nDNS=192.0.2.10\nDomains=example.com example.net\n", readTestFile(t, paths.resolvedDropin))
			} else {
				require.Equal(t, "nameserver 192.0.2.10\noptions attempts:2\nsearch example.com example.net\n", readTestFile(t, paths.resolvConf))
			}
		})
	}
}

func TestSNOCoreDNSResolverRetriesSystemdResolvedRestart(t *testing.T) {
	paths := prepareResolverTest(t, t.TempDir(), renderSNODNSFile(t, "/usr/local/bin/sno-resolv-prepender.sh"))
	stubResolv := filepath.Join(paths.resolvedRuntimeDir, "stub-resolv.conf")
	writeTestFile(t, stubResolv, "nameserver 127.0.0.53\n", 0o644)
	require.NoError(t, os.Symlink(stubResolv, paths.resolvConf))
	writeTestFile(t, paths.nmResolv, "nameserver 203.0.113.53\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "192.0.2.10\n", 0o644)
	writeExecutable(t, filepath.Join(paths.binDir, "systemctl"), `#!/bin/sh
attempts_file="$(dirname "$0")/systemctl-attempts"
attempts=0
if [ -f "${attempts_file}" ]; then
  read -r attempts < "${attempts_file}"
fi
attempts=$((attempts + 1))
printf '%s\n' "${attempts}" > "${attempts_file}"
[ "${attempts}" -gt 1 ]
`)

	_, err := resolverCommand(paths).CombinedOutput()
	require.Error(t, err)
	before, err := os.Stat(paths.resolvedDropin)
	require.NoError(t, err)
	_, err = os.Stat(paths.resolvedRestartPending)
	require.NoError(t, err)

	runResolver(t, paths)
	after, err := os.Stat(paths.resolvedDropin)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "unchanged drop-in was replaced")
	require.NoFileExists(t, paths.resolvedRestartPending)
	require.Equal(t, "2\n", readTestFile(t, filepath.Join(paths.binDir, "systemctl-attempts")))

	runResolver(t, paths)
	require.Equal(t, "2\n", readTestFile(t, filepath.Join(paths.binDir, "systemctl-attempts")), "unchanged applied drop-in triggered another restart")
}

func TestSNOCoreDNSResolverNetworkManagerSymlink(t *testing.T) {
	paths := prepareResolverTest(t, t.TempDir(), renderSNODNSFile(t, "/usr/local/bin/sno-resolv-prepender.sh"))
	require.NoError(t, os.Symlink(paths.nmResolv, paths.resolvConf))
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "192.0.2.10\n", 0o644)
	for _, upstream := range []string{"198.51.100.53", "203.0.113.53"} {
		nmConfig := "nameserver " + upstream + "\n"
		writeTestFile(t, paths.nmResolv, nmConfig, 0o644)
		runResolver(t, paths)
		require.Equal(t, nmConfig, readTestFile(t, paths.nmResolv), "NetworkManager input must not be overwritten")
		require.Equal(t, "nameserver 192.0.2.10\n"+nmConfig+"search example.com\n", readTestFile(t, paths.resolvConf))

		before, err := os.Stat(paths.resolvConf)
		require.NoError(t, err)
		runResolver(t, paths)
		after, err := os.Stat(paths.resolvConf)
		require.NoError(t, err)
		require.True(t, os.SameFile(before, after), "unchanged resolver was replaced")
	}
}

func TestSNOCoreDNSResolverDeduplicatesFallbacks(t *testing.T) {
	paths := prepareResolverTest(t, t.TempDir(), renderSNODNSFile(t, "/usr/local/bin/sno-resolv-prepender.sh"))
	writeTestFile(t, paths.resolvConf, "nameserver 198.51.100.53\n", 0o644)
	writeTestFile(t, paths.nmResolv, "nameserver 192.0.2.10\nnameserver 2001:db8::10\nnameserver 203.0.113.53\nnameserver 203.0.113.53\nnameserver 203.0.113.54\nsearch example.net example.com example.net\noptions attempts:2\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "2001:db8::10\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "ipv4"), "192.0.2.10\n", 0o644)
	writeTestFile(t, filepath.Join(paths.nodeIPDir, "ipv6"), "2001:db8::10\n", 0o644)
	want := "nameserver 2001:db8::10\nnameserver 192.0.2.10\nnameserver 203.0.113.53\n# nameserver 203.0.113.54\noptions attempts:2\nsearch example.com example.net\n"
	for range 2 {
		runResolver(t, paths)
		require.Equal(t, want, readTestFile(t, paths.resolvConf))
	}
}

func TestSNOCoreDNSResolverPreservesResolverOnFailure(t *testing.T) {
	script := renderSNODNSFile(t, "/usr/local/bin/sno-resolv-prepender.sh")
	for _, reason := range []string{"missing NetworkManager resolv.conf", "no nameservers"} {
		t.Run(reason, func(t *testing.T) {
			paths := prepareResolverTest(t, t.TempDir(), script)
			original := "nameserver 198.51.100.53\n"
			writeTestFile(t, paths.resolvConf, original, 0o644)
			if reason == "no nameservers" {
				writeTestFile(t, paths.nmResolv, "search example.net\n", 0o644)
			} else {
				writeTestFile(t, filepath.Join(paths.nodeIPDir, "primary-ip"), "192.0.2.10\n", 0o644)
			}
			_, err := resolverCommand(paths).CombinedOutput()
			require.Error(t, err)
			require.Equal(t, original, readTestFile(t, paths.resolvConf))

			entries, err := os.ReadDir(filepath.Dir(paths.resolvConf))
			require.NoError(t, err)
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			require.Equal(t, []string{"resolv.conf"}, names, "resolver left temporary files")
		})
	}
}

type resolverTestPaths struct {
	script                 string
	binDir                 string
	resolvedDropin         string
	resolvedRestartPending string
	resolvedRuntimeDir     string
	nmResolv               string
	resolvConf             string
	nodeIPDir              string
}

func renderSNODNSFile(t *testing.T, path string) string {
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
			if file.Path == path {
				contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
				require.NoError(t, err)
				return string(contents)
			}
		}
	}
	t.Fatalf("rendered MachineConfig is missing %s", path)
	return ""
}

func prepareResolverTest(t *testing.T, testDir, script string) resolverTestPaths {
	t.Helper()
	paths := resolverTestPaths{
		script:                 filepath.Join(testDir, "sno-resolv-prepender.sh"),
		binDir:                 filepath.Join(testDir, "bin"),
		resolvedDropin:         filepath.Join(testDir, "resolved.conf.d", "60-sno-internal-dns.conf"),
		resolvedRestartPending: filepath.Join(testDir, "run", "sno-resolv-prepender", "resolved-restart-pending"),
		resolvedRuntimeDir:     filepath.Join(testDir, "run", "systemd", "resolve"),
		nmResolv:               filepath.Join(testDir, "run", "NetworkManager", "resolv.conf"),
		resolvConf:             filepath.Join(testDir, "etc", "resolv.conf"),
		nodeIPDir:              filepath.Join(testDir, "run", "nodeip-configuration"),
	}
	for _, dir := range []string{paths.binDir, filepath.Dir(paths.resolvedDropin), paths.resolvedRuntimeDir, filepath.Dir(paths.nmResolv), filepath.Dir(paths.resolvConf), paths.nodeIPDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, paths.script, script)
	writeExecutable(t, filepath.Join(paths.binDir, "systemctl"), "#!/bin/sh\nexit 0\n")
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
		"SNO_DNS_RESOLVED_RESTART_PENDING="+paths.resolvedRestartPending,
		"SNO_DNS_RESOLVED_RUNTIME_DIR="+paths.resolvedRuntimeDir,
		"SNO_DNS_NM_RESOLV="+paths.nmResolv,
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
