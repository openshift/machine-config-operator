// daemon/update.go

package daemon

import (
	"fmt"
	"strings"

	"github.com/coreos/rpmostree-client-go/pkg/rpmostree"
	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

// needsExtensionsForUpdate determines if we need to pull/extract extensions for an OS update.
// We only need extensions when:
// 1. The new config has extensions that the old config didn't have (extensions added)
// 2. The old config had extensions that the new config doesn't have (extensions removed)
// 3. Extensions were previously used (old config had extensions)
func needsExtensionsForUpdate(oldConfig, newConfig *mcfgv1.MachineConfig) bool {
	// If either config has no extensions, we don't need to pull them
	if oldConfig == nil || newConfig == nil {
		return false
	}

	oldExtensions := getExtensionsFromConfig(oldConfig)
	newExtensions := getExtensionsFromConfig(newConfig)

	// If old config had extensions, we need them locally for the update
	if len(oldExtensions) > 0 {
		return true
	}

	// If new config has extensions that old config didn't have, we need them
	if len(newExtensions) > 0 && len(oldExtensions) == 0 {
		return true
	}

	// Check for specific extension changes
	oldExtSet := make(map[string]bool)
	for _, ext := range oldExtensions {
		oldExtSet[ext] = true
	}

	for _, ext := range newExtensions {
		if !oldExtSet[ext] {
			return true
		}
	}

	return false
}

// getExtensionsFromConfig extracts extension names from a MachineConfig
func getExtensionsFromConfig(config *mcfgv1.MachineConfig) []string {
	if config == nil || config.Spec.Extensions == nil {
		return nil
	}
	return config.Spec.Extensions
}

// handleOSUpdateWithExtensions handles the OS update process with smart extension management
func (dn *Daemon) handleOSUpdateWithExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// Check if we need to handle extensions for this update
	if !needsExtensionsForUpdate(oldConfig, newConfig) {
		glog.V(4).Info("No extension changes detected, skipping extension pull/extract")
		return dn.performOSUpdate(newConfig)
	}

	// We need extensions - pull and extract them
	glog.V(2).Info("Extension changes detected, pulling/extracting extensions")
	
	// Get the list of extensions we need
	neededExtensions := getExtensionsFromConfig(newConfig)
	if len(neededExtensions) == 0 && oldConfig != nil {
		// If new config has no extensions but old did, we still need to handle cleanup
		neededExtensions = getExtensionsFromConfig(oldConfig)
	}

	// Pull and extract extensions
	if err := dn.pullAndExtractExtensions(neededExtensions); err != nil {
		return fmt.Errorf("failed to pull/extract extensions: %w", err)
	}

	// Perform the actual OS update
	return dn.performOSUpdate(newConfig)
}

// pullAndExtractExtensions pulls and extracts the specified extensions
func (dn *Daemon) pullAndExtractExtensions(extensions []string) error {
	if len(extensions) == 0 {
		return nil
	}

	glog.V(2).Infof("Pulling and extracting extensions: %v", extensions)
	
	// Use rpm-ostree to pull extensions
	args := []string{"usroverlay", "--pull-extensions", strings.Join(extensions, ",")}
	if err := dn.rpmOstreeClient.Run(args...); err != nil {
		return fmt.Errorf("failed to pull extensions: %w", err)
	}

	return nil
}

// performOSUpdate performs the actual OS update without extension handling
func (dn *Daemon) performOSUpdate(config *mcfgv1.MachineConfig) error {
	// Existing OS update logic
	// This would be the existing update code path
	glog.V(4).Info("Performing OS update")
	return dn.updateOS(config)
}

// updateOS is the existing OS update function
func (dn *Daemon) updateOS(config *mcfgv1.MachineConfig) error {
	// Implementation of existing OS update logic
	// This would contain the actual rpm-ostree update commands
	return nil
}
