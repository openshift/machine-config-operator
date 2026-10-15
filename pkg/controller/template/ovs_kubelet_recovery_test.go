package template

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-systemd/v22/unit"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const recoveryPath = "/usr/local/sbin/ovs-kubelet-recovery"
const recoveryUnit = "ovs-kubelet-recovery.service"

// Exercise the real template renderer, including common templates and platform
// overrides, instead of maintaining a separate copy of the recovery script.
func renderRecoveryConfig(t *testing.T, fixture, network, role string, configure ...func(*mcfgv1.ControllerConfig)) ign3types.Config {
	t.Helper()
	cc, err := controllerConfigFromFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cc.Spec.NetworkType = network
	for _, fn := range configure {
		fn(cc)
	}
	cfgs, err := GenerateMachineConfigsForRole(&RenderConfig{&cc.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, role, templateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range cfgs {
		ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
		if err != nil {
			t.Fatal(err)
		}
		if findIgnUnit(ign.Systemd.Units, "kubelet-dependencies.target") {
			return ign
		}
	}
	t.Fatal("no MachineConfig contains kubelet-dependencies.target")
	return ign3types.Config{}
}

func TestOVSKubeletRecoveryTemplates(t *testing.T) {
	for name, fixture := range configs {
		for _, network := range []string{"OVNKubernetes", "OpenShiftSDN", ""} {
			for _, role := range []string{"master", "worker", "arbiter", "custom-worker"} {
				t.Run(name+"/"+network+"/"+role, func(t *testing.T) {
					ign := renderRecoveryConfig(t, fixture, network, role)
					wantRecovery := network == "OVNKubernetes"
					if got := findIgnFile(ign.Storage.Files, recoveryPath); got != wantRecovery {
						t.Fatalf("recovery script present = %t, want %t", got, wantRecovery)
					}
					if got := findIgnUnit(ign.Systemd.Units, recoveryUnit); got != wantRecovery {
						t.Fatalf("recovery unit present = %t, want %t", got, wantRecovery)
					}
					for _, file := range ign.Storage.Files {
						if file.Path == recoveryPath && (file.Mode == nil || *file.Mode != 0755) {
							t.Fatal("recovery script must be executable (0755)")
						}
					}
					for _, svc := range ign.Systemd.Units {
						if svc.Name != recoveryUnit && svc.Name != "kubelet-dependencies.target" {
							continue
						}
						if svc.Contents == nil {
							t.Fatalf("missing contents for %s", svc.Name)
						}
						options, err := unit.Deserialize(strings.NewReader(*svc.Contents))
						if err != nil {
							t.Fatal(err)
						}
						var expected []*unit.UnitOption
						if svc.Name == recoveryUnit {
							// Static: only OnFailure activates recovery. It must not
							// become part of the dependency chain it tries to start.
							if svc.Enabled != nil || len(svc.Dropins) != 0 {
								t.Fatal("recovery must be a static unit without drop-ins")
							}
							expected = []*unit.UnitOption{
								unit.NewUnitOption("Unit", "Description", "Recover OVS-dependent Kubernetes boot chain"),
								unit.NewUnitOption("Unit", "After", "local-fs.target"),
								unit.NewUnitOption("Unit", "StartLimitIntervalSec", "20min"),
								unit.NewUnitOption("Unit", "StartLimitBurst", "3"),
								unit.NewUnitOption("Service", "Type", "oneshot"),
								unit.NewUnitOption("Service", "ExecStart", recoveryPath),
								unit.NewUnitOption("Service", "TimeoutStartSec", "7min"),
							}
						} else {
							expected = []*unit.UnitOption{
								unit.NewUnitOption("Unit", "Description", "Dependencies necessary to run kubelet"),
								unit.NewUnitOption("Unit", "Documentation", "https://github.com/openshift/machine-config-operator/"),
								unit.NewUnitOption("Unit", "Requires", "basic.target network-online.target"),
								unit.NewUnitOption("Unit", "Wants", "NetworkManager-wait-online.service crio-wipe.service"),
								unit.NewUnitOption("Unit", "Wants", "rpc-statd.service chrony-wait.service"),
							}
							if wantRecovery {
								expected = append(expected, unit.NewUnitOption("Unit", "OnFailure", recoveryUnit))
							} else {
								// Avoid a whitespace-only target change (and needless
								// MachineConfig rollout) on non-OVN clusters.
								const unchanged = "[Unit]\n" +
									"Description=Dependencies necessary to run kubelet\n" +
									"Documentation=https://github.com/openshift/machine-config-operator/\n" +
									"Requires=basic.target network-online.target\n" +
									"Wants=NetworkManager-wait-online.service crio-wipe.service\n" +
									"Wants=rpc-statd.service chrony-wait.service\n"
								if *svc.Contents != unchanged {
									t.Errorf("non-OVN target changed: %q", *svc.Contents)
								}
							}
						}
						if !unit.AllMatch(options, expected) {
							t.Errorf("unexpected %s options:\ngot %v\nwant %v", svc.Name, options, expected)
						}
					}
				})
			}
		}
	}
}

func TestOVSKubeletRecoveryBootstrap(t *testing.T) {
	for _, network := range []string{"OVNKubernetes", "OpenShiftSDN", ""} {
		t.Run(network, func(t *testing.T) {
			cc, err := controllerConfigFromFile(configs["aws"])
			if err != nil {
				t.Fatal(err)
			}
			cc.Spec.NetworkType = network
			cfgs, err := RunBootstrap(templateDir, cc, []byte(`{"auths":{}}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			baseConfigs := 0
			for _, cfg := range cfgs {
				ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
				if err != nil {
					t.Fatal(err)
				}
				if !findIgnUnit(ign.Systemd.Units, "kubelet-dependencies.target") {
					continue
				}
				baseConfigs++
				wantRecovery := network == "OVNKubernetes"
				if findIgnFile(ign.Storage.Files, recoveryPath) != wantRecovery || findIgnUnit(ign.Systemd.Units, recoveryUnit) != wantRecovery {
					t.Errorf("unexpected bootstrap recovery configuration in %s", cfg.Name)
				}
			}
			if baseConfigs != 2 {
				t.Errorf("expected master and worker base configs, got %d", baseConfigs)
			}
		})
	}
}

func TestOVSKubeletRecoveryOverrides(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*mcfgv1.ControllerConfig)
		want      string
		dropin    bool
	}{
		{
			name: "azure",
			configure: func(cc *mcfgv1.ControllerConfig) {
				cc.Spec.Infra.Status.PlatformStatus = &configv1.PlatformStatus{Type: configv1.AzurePlatformType}
			},
			want: "Before=kubelet-dependencies.target node-valid-hostname.service dnsmasq.service",
		},
		{
			name: "single-node",
			configure: func(cc *mcfgv1.ControllerConfig) {
				cc.Spec.Infra.Status.ControlPlaneTopology = configv1.SingleReplicaTopologyMode
			},
			want: "Before=kubelet-dependencies.target node-valid-hostname.service",
		},
		{
			name: "two-node-with-fencing",
			configure: func(cc *mcfgv1.ControllerConfig) {
				cc.Spec.Infra.Status.ControlPlaneTopology = configv1.DualReplicaTopologyMode
			},
			want:   "ConditionPathExists=!/var/run/ovs-config-executed",
			dropin: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ign := renderRecoveryConfig(t, configs["baremetal"], "OVNKubernetes", "master", tc.configure)
			if !findIgnFile(ign.Storage.Files, recoveryPath) || !findIgnUnit(ign.Systemd.Units, recoveryUnit) {
				t.Fatal("missing recovery script or unit")
			}
			for _, svc := range ign.Systemd.Units {
				if svc.Name != "ovs-configuration.service" {
					continue
				}
				if tc.dropin {
					for _, dropin := range svc.Dropins {
						if dropin.Contents != nil && strings.Contains(*dropin.Contents, tc.want) {
							return
						}
					}
				} else if svc.Contents != nil && strings.Contains(*svc.Contents, tc.want) {
					return
				}
			}
			t.Fatalf("existing OVS configuration override %q was not preserved", tc.want)
		})
	}
}

func TestOVSKubeletRecoveryScript(t *testing.T) {
	ign := renderRecoveryConfig(t, configs["aws"], "OVNKubernetes", "worker")
	scriptPath := filepath.Join(t.TempDir(), "ovs-kubelet-recovery")
	for _, file := range ign.Storage.Files {
		if file.Path == recoveryPath {
			data, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(scriptPath, data, 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if out, err := exec.Command("bash", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("invalid Bash syntax: %v\n%s", err, out)
	}

	type testCase struct {
		scenario     string
		inactiveUnit string
		wantStarts   int
		wantSleeps   int
		wantExit     int
	}
	tests := []testCase{
		{scenario: "healthy"},
		{scenario: "recovered-ovs", wantStarts: 1},
		{scenario: "delayed-ovs", wantStarts: 1, wantSleeps: 1},
		{scenario: "queue-failure", wantStarts: 1, wantSleeps: 1},
		{scenario: "database-unavailable", wantStarts: 1, wantSleeps: 1},
		{scenario: "transient-kubelet", wantStarts: 2, wantSleeps: 1},
		{scenario: "ovs-regression", wantStarts: 1, wantSleeps: 1},
		{scenario: "persistent-ovs", wantSleeps: 29, wantExit: 1},
		{scenario: "persistent-database", wantSleeps: 29, wantExit: 1},
		{scenario: "persistent-kubelet", wantStarts: 30, wantSleeps: 29, wantExit: 1},
		{scenario: "reset-failure", wantExit: 1},
		{scenario: "interrupted", wantStarts: 1, wantExit: 1},
	}
	for _, name := range []string{"ovsdb-server.service", "ovs-vswitchd.service", "openvswitch.service"} {
		tests = append(tests, testCase{scenario: "missing-ovs-unit", inactiveUnit: name, wantStarts: 1, wantSleeps: 1})
	}
	for _, name := range []string{"kubelet-dependencies.target", "crio.service", "kubelet.service"} {
		tests = append(tests, testCase{scenario: "inactive-chain", inactiveUnit: name, wantStarts: 2, wantSleeps: 1})
	}
	for _, tc := range tests {
		t.Run(tc.scenario+"/"+tc.inactiveUnit, func(t *testing.T) {
			tracePath := filepath.Join(t.TempDir(), "trace")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "./test_data/ovs_kubelet_recovery_mock.sh", scriptPath)
			cmd.Env = append(os.Environ(), "SCENARIO="+tc.scenario, "INACTIVE_UNIT="+tc.inactiveUnit, "TRACE="+tracePath)
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("recovery did not terminate: %v\n%s", ctx.Err(), out)
			}
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.wantExit {
				t.Fatalf("unexpected exit: %v, want %d\n%s", err, tc.wantExit, out)
			}
			trace, err := os.ReadFile(tracePath)
			if err != nil {
				t.Fatal(err)
			}
			for command, want := range map[string]int{
				"systemctl reset-failed ":           1,
				"systemctl start kubelet.service\n": tc.wantStarts,
				"sleep 10\n":                        tc.wantSleeps,
			} {
				if got := strings.Count(string(trace), command); got != want {
					t.Errorf("%q count = %d, want %d\n%s\n%s", command, got, want, trace, out)
				}
			}
			if tc.wantExit == 0 && !strings.Contains(string(out), "OVS, dependency target, CRI-O and kubelet are active") {
				t.Errorf("missing success diagnostic: %s", out)
			}
			if (tc.scenario == "healthy" || tc.scenario == "reset-failure") && strings.Contains(string(trace), "systemctl start ") {
				t.Errorf("unexpected service start: %s", trace)
			}
			if tc.scenario == "interrupted" && !strings.Contains(string(out), "recovery interrupted or timed out") {
				t.Errorf("missing interruption diagnostic: %s", out)
			}
			if strings.HasPrefix(tc.scenario, "persistent-") && !strings.Contains(string(out), "retry limit reached") {
				t.Errorf("missing retry exhaustion diagnostic: %s", out)
			}
		})
	}
}
