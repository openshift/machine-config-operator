package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang/glog"
)

// genericCmdFunc describes a function for wrapping commands
type genericCmdFunc func(args ...string) ([]byte, error)

// systemctlFunc is a either a wrapper around the systemd binary or a mock
// execSystemctlFunc should be used for the execution of systemctl commands
var systemctlFunc genericCmdFunc

// execSystemctlFunc executes systemctl or the mocking function.
func execSystemctlFunc(args ...string) ([]byte, error) {
	if systemctlFunc != nil {
		glog.V(2).Info("Using a mocked function for systemctl")
		return systemctlFunc(args...)
	}
	systemdBin := filepath.Join("usr", "bin", "systemctl")
	return runGetOut(systemdBin, args...)
}

// reloadSystem restarts systemd
func reloadSystemd() error {
	out, err := execSystemctlFunc("daemon-reload")
	if err != nil {
		return fmt.Errorf("failed reload of systemd daemon: %s\n%v", string(out), err)
	}
	return nil
}

const (
	// defaultRebootTargetLink is the backing file for the rebootTarget for systemd
	defaultRebootTargetLink = "/dev/null"
	// defaultRebootTarget is the systemd reboot.target filepath used when masking a reboot.
	defaultRebootTarget = "/run/systemd/system/reboot.target"
)

var (
	// rebootTargetLink is the backing file for the rebootTarget for systemd
	rebootTargetLink = defaultRebootTargetLink
	// rebootTarget is the systemd reboot.target filepath used when masking a reboot.
	rebootTarget = defaultRebootTarget
)

// maskRebootTarget masks the systemd reboot.target to block reboots.
func maskRebootTarget() error {
	glog.Info("Setting a reboot.target systemd mask to prevent reboot during update operation")
	if _, err := os.Lstat(rebootTarget); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to check for existing reboot.target link: %w", err)
	}
	if err := os.Symlink(rebootTargetLink, rebootTarget); err != nil {
		return fmt.Errorf("failed to create %s masking symlink to %s: %v", rebootTargetLink, rebootTarget, err)
	}
	return reloadSystemd()
}

// unmaskRebootTarget removes the reboot mask.
func unmaskRebootTarget() error {
	glog.Info("Removing reboot mask")
	if err := os.Remove(rebootTarget); err != nil && os.IsNotExist(err) {
		return nil
	}
	return reloadSystemd()
}
