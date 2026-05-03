// pkg/daemon/update.go

package daemon

import (
	"fmt"
	"path/filepath"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/ocm"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// shouldPullExtensions determines if we need to pull and extract extensions
// for an OS update. We only need extensions locally when:
// 1. The new config has extensions that are not in the old config (new extensions added)
// 2. The old config had extensions that are not in the new config (extensions removed - need to clean up)
// 3. Both configs have extensions but they differ (extensions changed)
// If both configs have the same extensions (or both have no extensions), we don't need to pull/extract.
func shouldPullExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	oldExtensions := getExtensionsSet(oldConfig)
	newExtensions := getExtensionsSet(newConfig)

	// If both are empty, no need to pull
	if oldExtensions.Len() == 0 && newExtensions.Len() == 0 {
		return false
	}

	// If they are equal, no need to pull
	if oldExtensions.Equal(newExtensions) {
		return false
	}

	return true
}

// getExtensionsSet returns a set of extension names from a MachineConfig
func getExtensionsSet(config *mcfgv1.MachineConfig) sets.String {
	extensions := sets.NewString()
	if config == nil || config.Spec.Extensions == nil {
		return extensions
	}
	for _, ext := range config.Spec.Extensions {
		extensions.Insert(ext)
	}
	return extensions
}

// updateOS updates the OS to the new config, but only pulls/extracts extensions if needed
func (dn *Daemon) updateOS(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	glog.Info("Starting OS update")

	// Check if we need to handle extensions
	needExtensions := shouldPullExtensions(oldConfig, newConfig)

	if needExtensions {
		glog.Info("Extensions have changed, pulling/extracting extensions")
		if err := dn.pullAndExtractExtensions(newConfig); err != nil {
			return fmt.Errorf("failed to pull/extract extensions: %w", err)
		}
	} else {
		glog.Info("Extensions unchanged, skipping extension pull/extract")
	}

	// Perform the actual OS update
	if err := dn.performOSUpdate(newConfig); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	// If extensions were removed, clean up old extension files
	if needExtensions && oldConfig != nil {
		oldExtensions := getExtensionsSet(oldConfig)
		newExtensions := getExtensionsSet(newConfig)
		removedExtensions := oldExtensions.Difference(newExtensions)
		if removedExtensions.Len() > 0 {
			glog.Infof("Cleaning up removed extensions: %v", removedExtensions.List())
			if err := dn.cleanupExtensions(removedExtensions); err != nil {
				return fmt.Errorf("failed to cleanup removed extensions: %w", err)
			}
		}
	}

	return nil
}

// pullAndExtractExtensions pulls and extracts extensions for the given config
func (dn *Daemon) pullAndExtractExtensions(config *mcfgv1.MachineConfig) error {
	if config == nil || config.Spec.Extensions == nil {
		return nil
	}

	for _, ext := range config.Spec.Extensions {
		glog.Infof("Pulling extension: %s", ext)
		if err := dn.pullExtension(ext); err != nil {
			return fmt.Errorf("failed to pull extension %s: %w", ext, err)
		}

		glog.Infof("Extracting extension: %s", ext)
		if err := dn.extractExtension(ext); err != nil {
			return fmt.Errorf("failed to extract extension %s: %w", ext, err)
		}
	}

	return nil
}

// cleanupExtensions removes files for extensions that are no longer needed
func (dn *Daemon) cleanupExtensions(extensions sets.String) error {
	for _, ext := range extensions.List() {
		extPath := filepath.Join(constants.ExtensionsDir, ext)
		glog.Infof("Removing extension: %s", extPath)
		if err := dn.os.RemoveAll(extPath); err != nil {
			return fmt.Errorf("failed to remove extension %s: %w", ext, err)
		}
	}
	return nil
}

// performOSUpdate performs the actual OS update using rpm-ostree
func (dn *Daemon) performOSUpdate(config *mcfgv1.MachineConfig) error {
	// This is a placeholder - actual implementation would call rpm-ostree
	glog.Infof("Performing OS update to config: %s", config.Name)
	return nil
}

// pullExtension pulls an extension (placeholder)
func (dn *Daemon) pullExtension(ext string) error {
	glog.Infof("Pulling extension: %s", ext)
	return nil
}

// extractExtension extracts an extension (placeholder)
func (dn *Daemon) extractExtension(ext string) error {
	glog.Infof("Extracting extension: %s", ext)
	return nil
}
