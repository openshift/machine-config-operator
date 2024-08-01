package e2e_node_test

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/coreos/go-systemd/dbus"
	systemdunit "github.com/coreos/go-systemd/unit"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	rpmostreeclient "github.com/coreos/rpmostree-client-go/pkg/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ctrlconfigPath string
var machineconfigPath string

func init() {
	flag.StringVar(&machineconfigPath, "machineconfig", "", "Path to MachineConfig")
	flag.StringVar(&ctrlconfigPath, "ctrlconfig", "", "Path to ControllerConfig")
}

const (
	systemdRoot string = "/etc/systemd/system"
)

type assets struct {
	machineConfig *mcfgv1.MachineConfig
	ignConfig     *ign3types.Config
	ctrlConfig    *mcfgv1.ControllerConfig
}

// Asserts that the on-disk state is as expected by using the same functions the MCD uses.
func TestOnDiskState(t *testing.T) {
	runTestWithHostname(t, func(t *testing.T, a *assets) {
		assert.NoError(t, daemon.ValidateOnDiskState(a.machineConfig))
	})
}

// Asserts that the systemd units are configured appropriately.
func TestSystemdUnits(t *testing.T) {
	runTestWithHostname(t, func(t *testing.T, a *assets) {
		cxn, err := dbus.NewSystemdConnection()
		require.NoError(t, err)

		for _, unit := range a.ignConfig.Systemd.Units {
			t.Run(unit.Name, func(t *testing.T) {
				units, err := cxn.ListUnitsByNames([]string{unit.Name})
				require.NoError(t, err)

				unitState := units[0]

				if unit.Contents == nil {
					return
				}

				deserialized, err := systemdunit.Deserialize(strings.NewReader(*unit.Contents))
				require.NoError(t, err)

				if unit.Enabled != nil && *unit.Enabled {
					if !isOneshot(deserialized) && !isRemainAfterExit(deserialized) {
						assert.Contains(t, []string{"running", "active"}, unitState.SubState)
						assert.FileExists(t, filepath.Join(systemdRoot, unit.Name))
					} else {
						assert.Contains(t, []string{"dead", "exited"}, unitState.SubState)
					}
				}
			})
		}

	})
}

// Asserts that the given files exist.
func TestFiles(t *testing.T) {
	runTestWithHostname(t, func(t *testing.T, a *assets) {
		for _, file := range a.ignConfig.Storage.Files {
			t.Run(file.Path, func(t *testing.T) {
				assert.FileExists(t, file.Path)
			})
		}
	})
}

// Asserts that the OS image is as expected via the MachineConfig.
func TestOSImage(t *testing.T) {
	runTestWithHostname(t, func(t *testing.T, a *assets) {
		rpmostreeClient := rpmostreeclient.NewClient(t.Name())

		status, err := rpmostreeClient.QueryStatus()
		require.NoError(t, err)

		assert.Equal(t, len(status.Deployments), 1)
		currentDeploy := status.Deployments[0]

		assert.True(t, currentDeploy.Booted)
		assert.Contains(t, currentDeploy.ContainerImageReference, a.machineConfig.Spec.OSImageURL)
	})
}

// Asserts that the kernel arguments are as expected.
func TestKernelArgs(t *testing.T) {
	runTestWithHostname(t, func(t *testing.T, a *assets) {
		cmd := exec.Command("rpm-ostree", "kargs")

		output, err := cmd.CombinedOutput()
		require.NoError(t, err)

		for _, karg := range a.machineConfig.Spec.KernelArguments {
			assert.Contains(t, string(output), karg)
		}
	})
}

// Asserts that files managed by the certificate writer exist.
func TestCertificateWriter(t *testing.T) {
	runTestWithHostname(t, func(t *testing.T, a *assets) {
		filesToCheck := []string{
			"/etc/kubernetes/kubelet-ca.crt",
			"/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem",
			"/etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt",
			"/etc/kubernetes/kubeconfig",
			"/etc/mco/internal-registry-pull-secret.json",
		}

		for _, file := range filesToCheck {
			t.Run(file, func(t *testing.T) {
				assert.FileExists(t, file)
			})
		}
	})
}

// Asserts that a given file contains the expected contents.
func assertFileContains(t *testing.T, filename, expectedContents string) bool {
	if !assert.FileExists(t, filename) {
		return false
	}

	contents, err := os.ReadFile(filename)
	require.NoError(t, err)

	return assert.Contains(t, string(contents), expectedContents, "expected file %q to contain %q", filename, expectedContents)
}

// Asserts that a given file does not contain the expected contents.
func assertFileNotContains(t *testing.T, filename, expectedContents string) bool {
	if !assert.FileExists(t, filename) {
		return false
	}

	contents, err := os.ReadFile(filename)
	require.NoError(t, err)

	return assert.NotContains(t, string(contents), expectedContents, "expected file %q not to contain %q", filename, expectedContents)
}

// Appends the hostname to the test name via a subtest and ensures that all
// tests ran this way are ran in parallel.
func runTestWithHostname(t *testing.T, testFunc func(*testing.T, *assets)) {
	t.Parallel()

	hostname, err := os.Hostname()
	require.NoError(t, err)

	assets, err := readAssetsFromDisk()
	require.NoError(t, err)

	t.Run(hostname, func(t *testing.T) {
		t.Parallel()
		testFunc(t, assets)
	})
}

// Checks if a systemd unit is configured as expected.
func isUnitConfiguredThusly(opts []*systemdunit.UnitOption, cfg *systemdunit.UnitOption) bool {
	for _, opt := range opts {
		if opt.Match(cfg) {
			return true
		}
	}

	return false
}

// Returns true if a systemd unit is configured to remain after exit.
func isRemainAfterExit(opts []*systemdunit.UnitOption) bool {
	return isUnitConfiguredThusly(opts, &systemdunit.UnitOption{
		Section: "Service",
		Name:    "Type",
		Value:   "oneshot",
	})
}

// Returns trye if a systemd unit is configured as a onehost.
func isOneshot(opts []*systemdunit.UnitOption) bool {
	return isUnitConfiguredThusly(opts, &systemdunit.UnitOption{
		Section: "Service",
		Name:    "RemainAfterExit",
		Value:   "yes",
	})
}

// Reads the test assets (MachineConfig and ControllerConfig) from disk.
func readAssetsFromDisk() (*assets, error) {
	mc, err := readMachineConfigFromDisk(machineconfigPath)
	if err != nil {
		return nil, err
	}

	ignConfig, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, err
	}

	ctrlConfig, err := readControllerConfigFromDisk(ctrlconfigPath)
	if err != nil {
		return nil, err
	}

	return &assets{
		machineConfig: mc,
		ignConfig:     &ignConfig,
		ctrlConfig:    ctrlConfig,
	}, nil
}

// Reads the MachineConfig from disk.
func readMachineConfigFromDisk(path string) (*mcfgv1.MachineConfig, error) {
	mcBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	mc := &mcfgv1.MachineConfig{}

	if err := json.Unmarshal(mcBytes, mc); err != nil {
		return nil, err
	}

	return mc, nil
}

// Reads the ControllerConfig from disk.
func readControllerConfigFromDisk(path string) (*mcfgv1.ControllerConfig, error) {
	cfgBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	ctrlcfg := &mcfgv1.ControllerConfig{}
	if err := json.Unmarshal(cfgBytes, ctrlcfg); err != nil {
		return nil, err
	}

	return ctrlcfg, nil
}
