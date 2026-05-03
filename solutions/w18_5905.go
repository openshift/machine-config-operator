// pkg/daemon/update.go

package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

// shouldPullExtensions determines if we need to pull/extract extensions for an OS update.
// We only need extensions locally when:
// 1. The new config has extensions that the old config didn't have (mcDiff.extensions)
// 2. The old config had extensions that are being removed (mcDiff.removedExtensions)
// 3. The new config has extensions that are different from the old config
// 4. The old config had extensions and we're doing a full OS update (not just a config change)
func shouldPullExtensions(oldConfig, newConfig *mcfgv1.MachineConfig, mcDiff *machineConfigDiff) bool {
	// If there are no extensions in either config, no need to pull
	if len(oldConfig.Spec.Extensions) == 0 && len(newConfig.Spec.Extensions) == 0 {
		return false
	}

	// If extensions changed (added, removed, or modified), we need to pull
	if mcDiff != nil {
		if len(mcDiff.extensions) > 0 || len(mcDiff.removedExtensions) > 0 {
			glog.V(4).Infof("Extensions changed: added=%v, removed=%v", mcDiff.extensions, mcDiff.removedExtensions)
			return true
		}
	}

	// If old config had extensions and new config has different extensions
	if len(oldConfig.Spec.Extensions) > 0 && len(newConfig.Spec.Extensions) > 0 {
		if !stringSlicesEqual(oldConfig.Spec.Extensions, newConfig.Spec.Extensions) {
			glog.V(4).Infof("Extensions differ between old and new config")
			return true
		}
	}

	// If old config had extensions and we're doing a full OS update (kernel/initramfs change)
	if len(oldConfig.Spec.Extensions) > 0 && isOSUpdate(oldConfig, newConfig) {
		glog.V(4).Infof("Old config had extensions and we're doing an OS update")
		return true
	}

	return false
}

// isOSUpdate checks if the update involves a kernel or initramfs change (full OS update)
func isOSUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	if oldConfig == nil || newConfig == nil {
		return false
	}

	// Check if kernel arguments changed
	if !stringSlicesEqual(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments) {
		return true
	}

	// Check if kernel type changed
	if oldConfig.Spec.KernelType != newConfig.Spec.KernelType {
		return true
	}

	// Check if initramfs changed (via fips or other initramfs-affecting changes)
	if oldConfig.Spec.FIPS != newConfig.Spec.FIPS {
		return true
	}

	return false
}

// stringSlicesEqual checks if two string slices are equal
func stringSlicesEqual(a, b []string) bool {
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

// machineConfigDiff represents the diff between two machine configs
type machineConfigDiff struct {
	extensions        []string
	removedExtensions []string
	osUpdate          bool
}

// computeMachineConfigDiff computes the diff between old and new machine configs
func computeMachineConfigDiff(oldConfig, newConfig *mcfgv1.MachineConfig) *machineConfigDiff {
	diff := &machineConfigDiff{}

	// Compute extension changes
	oldExtensions := make(map[string]bool)
	for _, ext := range oldConfig.Spec.Extensions {
		oldExtensions[ext] = true
	}

	newExtensions := make(map[string]bool)
	for _, ext := range newConfig.Spec.Extensions {
		newExtensions[ext] = true
	}

	// Find added extensions
	for _, ext := range newConfig.Spec.Extensions {
		if !oldExtensions[ext] {
			diff.extensions = append(diff.extensions, ext)
		}
	}

	// Find removed extensions
	for _, ext := range oldConfig.Spec.Extensions {
		if !newExtensions[ext] {
			diff.removedExtensions = append(diff.removedExtensions, ext)
		}
	}

	// Check if this is an OS update
	diff.osUpdate = isOSUpdate(oldConfig, newConfig)

	return diff
}

// updateOS updates the OS and handles extensions appropriately
func (dn *Daemon) updateOS(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// Compute the diff between old and new configs
	mcDiff := computeMachineConfigDiff(oldConfig, newConfig)

	// Only pull/extract extensions if needed
	if shouldPullExtensions(oldConfig, newConfig, mcDiff) {
		glog.Info("Extensions need to be pulled/extracted for this update")
		
		// Pull extensions from the configured repositories
		if err := dn.pullExtensions(newConfig.Spec.Extensions); err != nil {
			return fmt.Errorf("failed to pull extensions: %w", err)
		}

		// Extract extensions to the local filesystem
		if err := dn.extractExtensions(newConfig.Spec.Extensions); err != nil {
			return fmt.Errorf("failed to extract extensions: %w", err)
		}
	} else {
		glog.Info("No extension changes detected, skipping extension pull/extract")
	}

	// Perform the actual OS update
	if err := dn.performOSUpdate(oldConfig, newConfig); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	return nil
}

// pullExtensions pulls the specified extensions from configured repositories
func (dn *Daemon) pullExtensions(extensions []string) error {
	if len(extensions) == 0 {
		return nil
	}

	glog.Infof("Pulling extensions: %v", extensions)
	
	// Implementation would use rpm-ostree or similar to pull extensions
	// This is a placeholder for the actual implementation
	for _, ext := range extensions {
		glog.V(4).Infof("Pulling extension: %s", ext)
		// Actual pull logic here
	}

	return nil
}

// extractExtensions extracts the specified extensions to the local filesystem
func (dn *Daemon) extractExtensions(extensions []string) error {
	if len(extensions) == 0 {
		return nil
	}

	glog.Infof("Extracting extensions: %v", extensions)
	
	// Implementation would extract extensions to the local filesystem
	// This is a placeholder for the actual implementation
	for _, ext := range extensions {
		glog.V(4).Infof("Extracting extension: %s", ext)
		// Actual extraction logic here
	}

	return nil
}

// performOSUpdate performs the actual OS update
func (dn *Daemon) performOSUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	glog.Info("Performing OS update")
	
	// Implementation would use rpm-ostree or similar to perform the update
	// This is a placeholder for the actual implementation
	
	return nil
}
