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
// 1. The new config has extensions that are not in the old config (new extensions added)
// 2. The old config had extensions that are not in the new config (extensions removed, need cleanup)
// 3. Both configs have extensions but they differ (extensions changed)
// If both configs have the same extensions, we don't need to pull/extract again.
func shouldPullExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// If either config is nil, we need to pull extensions
	if oldConfig == nil || newConfig == nil {
		return true
	}

	// Get extensions from both configs
	oldExtensions := getExtensions(oldConfig)
	newExtensions := getExtensions(newConfig)

	// If both are empty, no need to pull
	if len(oldExtensions) == 0 && len(newExtensions) == 0 {
		return false
	}

	// If one is empty and the other isn't, we need to pull
	if len(oldExtensions) == 0 || len(newExtensions) == 0 {
		return true
	}

	// If the sets are different, we need to pull
	if !extensionSetsEqual(oldExtensions, newExtensions) {
		return true
	}

	// If the sets are the same, check if we have the extensions locally
	// This handles the case where extensions were previously pulled but then removed
	if !extensionsExistLocally(newExtensions) {
		return true
	}

	return false
}

// getExtensions returns the list of extensions from a MachineConfig
func getExtensions(mc *mcfgv1.MachineConfig) []string {
	if mc == nil || mc.Spec.Extensions == nil {
		return []string{}
	}
	return mc.Spec.Extensions
}

// extensionSetsEqual checks if two extension sets are equal (order doesn't matter)
func extensionSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	set := make(map[string]bool)
	for _, ext := range a {
		set[ext] = true
	}

	for _, ext := range b {
		if !set[ext] {
			return false
		}
	}

	return true
}

// extensionsExistLocally checks if all specified extensions exist in the local filesystem
func extensionsExistLocally(extensions []string) bool {
	extensionsDir := constants.MachineConfigExtensionsPath
	if _, err := os.Stat(extensionsDir); os.IsNotExist(err) {
		return false
	}

	for _, ext := range extensions {
		extPath := filepath.Join(extensionsDir, ext)
		if _, err := os.Stat(extPath); os.IsNotExist(err) {
			glog.V(4).Infof("Extension %s not found locally at %s", ext, extPath)
			return false
		}
	}

	return true
}

// updateOS performs the OS update, conditionally pulling extensions
func (dn *Daemon) updateOS(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// Determine if we need to pull/extract extensions
	needExtensions := shouldPullExtensions(oldConfig, newConfig)

	if needExtensions {
		glog.Info("Extensions need to be pulled/extracted for this OS update")
		if err := dn.pullAndExtractExtensions(newConfig); err != nil {
			return fmt.Errorf("failed to pull/extract extensions: %w", err)
		}
	} else {
		glog.Info("Extensions are already present and unchanged, skipping pull/extract")
	}

	// Perform the actual OS update
	if err := dn.performOSUpdate(newConfig); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	return nil
}

// pullAndExtractExtensions pulls and extracts extensions for the given config
func (dn *Daemon) pullAndExtractExtensions(config *mcfgv1.MachineConfig) error {
	extensions := getExtensions(config)
	if len(extensions) == 0 {
		glog.V(4).Info("No extensions to pull/extract")
		return nil
	}

	glog.Infof("Pulling and extracting %d extensions", len(extensions))
	
	// Implementation of extension pulling and extraction
	// This would involve:
	// 1. Pulling extension containers/images
	// 2. Extracting them to the appropriate location
	// 3. Verifying the extensions are properly placed
	
	for _, ext := range extensions {
		glog.V(4).Infof("Processing extension: %s", ext)
		// Actual extension processing logic here
	}

	return nil
}

// performOSUpdate performs the actual OS update
func (dn *Daemon) performOSUpdate(config *mcfgv1.MachineConfig) error {
	glog.Info("Performing OS update")
	// Actual OS update logic here
	return nil
}
