package daemon

import (
	"fmt"
	"testing"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// TestUpdateOS verifies the return errors from attempting to update the OS follow expectations
func TestUpdateOS(t *testing.T) {
	// expectedError is the error we will use when expecting an error to return
	expectedError := fmt.Errorf("broken")

	// testClient is the NodeUpdaterClient mock instance that will front
	// calls to update the host.
	testClient := RpmOstreeClientMock{
		GetBootedOSImageURLReturns: []GetBootedOSImageURLReturn{},
		RunPivotReturns: []error{
			// First run will return no error
			nil,
			// Second rrun will return our expected error
			expectedError},
	}

	// Create a Daemon instance with mocked clients
	d := Daemon{
		name:              "nodeName",
		OperatingSystem:   MachineConfigDaemonOSRHCOS,
		NodeUpdaterClient: testClient,
		loginClient:       nil, // set to nil as it will not be used within tests
		client:            fake.NewSimpleClientset(),
		kubeClient:        k8sfake.NewSimpleClientset(),
		rootMount:         "/",
		bootedOSImageURL:  "test",
	}

	// Set up machineconfigs to pass to updateOS.
	mcfg := &mcfgv1.MachineConfig{}
	// differentMcfg has a different OSImageURL so it will force Daemon.UpdateOS
	// to trigger an update of the operatingsystem (as fronted by our testClient)
	differentMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "somethingDifferent",
		},
	}

	// Test when the machine configs match. No operation should occur
	if err := d.updateOS(mcfg, mcfg); err != nil {
		t.Errorf("Expected no error. Got %s.", err)
	}
	// When machine configs differ but pivot succeeds we should get no error.
	if err := d.updateOS(mcfg, mcfg); err != nil {
		t.Errorf("Expected no error. Got %s.", err)
	}
	// When machine configs differ but pivot fails we should get the expected error.
	if err := d.updateOS(mcfg, differentMcfg); err == expectedError {
		t.Error("Expected an error. Got none.")
	}
}

// TestReconcilable attempts to verify the conditions in which configs would and would not be
// reconcilable. Welcome to the longest unittest you've ever read.
func TestReconcilable(t *testing.T) {
	// checkReconcilableResults is a shortcut for verifying results that should be reconcilable
	checkReconcilableResults := func(key string, reconcilableError *string) {

		if reconcilableError != nil {
			t.Errorf("Expected the same %s values would be reconcilable. Received error: %v", key, *reconcilableError)
		}
	}

	// checkIreconcilableResults is a shortcut for verifing results that should be ireconcilable
	checkIreconcilableResults := func(key string, reconcilableError *string) {

		if reconcilableError == nil {
			t.Errorf("Expected different %s values would not be reconcilable.", key)
		}
	}

	d := Daemon{
		name:              "nodeName",
		OperatingSystem:   MachineConfigDaemonOSRHCOS,
		NodeUpdaterClient: nil,
		loginClient:       nil,
		client:            nil,
		kubeClient:        nil,
		rootMount:         "/",
		bootedOSImageURL:  "test",
	}

	// oldConfig is the current config of the fake system
	oldConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "0.0",
				},
			},
		},
	}

	// newConfig is the config that is being requested to apply to the system
	newConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: ignv2_2types.Config{
				Ignition: ignv2_2types.Ignition{
					Version: "1.0",
				},
			},
		},
	}

	// Verify Ignition version changes react as expected
	isReconcilable := d.reconcilable(oldConfig, newConfig)
	checkIreconcilableResults("ignition", isReconcilable)

	// Match ignition versions
	oldConfig.Spec.Config.Ignition.Version = "1.0"
	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults("ignition", isReconcilable)

	// Verify Networkd unit changes react as expected
	oldConfig.Spec.Config.Networkd = ignv2_2types.Networkd{}
	newConfig.Spec.Config.Networkd = ignv2_2types.Networkd{
		Units: []ignv2_2types.Networkdunit{
			ignv2_2types.Networkdunit{
				Name: "test",
			},
		},
	}
	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkIreconcilableResults("networkd", isReconcilable)

	// Match Networkd
	oldConfig.Spec.Config.Networkd = newConfig.Spec.Config.Networkd

	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults("networkd", isReconcilable)

	// Verify Disk changes react as expected
	oldConfig.Spec.Config.Storage.Disks = []ignv2_2types.Disk{
		ignv2_2types.Disk{
			Device: "one",
		},
	}

	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkIreconcilableResults("disk", isReconcilable)

	// Match storage disks
	newConfig.Spec.Config.Storage.Disks = oldConfig.Spec.Config.Storage.Disks
	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults("disk", isReconcilable)

	// Verify Filesystems changes react as expected
	oldConfig.Spec.Config.Storage.Filesystems = []ignv2_2types.Filesystem{
		ignv2_2types.Filesystem{
			Name: "test",
		},
	}

	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkIreconcilableResults("filesystem", isReconcilable)

	// Match Storage filesystems
	newConfig.Spec.Config.Storage.Filesystems = oldConfig.Spec.Config.Storage.Filesystems
	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults("filesystem", isReconcilable)

	// Verify Raid changes react as expected
	oldConfig.Spec.Config.Storage.Raid = []ignv2_2types.Raid{
		ignv2_2types.Raid{
			Name: "test",
		},
	}

	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkIreconcilableResults("raid", isReconcilable)

	// Match storage raid
	newConfig.Spec.Config.Storage.Raid = oldConfig.Spec.Config.Storage.Raid
	isReconcilable = d.reconcilable(oldConfig, newConfig)
	checkReconcilableResults("raid", isReconcilable)
}
