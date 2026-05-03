package daemon

import (
	"fmt"
	"sort"

	"github.com/coreos/rpmostree-client-go/pkg/api"
	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/update"
	"github.com/vincent-petithory/dataurl"
)

// checkExtensionsNeeded determines if extensions need to be pulled/extracted
// for an OS update by comparing old and new configs.
func checkExtensionsNeeded(oldConfig, newConfig *api.MachineConfig) (bool, error) {
	// If either config has no extensions, we don't need to pull/extract
	if oldConfig == nil || newConfig == nil {
		return false, nil
	}

	// Get old and new extension lists
	oldExtensions := getExtensionsFromConfig(oldConfig)
	newExtensions := getExtensionsFromConfig(newConfig)

	// If both are empty, no need to pull/extract
	if len(oldExtensions) == 0 && len(newExtensions) == 0 {
		return false, nil
	}

	// Check if extensions have changed
	if !extensionsEqual(oldExtensions, newExtensions) {
		return true, nil
	}

	// Check if any existing extensions need to be updated
	for _, ext := range oldExtensions {
		needsUpdate, err := extensionNeedsUpdate(ext, newConfig)
		if err != nil {
			return false, fmt.Errorf("failed to check if extension %s needs update: %w", ext.Name, err)
		}
		if needsUpdate {
			return true, nil
		}
	}

	return false, nil
}

// getExtensionsFromConfig extracts extension names from a MachineConfig
func getExtensionsFromConfig(config *api.MachineConfig) []api.Extension {
	if config.Spec.Extensions == nil {
		return nil
	}
	return config.Spec.Extensions
}

// extensionsEqual checks if two extension lists are equal
func extensionsEqual(a, b []api.Extension) bool {
	if len(a) != len(b) {
		return false
	}

	sort.Slice(a, func(i, j int) bool { return a[i].Name < a[j].Name })
	sort.Slice(b, func(i, j int) bool { return b[i].Name < b[j].Name })

	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// extensionNeedsUpdate checks if a specific extension needs to be updated
func extensionNeedsUpdate(ext api.Extension, newConfig *api.MachineConfig) (bool, error) {
	// Find the extension in the new config
	var newExt *api.Extension
	for _, e := range newConfig.Spec.Extensions {
		if e.Name == ext.Name {
			newExt = &e
			break
		}
	}

	// If extension is not in new config, it might need to be removed
	if newExt == nil {
		return true, nil
	}

	// Check if extension version or content has changed
	if ext.Version != newExt.Version {
		return true, nil
	}

	// Check if extension URL has changed
	if ext.URL != newExt.URL {
		return true, nil
	}

	// Check if extension content (if inline) has changed
	if ext.Content != nil && newExt.Content != nil {
		oldData, err := dataurl.Decode(*ext.Content)
		if err != nil {
			return false, fmt.Errorf("failed to decode old extension content: %w", err)
		}
		newData, err := dataurl.Decode(*newExt.Content)
		if err != nil {
			return false, fmt.Errorf("failed to decode new extension content: %w", err)
		}
		if string(oldData.Data) != string(newData.Data) {
			return true, nil
		}
	} else if ext.Content != nil || newExt.Content != nil {
		// One has content, the other doesn't
		return true, nil
	}

	return false, nil
}

// handleOSUpdateWithExtensions handles an OS update, only pulling/extracting
// extensions when necessary
func handleOSUpdateWithExtensions(oldConfig, newConfig *api.MachineConfig) error {
	needed, err := checkExtensionsNeeded(oldConfig, newConfig)
	if err != nil {
		return fmt.Errorf("failed to check if extensions are needed: %w", err)
	}

	if !needed {
		glog.V(4).Info("No extension changes detected, skipping extension pull/extract")
		return nil
	}

	glog.V(4).Info("Extension changes detected, pulling/extracting extensions")

	// Pull and extract extensions
	if err := update.PullExtensions(newConfig); err != nil {
		return fmt.Errorf("failed to pull extensions: %w", err)
	}

	if err := update.ExtractExtensions(newConfig); err != nil {
		return fmt.Errorf("failed to extract extensions: %w", err)
	}

	return nil
}

// applyOSUpdate applies an OS update, handling extensions intelligently
func applyOSUpdate(oldConfig, newConfig *api.MachineConfig) error {
	// Handle extensions first
	if err := handleOSUpdateWithExtensions(oldConfig, newConfig); err != nil {
		return fmt.Errorf("failed to handle extensions during OS update: %w", err)
	}

	// Apply the OS update
	if err := update.ApplyOSUpdate(newConfig); err != nil {
		return fmt.Errorf("failed to apply OS update: %w", err)
	}

	return nil
}
