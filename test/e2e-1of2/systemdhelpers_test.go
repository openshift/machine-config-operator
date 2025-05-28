package e2e_1of2_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

// Creates a dummy systemd unit file with a customizable description.
func getSystemdUnitContents(desc string) *string {
	contents := []string{
		"[Unit]",
		fmt.Sprintf("Description=%s", desc),
		"",
		"[Service]",
		`ExecStart=/bin/bash -c "sleep infinity"`,
		"Restart=on-failure",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
	}

	return helpers.StrToPtr(strings.Join(contents, "\n"))
}

// Asserts that a given systemd unit is enabled.
func assertSystemdUnitIsEnabled(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName string) {
	t.Helper()

	output := showSystemdUnit(t, cs, node, unitName)
	assert.Contains(t, output, "UnitFileState=enabled")
	assert.Contains(t, output, "ActiveState=active")
	assert.Contains(t, output, "LoadState=loaded")
}

// Asserts that a given systemd unit is disabled.
func assertSystemdUnitIsDisabled(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName string) {
	t.Helper()

	output := showSystemdUnit(t, cs, node, unitName)
	assert.Contains(t, output, "UnitFileState=disabled")
	assert.Contains(t, output, "ActiveState=inactive")
	assert.Contains(t, output, "LoadState=loaded")
}

// Asserts that a given systemd unit has the specified dropins.
func assertSystemdUnitHasDropins(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName string, dropins []string) {
	t.Helper()

	output := showSystemdUnit(t, cs, node, unitName)
	dropinPaths := getSystemdLineFromOutput(output, "DropInPaths")
	for _, dropin := range dropins {
		assert.Contains(t, dropinPaths, dropin)
	}
}

const (
	// The root path for where systemd stores its units, dropins, etc.
	systemdRootPath string = "/etc/systemd/system"
)

// Asserts that a dropin file exists for a given systemd unit.
func assertSystemdUnitDropinFileExists(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName, dropinName string) {
	t.Helper()
	helpers.AssertFileOnNode(t, cs, node, filepath.Join(systemdRootPath, unitName+".d", dropinName))
}

// Asserts tha a dropin file does not exist for a given systemd unit.
func assertSystemdUnitDropinFileDoesNotExist(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName, dropinName string) {
	t.Helper()
	helpers.AssertFileNotOnNode(t, cs, node, filepath.Join(systemdRootPath, unitName+".d", dropinName))
}

// Asserts that the unit file for a given systemd unit exists.
func assertSystemdUnitFileExists(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName string) {
	t.Helper()
	helpers.AssertFileOnNode(t, cs, node, filepath.Join(systemdRootPath, unitName))
}

// Asserts that the given systemd unit exists by querying systemd.
func assertSystemdUnitExists(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName string) {
	t.Helper()
	output := showSystemdUnit(t, cs, node, unitName)
	loadState := getSystemdLineFromOutput(output, "LoadState")
	assert.Equal(t, loadState, "LoadState=loaded")
}

// Asserts that the given systemd unit does not exist by querying systemd.
func assertSystemdUnitDoesNotExist(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName string) {
	t.Helper()

	output := showSystemdUnit(t, cs, node, unitName)

	loadState := getSystemdLineFromOutput(output, "LoadState")
	assert.Equal(t, "LoadState=not-found", loadState)

	loadError := getSystemdLineFromOutput(output, "LoadError")
	assert.Contains(t, loadError, "NoSuchUnit")
}

// Asserts that the given systemd unit file does not exist.
func assertSystemdUnitFileDoesNotExist(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName string) {
	t.Helper()
	helpers.AssertFileNotOnNode(t, cs, node, filepath.Join("/rootfs", systemdRootPath, unitName))
}

// Runs "systemctl show <unit>" on a given node. Note: This command should
// always returns zero even when the unit does not exist. Assertions are done
// by interrogating its output.
func showSystemdUnit(t *testing.T, cs *framework.ClientSet, node corev1.Node, unitName string) string {
	return helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "systemctl", "show", unitName)
}

// TODO: This does not handle the case where a key may appear multiple times;
// e.g., multiple ExecStart lines.
func getSystemdLineFromOutput(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}

	return ""
}
