package daemon

import (
	"context"
	"fmt"

	"github.com/coreos/rpmostree-client-go/pkg/capi"
	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/ocm"
	"github.com/openshift/machine-config-operator/pkg/daemon/rpmostree"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/daemon/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/update"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

// updateOS updates the OS to the target config, only pulling/extracting extensions when necessary
func (dn *Daemon) updateOS(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	glog.Info("Updating OS")

	// Check if extensions need to be pulled/extracted
	needsExtensions := dn.needsExtensionsForUpdate(oldConfig, newConfig)

	if needsExtensions {
		glog.Info("Extensions required for this update, pulling/extracting")
		if err := dn.pullAndExtractExtensions(newConfig); err != nil {
			return fmt.Errorf("failed to pull/extract extensions: %w", err)
		}
	} else {
		glog.Info("No extensions needed for this update, skipping pull/extract")
	}

	// Perform the OS update
	if err := dn.performOSUpdate(newConfig); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	return nil
}

// needsExtensionsForUpdate determines if extensions need to be pulled/extracted for the update
func (dn *Daemon) needsExtensionsForUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// If there's no old config, we need extensions
	if oldConfig == nil {
		return true
	}

	// Check if extensions changed between old and new configs
	oldExtensions := oldConfig.Spec.Extensions
	newExtensions := newConfig.Spec.Extensions

	// If extensions are different, we need to pull/extract
	if !extensionSlicesEqual(oldExtensions, newExtensions) {
		return true
	}

	// Check if any other OS-related changes require extensions
	if dn.osConfigChanged(oldConfig, newConfig) {
		return true
	}

	return false
}

// extensionSlicesEqual checks if two extension slices are equal
func extensionSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// osConfigChanged checks if the OS configuration changed between old and new configs
func (dn *Daemon) osConfigChanged(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// Check kernel arguments
	if !kernelArgsEqual(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments) {
		return true
	}

	// Check kernel type
	if oldConfig.Spec.KernelType != newConfig.Spec.KernelType {
		return true
	}

	// Check if OS image URL changed
	if oldConfig.Spec.OSImageURL != newConfig.Spec.OSImageURL {
		return true
	}

	return false
}

// kernelArgsEqual checks if two kernel argument slices are equal
func kernelArgsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pullAndExtractExtensions pulls and extracts extensions for the given config
func (dn *Daemon) pullAndExtractExtensions(config *mcfgv1.MachineConfig) error {
	glog.Infof("Pulling and extracting extensions for config %s", config.Name)

	// Get the list of extensions from the config
	extensions := config.Spec.Extensions
	if len(extensions) == 0 {
		glog.Info("No extensions to pull/extract")
		return nil
	}

	// Use rpm-ostree to pull/extract extensions
	client, err := rpmostree.NewClient()
	if err != nil {
		return fmt.Errorf("failed to create rpm-ostree client: %w", err)
	}
	defer client.Close()

	for _, ext := range extensions {
		glog.Infof("Processing extension: %s", ext)
		if err := client.PullAndExtend(context.TODO(), ext); err != nil {
			return fmt.Errorf("failed to pull/extract extension %s: %w", ext, err)
		}
	}

	return nil
}

// performOSUpdate performs the actual OS update
func (dn *Daemon) performOSUpdate(config *mcfgv1.MachineConfig) error {
	glog.Infof("Performing OS update to config %s", config.Name)

	// Use rpm-ostree to perform the update
	client, err := rpmostree.NewClient()
	if err != nil {
		return fmt.Errorf("failed to create rpm-ostree client: %w", err)
	}
	defer client.Close()

	if err := client.Update(context.TODO(), config.Spec.OSImageURL); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	return nil
}
