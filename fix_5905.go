package daemon

import (
	"fmt"
	"strings"

	"github.com/coreos/rpmostree-client-go/pkg/rpmostree"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// shouldPullExtensions determines if we need to pull/extract extensions for an OS update
func shouldPullExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// If either config has no OS update, we don't need extensions
	if oldConfig == nil || newConfig == nil {
		return false
	}

	// Get the OS update URLs
	oldOSUpdateURL := getOSUpdateURL(oldConfig)
	newOSUpdateURL := getOSUpdateURL(newConfig)

	// If there's no OS update in either config, no extensions needed
	if oldOSUpdateURL == "" && newOSUpdateURL == "" {
		return false
	}

	// If the OS update URL hasn't changed, check if extensions changed
	if oldOSUpdateURL == newOSUpdateURL {
		return haveExtensionsChanged(oldConfig, newConfig)
	}

	// OS update URL changed, check if we need extensions
	return needExtensionsForUpdate(oldConfig, newConfig)
}

// getOSUpdateURL extracts the OS update URL from a MachineConfig
func getOSUpdateURL(config *mcfgv1.MachineConfig) string {
	if config.Spec.OSUpdateURL != nil {
		return *config.Spec.OSUpdateURL
	}
	return ""
}

// haveExtensionsChanged checks if the extensions configuration has changed
func haveExtensionsChanged(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	oldExtensions := getExtensions(oldConfig)
	newExtensions := getExtensions(newConfig)

	// If both have no extensions, no change
	if len(oldExtensions) == 0 && len(newExtensions) == 0 {
		return false
	}

	// If one has extensions and the other doesn't, change occurred
	if len(oldExtensions) != len(newExtensions) {
		return true
	}

	// Compare extension lists
	oldExtMap := make(map[string]bool)
	for _, ext := range oldExtensions {
		oldExtMap[ext] = true
	}

	for _, ext := range newExtensions {
		if !oldExtMap[ext] {
			return true
		}
	}

	return false
}

// needExtensionsForUpdate determines if we need extensions for a new OS update
func needExtensionsForUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// Check if the new config has extensions
	newExtensions := getExtensions(newConfig)
	if len(newExtensions) > 0 {
		return true
	}

	// Check if the old config had extensions that might still be needed
	oldExtensions := getExtensions(oldConfig)
	if len(oldExtensions) > 0 {
		// Check if the old extensions are still relevant
		return areOldExtensionsStillRelevant(oldConfig, newConfig)
	}

	return false
}

// getExtensions extracts the list of extensions from a MachineConfig
func getExtensions(config *mcfgv1.MachineConfig) []string {
	if config.Spec.Extensions != nil {
		return config.Spec.Extensions
	}
	return []string{}
}

// areOldExtensionsStillRelevant checks if old extensions are still needed after an update
func areOldExtensionsStillRelevant(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// This is a simplified check - in reality, you'd want to check if the
	// extensions are still available in the new OS update or if they've been
	// superseded

	oldExtensions := getExtensions(oldConfig)
	newExtensions := getExtensions(newConfig)

	// Create a set of new extensions for quick lookup
	newExtSet := make(map[string]bool)
	for _, ext := range newExtensions {
		newExtSet[ext] = true
	}

	// Check if any old extension is not in the new set
	for _, ext := range oldExtensions {
		if !newExtSet[ext] {
			// This extension is no longer explicitly listed, but might still be needed
			// Check if it's a system extension that should persist
			if isSystemExtension(ext) {
				return true
			}
		}
	}

	return false
}

// isSystemExtension checks if an extension is a system-level extension that should persist
func isSystemExtension(extension string) bool {
	// List of system extensions that should always be available
	systemExtensions := map[string]bool{
		"usbguard":     true,
		"selinux":      true,
		"kernel-devel": true,
		"kernel-headers": true,
		"glibc":        true,
		"systemd":      true,
	}

	return systemExtensions[extension]
}

// handleOSUpdate handles the OS update process with smart extension management
func (dn *Daemon) handleOSUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	glog.V(4).Infof("Checking if extensions need to be pulled for OS update")

	if !shouldPullExtensions(oldConfig, newConfig) {
		glog.V(4).Infof("No extensions needed for this OS update, skipping pull/extract")
		return nil
	}

	glog.V(4).Infof("Extensions needed for OS update, proceeding with pull/extract")

	// Get the list of extensions to pull
	extensions := getExtensions(newConfig)
	if len(extensions) == 0 && oldConfig != nil {
		extensions = getExtensions(oldConfig)
	}

	// Pull and extract the extensions
	for _, ext := range extensions {
		if err := dn.pullAndExtractExtension(ext); err != nil {
			return fmt.Errorf("failed to pull/extract extension %s: %w", ext, err)
		}
	}

	return nil
}

// pullAndExtractExtension pulls and extracts a single extension
func (dn *Daemon) pullAndExtractExtension(extension string) error {
	glog.V(4).Infof("Pulling and extracting extension: %s", extension)

	// This is a placeholder for the actual implementation
	// In reality, this would use rpm-ostree to pull and extract the extension
	if err := dn.rpmOstreeClient.PullExtension(extension); err != nil {
		return fmt.Errorf("failed to pull extension %s: %w", extension, err)
	}

	if err := dn.rpmOstreeClient.ExtractExtension(extension); err != nil {
		return fmt.Errorf("failed to extract extension %s: %w", extension, err)
	}

	return nil
}
