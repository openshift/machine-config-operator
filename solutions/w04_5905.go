// pkg/daemon/update.go

package daemon

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/rpmostree"
	"github.com/openshift/machine-config-operator/pkg/daemon/update"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/vincent-petithory/dataurl"
)

// needsExtensionsForUpdate determines if we need to pull/extract extensions for an OS update.
// We only need extensions when:
// 1. The old config had extensions that are not in the new config (cleanup needed)
// 2. The new config has extensions that are not in the old config (new extensions to install)
// 3. Extensions are being used in the current boot (e.g., layered packages)
func needsExtensionsForUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// If either config has no extensions, we still need to check the other
	oldExtensions := getExtensionsFromConfig(oldConfig)
	newExtensions := getExtensionsFromConfig(newConfig)

	// If both are empty, no extensions needed
	if len(oldExtensions) == 0 && len(newExtensions) == 0 {
		return false
	}

	// Check if extensions changed between old and new configs
	if !extensionsEqual(oldExtensions, newExtensions) {
		return true
	}

	// Check if we have any currently used extensions that need to be maintained
	// This handles the case where a layered package was added and the yum repo isn't available
	currentExtensions := getCurrentBootExtensions()
	if len(currentExtensions) > 0 {
		// We need to ensure current extensions are still available
		return true
	}

	return false
}

// getExtensionsFromConfig extracts the list of extensions from a MachineConfig
func getExtensionsFromConfig(config *mcfgv1.MachineConfig) []string {
	if config == nil || config.Spec.Extensions == nil {
		return nil
	}
	return config.Spec.Extensions
}

// extensionsEqual checks if two extension lists are equal (order doesn't matter)
func extensionsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	extMap := make(map[string]bool, len(a))
	for _, ext := range a {
		extMap[ext] = true
	}
	for _, ext := range b {
		if !extMap[ext] {
			return false
		}
	}
	return true
}

// getCurrentBootExtensions returns the extensions that are currently in use on the booted system
func getCurrentBootExtensions() []string {
	// This would typically read from the current deployment's metadata
	// For now, we return an empty list as a placeholder
	// In production, this would check /run/ostree-booted or similar
	return nil
}

// updateOS handles the OS update process, including extension management
func (dn *Daemon) updateOS(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	glog.V(2).Info("Starting OS update process")

	// Check if we need to handle extensions
	if needsExtensionsForUpdate(oldConfig, newConfig) {
		glog.V(2).Info("Extensions need to be pulled/extracted for this update")
		if err := dn.handleExtensionsForUpdate(oldConfig, newConfig); err != nil {
			return fmt.Errorf("failed to handle extensions: %w", err)
		}
	} else {
		glog.V(2).Info("No extension changes detected, skipping extension pull/extract")
	}

	// Proceed with the actual OS update
	if err := dn.performOSUpdate(newConfig); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	return nil
}

// handleExtensionsForUpdate manages the extension pull/extract process
func (dn *Daemon) handleExtensionsForUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	oldExtensions := getExtensionsFromConfig(oldConfig)
	newExtensions := getExtensionsFromConfig(newConfig)

	// Determine which extensions to pull (new ones not in old)
	extensionsToPull := getNewExtensions(oldExtensions, newExtensions)
	if len(extensionsToPull) > 0 {
		glog.V(2).Infof("Pulling new extensions: %v", extensionsToPull)
		if err := dn.pullExtensions(extensionsToPull); err != nil {
			return fmt.Errorf("failed to pull extensions: %w", err)
		}
	}

	// Determine which extensions to remove (old ones not in new)
	extensionsToRemove := getRemovedExtensions(oldExtensions, newExtensions)
	if len(extensionsToRemove) > 0 {
		glog.V(2).Infof("Removing old extensions: %v", extensionsToRemove)
		if err := dn.removeExtensions(extensionsToRemove); err != nil {
			return fmt.Errorf("failed to remove extensions: %w", err)
		}
	}

	return nil
}

// getNewExtensions returns extensions that are in new but not in old
func getNewExtensions(old, new []string) []string {
	oldSet := make(map[string]bool, len(old))
	for _, ext := range old {
		oldSet[ext] = true
	}
	var result []string
	for _, ext := range new {
		if !oldSet[ext] {
			result = append(result, ext)
		}
	}
	return result
}

// getRemovedExtensions returns extensions that are in old but not in new
func getRemovedExtensions(old, new []string) []string {
	newSet := make(map[string]bool, len(new))
	for _, ext := range new {
		newSet[ext] = true
	}
	var result []string
	for _, ext := range old {
		if !newSet[ext] {
			result = append(result, ext)
		}
	}
	return result
}

// pullExtensions pulls the specified extensions
func (dn *Daemon) pullExtensions(extensions []string) error {
	// Implementation would use rpm-ostree or similar to pull extensions
	glog.V(2).Infof("Pulling extensions: %v", extensions)
	return nil
}

// removeExtensions removes the specified extensions
func (dn *Daemon) removeExtensions(extensions []string) error {
	// Implementation would use rpm-ostree or similar to remove extensions
	glog.V(2).Infof("Removing extensions: %v", extensions)
	return nil
}

// performOSUpdate performs the actual OS update
func (dn *Daemon) performOSUpdate(newConfig *mcfgv1.MachineConfig) error {
	// Implementation would use rpm-ostree to perform the update
	glog.V(2).Info("Performing OS update")
	return nil
}
