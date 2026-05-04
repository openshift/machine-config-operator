package daemon

import (
	"fmt"
	"path/filepath"

	"github.com/coreos/rpmostree-client-go/pkg/api"
	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/ocm"
	"github.com/openshift/machine-config-operator/pkg/daemon/rpmostree"
	"github.com/openshift/machine-config-operator/pkg/daemon/rpmostreeclient"
)

// extensionsNeededForUpdate checks if extensions need to be pulled/extracted for an OS update.
// Returns true if the old and new configs have different extensions or if extensions were previously used.
func extensionsNeededForUpdate(oldConfig, newConfig *api.MachineConfig) bool {
	// If there's no old config (first boot), we need extensions
	if oldConfig == nil {
		return true
	}

	// Check if extensions differ between old and new configs
	oldExtensions := getExtensionsFromConfig(oldConfig)
	newExtensions := getExtensionsFromConfig(newConfig)

	if !extensionsEqual(oldExtensions, newExtensions) {
		return true
	}

	// Check if extensions were previously used (e.g., from a previous deployment)
	// This handles the case where extensions were layered but the config hasn't changed
	if wereExtensionsPreviouslyUsed() {
		return true
	}

	return false
}

// getExtensionsFromConfig extracts the list of extensions from a MachineConfig
func getExtensionsFromConfig(config *api.MachineConfig) []string {
	if config == nil || config.Spec.Extensions == nil {
		return []string{}
	}
	return config.Spec.Extensions
}

// extensionsEqual checks if two extension lists are equal
func extensionsEqual(a, b []string) bool {
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

// wereExtensionsPreviouslyUsed checks if extensions were used in a previous deployment
// This is a simplified check - in production, you'd check the actual deployment state
func wereExtensionsPreviouslyUsed() bool {
	// Check if there are any layered packages or extensions in the current deployment
	// This would involve checking the RPM-OSTree deployment state
	// For now, we return false as a placeholder
	return false
}

// handleOSUpdate handles an OS update, only pulling/extracting extensions if needed
func handleOSUpdate(oldConfig, newConfig *api.MachineConfig) error {
	if !extensionsNeededForUpdate(oldConfig, newConfig) {
		glog.Info("Extensions not needed for this OS update, skipping pull/extract")
		return nil
	}

	glog.Info("Extensions needed for this OS update, proceeding with pull/extract")

	// Pull and extract extensions
	if err := pullExtensions(newConfig); err != nil {
		return fmt.Errorf("failed to pull extensions: %w", err)
	}

	if err := extractExtensions(newConfig); err != nil {
		return fmt.Errorf("failed to extract extensions: %w", err)
	}

	return nil
}

// pullExtensions pulls the extensions for the given config
func pullExtensions(config *api.MachineConfig) error {
	// Implementation would use RPM-OSTree client to pull extensions
	// This is a placeholder
	return nil
}

// extractExtensions extracts the extensions for the given config
func extractExtensions(config *api.MachineConfig) error {
	// Implementation would use RPM-OSTree client to extract extensions
	// This is a placeholder
	return nil
}

// In the main update flow, replace the old logic with the new smart check
func updateOS(oldConfig, newConfig *api.MachineConfig) error {
	// ... existing update logic ...

	// Only pull/extract extensions if needed
	if err := handleOSUpdate(oldConfig, newConfig); err != nil {
		return fmt.Errorf("failed to handle OS update extensions: %w", err)
	}

	// ... rest of update logic ...
	return nil
}
