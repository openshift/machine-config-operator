// pkg/daemon/update.go

package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// shouldPullExtensions determines if we need to pull/extract extensions for an OS update.
// We only need extensions when:
// 1. The old config had extensions that are no longer in the new config (cleanup needed)
// 2. The new config has extensions that weren't in the old config (new extensions to pull)
// 3. Both configs have extensions but the sets differ (some changed)
// Returns true if extensions need to be pulled/extracted.
func shouldPullExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	if oldConfig == nil || newConfig == nil {
		return false
	}

	oldExtensions := getExtensionSet(oldConfig)
	newExtensions := getExtensionSet(newConfig)

	// If both are empty, no extensions needed
	if oldExtensions.Len() == 0 && newExtensions.Len() == 0 {
		return false
	}

	// If old had extensions but new doesn't, we need to clean up
	if oldExtensions.Len() > 0 && newExtensions.Len() == 0 {
		return true
	}

	// If new has extensions but old didn't, we need to pull them
	if oldExtensions.Len() == 0 && newExtensions.Len() > 0 {
		return true
	}

	// Both have extensions - check if they differ
	return !oldExtensions.Equal(newExtensions)
}

// getExtensionSet returns a set of extension names from a MachineConfig
func getExtensionSet(mc *mcfgv1.MachineConfig) sets.String {
	extensions := sets.NewString()
	if mc.Spec.Extensions != nil {
		for _, ext := range mc.Spec.Extensions {
			extensions.Insert(ext)
		}
	}
	return extensions
}

// updateOS handles OS updates, only pulling extensions when necessary
func (dn *Daemon) updateOS(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// Check if we need to handle extensions
	needExtensions := shouldPullExtensions(oldConfig, newConfig)

	if needExtensions {
		glog.Info("Extensions configuration changed, pulling/extracting extensions")
		if err := dn.pullAndExtractExtensions(newConfig); err != nil {
			return fmt.Errorf("failed to pull/extract extensions: %w", err)
		}
	} else {
		glog.Info("Extensions configuration unchanged, skipping extension pull/extract")
	}

	// Proceed with OS update
	if err := dn.performOSUpdate(newConfig); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	return nil
}

// pullAndExtractExtensions pulls and extracts extensions for the given config
func (dn *Daemon) pullAndExtractExtensions(config *mcfgv1.MachineConfig) error {
	if config.Spec.Extensions == nil || len(config.Spec.Extensions) == 0 {
		return nil
	}

	extensionsDir := filepath.Join(constants.MachineConfigDir, "extensions")
	if err := os.MkdirAll(extensionsDir, 0755); err != nil {
		return fmt.Errorf("failed to create extensions directory: %w", err)
	}

	for _, ext := range config.Spec.Extensions {
		glog.Infof("Pulling extension: %s", ext)
		if err := dn.pullExtension(ext, extensionsDir); err != nil {
			return fmt.Errorf("failed to pull extension %s: %w", ext, err)
		}

		glog.Infof("Extracting extension: %s", ext)
		if err := dn.extractExtension(ext, extensionsDir); err != nil {
			return fmt.Errorf("failed to extract extension %s: %w", ext, err)
		}
	}

	return nil
}

// performOSUpdate performs the actual OS update
func (dn *Daemon) performOSUpdate(config *mcfgv1.MachineConfig) error {
	// Implementation of OS update logic
	glog.Info("Performing OS update")
	// ... existing OS update code ...
	return nil
}

// pullExtension pulls a single extension
func (dn *Daemon) pullExtension(extension, destDir string) error {
	// Implementation of extension pulling
	glog.Infof("Pulling extension %s to %s", extension, destDir)
	// ... existing pull logic ...
	return nil
}

// extractExtension extracts a single extension
func (dn *Daemon) extractExtension(extension, destDir string) error {
	// Implementation of extension extraction
	glog.Infof("Extracting extension %s to %s", extension, destDir)
	// ... existing extraction logic ...
	return nil
}
