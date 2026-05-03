// pkg/daemon/update.go

package daemon

import (
	"fmt"
	"path/filepath"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

// needsExtensionsUpdate checks if the extensions configuration has changed between old and new configs.
// Returns true if extensions need to be pulled/extracted locally.
func needsExtensionsUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	if oldConfig == nil || newConfig == nil {
		return true
	}

	// Check if extensions list has changed
	oldExtensions := oldConfig.Spec.Extensions
	newExtensions := newConfig.Spec.Extensions

	if len(oldExtensions) != len(newExtensions) {
		return true
	}

	extMap := make(map[string]bool)
	for _, ext := range oldExtensions {
		extMap[ext] = true
	}

	for _, ext := range newExtensions {
		if !extMap[ext] {
			return true
		}
	}

	// Check if kernel arguments changed (which might affect extension compatibility)
	oldKernelArgs := oldConfig.Spec.KernelArguments
	newKernelArgs := newConfig.Spec.KernelArguments

	if len(oldKernelArgs) != len(newKernelArgs) {
		return true
	}

	argMap := make(map[string]bool)
	for _, arg := range oldKernelArgs {
		argMap[arg] = true
	}

	for _, arg := range newKernelArgs {
		if !argMap[arg] {
			return true
		}
	}

	// Check if OS image changed (which would require re-extraction)
	if oldConfig.Spec.OSImageURL != newConfig.Spec.OSImageURL {
		return true
	}

	return false
}

// shouldPullExtensions determines if we need to pull/extract extensions for this update.
// We only need extensions locally when:
// 1. They are going to be used (new config has extensions)
// 2. They had previously been used (old config had extensions)
// 3. The extension configuration has changed between old and new
func shouldPullExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// If neither config has extensions, no need to pull
	if (oldConfig == nil || len(oldConfig.Spec.Extensions) == 0) &&
		(newConfig == nil || len(newConfig.Spec.Extensions) == 0) {
		return false
	}

	// If new config has extensions, we need them
	if newConfig != nil && len(newConfig.Spec.Extensions) > 0 {
		return true
	}

	// If old config had extensions and new config doesn't, we might still need them
	// for rollback scenarios, but we can skip pulling new ones
	if oldConfig != nil && len(oldConfig.Spec.Extensions) > 0 {
		return false
	}

	// If extensions configuration changed, we need to update
	return needsExtensionsUpdate(oldConfig, newConfig)
}

// updateOS updates the OS to the new config, handling extensions intelligently.
func (dn *Daemon) updateOS(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// Determine if we need to pull/extract extensions
	pullExtensions := shouldPullExtensions(oldConfig, newConfig)

	if pullExtensions {
		glog.Info("Extensions configuration changed or needed, pulling/extracting extensions")
		if err := dn.pullAndExtractExtensions(newConfig); err != nil {
			return fmt.Errorf("failed to pull/extract extensions: %w", err)
		}
	} else {
		glog.Info("Extensions configuration unchanged, skipping extension pull/extract")
	}

	// Perform the OS update
	if err := dn.performOSUpdate(newConfig); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	return nil
}

// pullAndExtractExtensions pulls and extracts extensions for the given config.
func (dn *Daemon) pullAndExtractExtensions(config *mcfgv1.MachineConfig) error {
	if config == nil || len(config.Spec.Extensions) == 0 {
		return nil
	}

	// Pull extensions from the configured repositories
	for _, ext := range config.Spec.Extensions {
		glog.Infof("Pulling extension: %s", ext)
		if err := dn.pullExtension(ext); err != nil {
			return fmt.Errorf("failed to pull extension %s: %w", ext, err)
		}
	}

	// Extract extensions to the local filesystem
	extractDir := filepath.Join(constants.MachineConfigDir, "extensions")
	for _, ext := range config.Spec.Extensions {
		glog.Infof("Extracting extension: %s", ext)
		if err := dn.extractExtension(ext, extractDir); err != nil {
			return fmt.Errorf("failed to extract extension %s: %w", ext, err)
		}
	}

	return nil
}

// performOSUpdate performs the actual OS update using rpm-ostree.
func (dn *Daemon) performOSUpdate(config *mcfgv1.MachineConfig) error {
	// Implementation of OS update logic
	// This would typically call rpm-ostree commands
	glog.Infof("Performing OS update to config: %s", config.Name)
	return nil
}

// pullExtension pulls a single extension from the repository.
func (dn *Daemon) pullExtension(extension string) error {
	// Implementation of extension pulling logic
	glog.Infof("Pulling extension: %s", extension)
	return nil
}

// extractExtension extracts a single extension to the specified directory.
func (dn *Daemon) extractExtension(extension, destDir string) error {
	// Implementation of extension extraction logic
	glog.Infof("Extracting extension %s to %s", extension, destDir)
	return nil
}
