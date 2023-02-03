package daemon

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

// Used to hold global paths so we can override them for testing.
// TODO: Wire this up with the origDirPath and noOrigDirPath so we can avoid mutating global variables.
type ValidationPaths struct {
	// The Systemd dropin path location.
	// Defaults to /etc/systemd/system
	systemdPath          string
	expectedSSHKeyPath   string
	unexpectedSSHKeyPath string
}

func GetValidationPaths(os osRelease) (ValidationPaths, error) {
	vp := ValidationPaths{
		systemdPath: pathSystemd,
	}

	if os.IsEL9() || os.IsSCOS() || os.IsFCOS() {
		vp.expectedSSHKeyPath = constants.RHCOS9SSHKeyPath
		vp.unexpectedSSHKeyPath = constants.RHCOS8SSHKeyPath
	} else {
		vp.expectedSSHKeyPath = constants.RHCOS8SSHKeyPath
		vp.unexpectedSSHKeyPath = constants.RHCOS9SSHKeyPath
	}

	return vp, vp.validate()
}

func GetValidationPathsWithPrefix(os osRelease, pathPrefix string) (ValidationPaths, error) {
	vp, err := GetValidationPaths(os)
	if err != nil {
		return vp, err
	}

	vp.systemdPath = filepath.Join(pathPrefix, vp.systemdPath)
	vp.expectedSSHKeyPath = filepath.Join(pathPrefix, vp.expectedSSHKeyPath)
	vp.unexpectedSSHKeyPath = filepath.Join(pathPrefix, vp.unexpectedSSHKeyPath)

	return vp, vp.validate()
}

// Gets the root SSH key path dir for a given SSH path; e.g., /home/core/.ssh
func (vp *ValidationPaths) SSHKeyRoot() string {
	fragments := vp.ExpectedSSHPathFragments()
	return fragments[len(fragments)-1]
}

func (vp *ValidationPaths) ExpectedSSHKeyPath() string {
	return vp.expectedSSHKeyPath
}

func (vp *ValidationPaths) UnexpectedSSHKeyPath() string {
	return vp.unexpectedSSHKeyPath
}

// Returns all of the path fragments for the expected SSH path. If expected SSH key
// path is /home/core/.ssh/authorized_keys.d/ignition, it will return
// /home/core/.ssh/authorized_keys.d/ignition,
// /home/core/.ssh/authorized_keys.d, /home/core/.ssh
func (vp *ValidationPaths) ExpectedSSHPathFragments() []string {
	return vp.getSSHPathFragments(vp.expectedSSHKeyPath)
}

// Returns all of the path fragments for the unexpected SSH path. If the
// unexpected SSH key path is /home/core/.ssh/authorized_keys, it
// will return /home/core/.ssh/authorized_keys, /home/core/.ssh
func (vp *ValidationPaths) UnexpectedSSHPathFragments() []string {
	return vp.getSSHPathFragments(vp.unexpectedSSHKeyPath)
}

// Gets the systemd root path. Defaults to pathSystemd, if empty.
func (vp *ValidationPaths) SystemdPath() string {
	if vp.systemdPath == "" {
		return pathSystemd
	}

	return vp.systemdPath
}

// Gets the absolute path for a systemd unit, given the unit name.
func (vp *ValidationPaths) SystemdUnitPath(unitName string) string {
	return filepath.Join(vp.SystemdPath(), unitName)
}

// Gets the absolute path for a systemd unit and dropin, given the unit and dropin names.
func (vp *ValidationPaths) SystemdDropinPath(unitName, dropinName string) string {
	return filepath.Join(vp.SystemdPath(), unitName+".d", dropinName)
}

// Returns all of the path fragments for a given SSH path. If
// given /home/core/.ssh/authorized_keys.d/ignition, it will return
// /home/core/.ssh/authorized_keys.d/ignition,
// /home/core/.ssh/authorized_keys.d, /home/core/.ssh
func (vp *ValidationPaths) getSSHPathFragments(path string) []string {
	out := []string{path}

	sshFragment := filepath.Base(constants.CoreUserSSH)

	if strings.HasSuffix(path, sshFragment) {
		return out
	}

	tmp := path
	for {
		tmp = filepath.Dir(tmp)
		out = append(out, tmp)
		if strings.HasSuffix(tmp, sshFragment) {
			break
		}
	}

	return out
}

func (vp *ValidationPaths) validate() error {
	if vp.systemdPath == "" {
		vp.systemdPath = pathSystemd
	}

	if vp.expectedSSHKeyPath == "" {
		return fmt.Errorf("ExpectedSSHKeyPath not set")
	}

	if vp.unexpectedSSHKeyPath == "" {
		return fmt.Errorf("UnexpectedSSHKeyPath not set")
	}

	return nil
}
