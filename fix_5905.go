package daemon

import (
	"fmt"
	"strings"

	"github.com/coreos/rpmostree-client-go/pkg/rpmostree"
	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/extension"
	"github.com/openshift/machine-config-operator/pkg/daemon/ocm"
	"github.com/openshift/machine-config-operator/pkg/daemon/runtime"
	"github.com/openshift/machine-config-operator/pkg/daemon/state"
	"github.com/openshift/machine-config-operator/pkg/daemon/update"
)

// handleExtensionsForUpdate determines if extensions need to be pulled/extracted
// for a given OS update. It checks the old and new configs to see if extensions
// are actually needed, avoiding unnecessary pulls/extractions.
func (dn *Daemon) handleExtensionsForUpdate(oldConfig, newConfig *runtime.MachineConfig) error {
	// If there's no old config, this is a fresh install, so we need extensions
	if oldConfig == nil {
		glog.Info("No old config found, pulling/extracting extensions for initial setup")
		return dn.pullAndExtractExtensions(newConfig)
	}

	// Check if extensions are needed based on config differences
	extensionsNeeded := dn.areExtensionsNeededForUpdate(oldConfig, newConfig)

	if !extensionsNeeded {
		glog.Info("Extensions not needed for this update, skipping pull/extract")
		return nil
	}

	glog.Info("Extensions needed for this update, pulling/extracting")
	return dn.pullAndExtractExtensions(newConfig)
}

// areExtensionsNeededForUpdate checks if extensions are needed for the update
// by comparing old and new configs.
func (dn *Daemon) areExtensionsNeededForUpdate(oldConfig, newConfig *runtime.MachineConfig) bool {
	// Check if extensions are explicitly configured
	if !hasExtensionsConfigured(newConfig) {
		glog.V(4).Info("No extensions configured in new config")
		return false
	}

	// Check if extensions changed between old and new configs
	if extensionsChanged(oldConfig, newConfig) {
		glog.V(4).Info("Extensions changed between configs")
		return true
	}

	// Check if there are layered packages that might need extensions
	if hasLayeredPackages(newConfig) {
		glog.V(4).Info("Layered packages present, may need extensions")
		return true
	}

	// Check if the OS update itself requires extensions (e.g., for kernel modules)
	if osUpdateRequiresExtensions(oldConfig, newConfig) {
		glog.V(4).Info("OS update requires extensions")
		return true
	}

	// Check if extensions were previously used and need to be maintained
	if extensionsWerePreviouslyUsed(oldConfig) {
		glog.V(4).Info("Extensions were previously used, maintaining them")
		return true
	}

	glog.V(4).Info("No reason to pull/extract extensions for this update")
	return false
}

// hasExtensionsConfigured checks if the config has any extensions configured.
func hasExtensionsConfigured(config *runtime.MachineConfig) bool {
	if config == nil {
		return false
	}
	return len(config.Spec.Extensions) > 0 || len(config.Spec.KernelArguments) > 0
}

// extensionsChanged checks if the extensions in the old and new configs differ.
func extensionsChanged(oldConfig, newConfig *runtime.MachineConfig) bool {
	if oldConfig == nil || newConfig == nil {
		return true
	}

	// Compare extension lists
	oldExtensions := oldConfig.Spec.Extensions
	newExtensions := newConfig.Spec.Extensions

	if len(oldExtensions) != len(newExtensions) {
		return true
	}

	oldSet := make(map[string]bool)
	for _, ext := range oldExtensions {
		oldSet[ext] = true
	}

	for _, ext := range newExtensions {
		if !oldSet[ext] {
			return true
		}
	}

	return false
}

// hasLayeredPackages checks if the config has any layered packages.
func hasLayeredPackages(config *runtime.MachineConfig) bool {
	if config == nil {
		return false
	}
	return len(config.Spec.LayeredPackages) > 0
}

// osUpdateRequiresExtensions checks if the OS update itself requires extensions.
func osUpdateRequiresExtensions(oldConfig, newConfig *runtime.MachineConfig) bool {
	if oldConfig == nil || newConfig == nil {
		return true
	}

	// Check if kernel arguments changed (may require kernel module extensions)
	if !strings.EqualFold(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments) {
		return true
	}

	// Check if the OS image changed significantly
	if oldConfig.Spec.OSImageURL != newConfig.Spec.OSImageURL {
		// Check if the new OS image has different kernel or base packages
		// that might require extensions
		oldOSRelease := getOSReleaseFromConfig(oldConfig)
		newOSRelease := getOSReleaseFromConfig(newConfig)
		if oldOSRelease != newOSRelease {
			return true
		}
	}

	return false
}

// extensionsWerePreviouslyUsed checks if extensions were used in the old config.
func extensionsWerePreviouslyUsed(oldConfig *runtime.MachineConfig) bool {
	if oldConfig == nil {
		return false
	}

	// Check if extensions were previously installed
	if len(oldConfig.Spec.Extensions) > 0 {
		return true
	}

	// Check if there are any extension-related state files
	return dn.extensionStateExists()
}

// extensionStateExists checks if there are any extension-related state files.
func (dn *Daemon) extensionStateExists() bool {
	// Check for extension state files in the daemon's state directory
	stateDir := dn.stateDir
	if stateDir == "" {
		stateDir = constants.DefaultStateDir
	}

	extensionStateFile := fmt.Sprintf("%s/extensions.state", stateDir)
	return dn.fileExists(extensionStateFile)
}

// pullAndExtractExtensions pulls and extracts extensions for the given config.
func (dn *Daemon) pullAndExtractExtensions(config *runtime.MachineConfig) error {
	if config == nil {
		return fmt.Errorf("cannot pull/extract extensions for nil config")
	}

	glog.Infof("Pulling and extracting extensions for config %s", config.GetName())

	// Get the list of extensions to pull
	extensions := config.Spec.Extensions
	if len(extensions) == 0 {
		glog.Info("No extensions to pull")
		return nil
	}

	// Pull extensions from the configured sources
	for _, ext := range extensions {
		if err := dn.pullExtension(ext); err != nil {
			return fmt.Errorf("failed to pull extension %s: %w", ext, err)
		}
	}

	// Extract extensions to the appropriate location
	if err := dn.extractExtensions(extensions); err != nil {
		return fmt.Errorf("failed to extract extensions: %w", err)
	}

	// Save extension state
	if err := dn.saveExtensionState(config); err != nil {
		return fmt.Errorf("failed to save extension state: %w", err)
	}

	return nil
}

// pullExtension pulls a single extension from its source.
func (dn *Daemon) pullExtension(extensionName string) error {
	glog.Infof("Pulling extension %s", extensionName)
	// Implementation depends on the extension source (e.g., container registry, HTTP, etc.)
	// This is a placeholder for the actual implementation
	return nil
}

// extractExtensions extracts the pulled extensions to the appropriate location.
func (dn *Daemon) extractExtensions(extensions []string) error {
	glog.Infof("Extracting %d extensions", len(extensions))
	// Implementation depends on the extension format and target location
	// This is a placeholder for the actual implementation
	return nil
}

// saveExtensionState saves the current extension state for future reference.
func (dn *Daemon) saveExtensionState(config *runtime.MachineConfig) error {
	stateDir := dn.stateDir
	if stateDir == "" {
		stateDir = constants.DefaultStateDir
	}

	extensionStateFile := fmt.Sprintf("%s/extensions.state", stateDir)
	
	// Create a state entry for the current extensions
	state := &extension.State{
		ConfigName: config.GetName(),
		Extensions: config.Spec.Extensions,
		Timestamp:  time.Now().Unix(),
	}

	// Marshal and save the state
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal extension state: %w", err)
	}

	if err := ioutil.WriteFile(extensionStateFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write extension state file: %w", err)
	}

	return nil
}

// getOSReleaseFromConfig extracts the OS release from a machine config.
func getOSReleaseFromConfig(config *runtime.MachineConfig) string {
	if config == nil || config.Spec.OSImageURL == "" {
		return ""
	}

	// Parse the OS image URL to extract the release
	// Format: quay.io/openshift-release-dev/ocp-release@sha256:... or similar
	parts := strings.Split(config.Spec.OSImageURL, "@")
	if len(parts) >= 2 {
		return parts[1] // Return the digest
	}

	// Fallback to the full URL
	return config.Spec.OSImageURL
}

// fileExists checks if a file exists.
func (dn *Daemon) fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
