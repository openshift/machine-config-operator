package daemon

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

// Used to hold global paths so we can override them for testing.
// TODO: Wire this up with the origDirPath and noOrigDirPath so we can avoid mutating global variables.
type Paths struct {
	// The Systemd dropin path location.
	// Defaults to /etc/systemd/system
	systemdPath          string
	expectedSSHKeyPath   string
	unexpectedSSHKeyPath string
	origParentDirPath    string
	noOrigParentDirPath  string
	usrPath              string
}

func GetPaths(os osRelease) (Paths, error) {
	p := Paths{
		systemdPath:         pathSystemd,
		origParentDirPath:   filepath.Join("/etc", "machine-config-daemon", "orig"),
		noOrigParentDirPath: filepath.Join("/etc", "machine-config-daemon", "noorig"),
		usrPath:             "/usr",
	}

	if os.IsEL9() || os.IsSCOS() || os.IsFCOS() {
		p.expectedSSHKeyPath = constants.RHCOS9SSHKeyPath
		p.unexpectedSSHKeyPath = constants.RHCOS8SSHKeyPath
	} else {
		p.expectedSSHKeyPath = constants.RHCOS8SSHKeyPath
		p.unexpectedSSHKeyPath = constants.RHCOS9SSHKeyPath
	}

	return p, p.validate()
}

func GetPathsWithPrefix(os osRelease, pathPrefix string) (Paths, error) {
	p, err := GetPaths(os)
	if err != nil {
		return p, err
	}

	p.systemdPath = filepath.Join(pathPrefix, p.systemdPath)
	p.expectedSSHKeyPath = filepath.Join(pathPrefix, p.expectedSSHKeyPath)
	p.unexpectedSSHKeyPath = filepath.Join(pathPrefix, p.unexpectedSSHKeyPath)
	p.origParentDirPath = filepath.Join(pathPrefix, p.origParentDirPath)
	p.noOrigParentDirPath = filepath.Join(pathPrefix, p.noOrigParentDirPath)
	p.usrPath = filepath.Join(pathPrefix, p.usrPath)

	return p, p.validate()
}

// Gets the root SSH key path dir for a given SSH path; e.g., /home/core/.ssh
func (p *Paths) SSHKeyRoot() string {
	fragments := p.ExpectedSSHPathFragments()
	return fragments[len(fragments)-1]
}

func (p *Paths) ExpectedSSHKeyPath() string {
	return p.expectedSSHKeyPath
}

func (p *Paths) UnexpectedSSHKeyPath() string {
	return p.unexpectedSSHKeyPath
}

// Returns all of the path fragments for the expected SSH path. If expected SSH key
// path is /home/core/.ssh/authorized_keys.d/ignition, it will return
// /home/core/.ssh/authorized_keys.d/ignition,
// /home/core/.ssh/authorized_keys.d, /home/core/.ssh
func (p *Paths) ExpectedSSHPathFragments() []string {
	return p.getSSHPathFragments(p.expectedSSHKeyPath)
}

// Returns all of the path fragments for the unexpected SSH path. If the
// unexpected SSH key path is /home/core/.ssh/authorized_keys, it
// will return /home/core/.ssh/authorized_keys, /home/core/.ssh
func (p *Paths) UnexpectedSSHPathFragments() []string {
	return p.getSSHPathFragments(p.unexpectedSSHKeyPath)
}

// Gets the systemd root path. Defaults to pathSystemd, if empty.
func (p *Paths) SystemdPath() string {
	if p.systemdPath == "" {
		return pathSystemd
	}

	return p.systemdPath
}

// Gets the absolute path for a systemd unit, given the unit name.
func (p *Paths) SystemdUnitPath(unitName string) string {
	return filepath.Join(p.SystemdPath(), unitName)
}

// Gets the absolute path for a systemd unit and dropin, given the unit and dropin names.
func (p *Paths) SystemdDropinPath(unitName, dropinName string) string {
	if !strings.HasSuffix(unitName, ".d") {
		unitName += ".d"
	}

	return filepath.Join(p.SystemdPath(), unitName, dropinName)
}

func (p *Paths) OrigFileName(fpath string) string {
	return filepath.Join(p.OrigParentDir(), fpath+".mcdorig")
}

// We use this to create a file that indicates that no original file existed on disk
// when we write a file via a MachineConfig. Otherwise the MCD does not differentiate
// between "a file existed due to a previous machineconfig" vs "a file existed on disk
// before the MCD took over". Also see deleteStaleData() above.
//
// The "stamp" part of the name indicates it is not an actual backup file, just an
// empty file to indicate lack of previous existence.
func (p *Paths) NoOrigFileStampName(fpath string) string {
	return filepath.Join(p.NoOrigParentDir(), fpath+".mcdnoorig")
}

func (p *Paths) OrigParentDir() string {
	return p.origParentDirPath
}

func (p *Paths) NoOrigParentDir() string {
	return p.noOrigParentDirPath
}

func (p *Paths) WithUsrPath(fpath string) string {
	return filepath.Join(p.usrPath, fpath)
}

// Returns all of the path fragments for a given SSH path. If
// given /home/core/.ssh/authorized_keys.d/ignition, it will return
// /home/core/.ssh/authorized_keys.d/ignition,
// /home/core/.ssh/authorized_keys.d, /home/core/.ssh
func (p *Paths) getSSHPathFragments(path string) []string {
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

func (p *Paths) validate() error {
	if p.systemdPath == "" {
		p.systemdPath = pathSystemd
	}

	if p.expectedSSHKeyPath == "" {
		return fmt.Errorf("ExpectedSSHKeyPath not set")
	}

	if p.unexpectedSSHKeyPath == "" {
		return fmt.Errorf("UnexpectedSSHKeyPath not set")
	}

	return nil
}
