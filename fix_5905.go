package daemon

import (
	"fmt"
	"strings"

	"github.com/coreos/rpmostree-client-go/pkg/api"
	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/daemon"
	"github.com/openshift/machine-config-operator/pkg/daemon/ocm"
	"github.com/openshift/machine-config-operator/pkg/daemon/rpmostree"
	"github.com/openshift/machine-config-operator/pkg/daemon/update"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions/pull"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions/pull/extract"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions/pull/extract/check"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions/pull/extract/check/diff"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions/pull/extract/check/diff/mcDiff"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions/pull/extract/check/diff/mcDiff/extensions"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions/pull/extract/check/diff/mcDiff/extensions/old"
	"github.com/openshift/machine-config-operator/pkg/daemon/update/os/extensions/pull/extract/check/diff/mcDiff/extensions/new"
)

func (d *Daemon) updateOS(config *api.MachineConfig) error {
	glog.Info("Starting OS update")

	// Get the current config
	currentConfig, err := d.getCurrentConfig()
	if err != nil {
		return fmt.Errorf("failed to get current config: %w", err)
	}

	// Check if we need to pull/extract extensions
	needExtensions := checkExtensionsNeeded(currentConfig, config)

	if needExtensions {
		glog.Info("Extensions need to be pulled/extracted for this update")
		if err := d.pullAndExtractExtensions(config); err != nil {
			return fmt.Errorf("failed to pull/extract extensions: %w", err)
		}
	} else {
		glog.Info("No extensions needed for this update, skipping pull/extract")
	}

	// Perform the actual OS update
	if err := d.performOSUpdate(config); err != nil {
		return fmt.Errorf("failed to perform OS update: %w", err)
	}

	glog.Info("OS update completed successfully")
	return nil
}

func checkExtensionsNeeded(oldConfig, newConfig *api.MachineConfig) bool {
	// If there's no old config, we need extensions
	if oldConfig == nil {
		return true
	}

	// Compare the extensions in old and new configs
	oldExtensions := getExtensionsFromConfig(oldConfig)
	newExtensions := getExtensionsFromConfig(newConfig)

	// If extensions are the same, no need to pull/extract
	if oldExtensions == newExtensions {
		return false
	}

	// Check if the new config has extensions that are not in the old config
	for _, ext := range newExtensions {
		if !contains(oldExtensions, ext) {
			return true
		}
	}

	// Check if the old config had extensions that are not in the new config
	// (these might need to be removed)
	for _, ext := range oldExtensions {
		if !contains(newExtensions, ext) {
			return true
		}
	}

	return false
}

func getExtensionsFromConfig(config *api.MachineConfig) []string {
	if config == nil || config.Spec == nil || config.Spec.OS == nil {
		return nil
	}
	return config.Spec.OS.Extensions
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func (d *Daemon) pullAndExtractExtensions(config *api.MachineConfig) error {
	glog.Info("Pulling and extracting extensions")

	extensions := getExtensionsFromConfig(config)
	if len(extensions) == 0 {
		glog.Info("No extensions to pull/extract")
		return nil
	}

	// Pull extensions
	if err := d.pullExtensions(extensions); err != nil {
		return fmt.Errorf("failed to pull extensions: %w", err)
	}

	// Extract extensions
	if err := d.extractExtensions(extensions); err != nil {
		return fmt.Errorf("failed to extract extensions: %w", err)
	}

	glog.Info("Extensions pulled and extracted successfully")
	return nil
}

func (d *Daemon) pullExtensions(extensions []string) error {
	glog.Infof("Pulling extensions: %s", strings.Join(extensions, ", "))
	// Implementation for pulling extensions
	// This would typically involve calling rpm-ostree or similar
	return nil
}

func (d *Daemon) extractExtensions(extensions []string) error {
	glog.Infof("Extracting extensions: %s", strings.Join(extensions, ", "))
	// Implementation for extracting extensions
	// This would typically involve calling rpm-ostree or similar
	return nil
}

func (d *Daemon) performOSUpdate(config *api.MachineConfig) error {
	glog.Info("Performing OS update")
	// Implementation for performing the actual OS update
	// This would typically involve calling rpm-ostree or similar
	return nil
}

func (d *Daemon) getCurrentConfig() (*api.MachineConfig, error) {
	// Implementation to get the current machine config
	// This would typically read from the current state
	return nil, nil
}
