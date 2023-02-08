package daemon

import (
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

	// The prefix to append to all paths for testing.
	pathPrefix string
}

func GetPaths(os osRelease) Paths {
	p := Paths{
		systemdPath:         pathSystemd,
		origParentDirPath:   filepath.Join("/etc", "machine-config-daemon", "orig"),
		noOrigParentDirPath: filepath.Join("/etc", "machine-config-daemon", "noorig"),
		usrPath:             "/usr",
		pathPrefix:          "",
	}

	if os.IsEL9() || os.IsSCOS() || os.IsFCOS() {
		p.expectedSSHKeyPath = constants.RHCOS9SSHKeyPath
		p.unexpectedSSHKeyPath = constants.RHCOS8SSHKeyPath
	} else {
		p.expectedSSHKeyPath = constants.RHCOS8SSHKeyPath
		p.unexpectedSSHKeyPath = constants.RHCOS9SSHKeyPath
	}

	return p
}

func GetPathsWithPrefix(os osRelease, pathPrefix string) Paths {
	p := GetPaths(os)
	p.pathPrefix = pathPrefix
	return p
}

// Gets the root SSH key path dir for a given SSH path; e.g., /home/core/.ssh
func (p *Paths) SSHKeyRoot() string {
	return p.appendPrefix(constants.CoreUserSSH)
}

func (p *Paths) ExpectedSSHKeyPath() string {
	return p.appendPrefix(p.expectedSSHKeyPath)
}

func (p *Paths) UnexpectedSSHKeyPath() string {
	return p.appendPrefix(p.unexpectedSSHKeyPath)
}

// Returns all of the path fragments for the expected SSH path. If expected SSH key
// path is /home/core/.ssh/authorized_keys.d/ignition, it will return
// /home/core/.ssh/authorized_keys.d/ignition,
// /home/core/.ssh/authorized_keys.d, /home/core/.ssh
func (p *Paths) ExpectedSSHPathFragments() []string {
	out := []string{constants.CoreUserSSH}

	if p.ExpectedSSHKeyPath() == p.appendPrefix(constants.RHCOS8SSHKeyPath) {
		out = append(out, p.rhcos8KeyPathFragments()...)
	} else {
		out = append(out, p.rhcos9KeyPathFragments()...)
	}

	return p.appendToAll(out)
}

// Returns all of the path fragments for the unexpected SSH path. If the
// unexpected SSH key path is /home/core/.ssh/authorized_keys, it
// will return /home/core/.ssh/authorized_keys, /home/core/.ssh
func (p *Paths) UnexpectedSSHPathFragments() []string {
	var out []string

	if p.UnexpectedSSHKeyPath() == p.appendPrefix(constants.RHCOS9SSHKeyPath) {
		out = p.rhcos9KeyPathFragments()
	} else {
		out = p.rhcos8KeyPathFragments()
	}

	return p.appendToAll(out)
}

func (p *Paths) rhcos8KeyPathFragments() []string {
	// /home/core/.ssh/authorized_keys
	return []string{constants.RHCOS8SSHKeyPath}
}

func (p *Paths) rhcos9KeyPathFragments() []string {
	return []string{
		// /home/core/.ssh/authorized_keys.d/ignition
		constants.RHCOS9SSHKeyPath,
		// /home/core/.ssh/authorized_keys.d
		filepath.Dir(constants.RHCOS9SSHKeyPath),
	}
}

// Gets the systemd root path.
func (p *Paths) SystemdPath() string {
	return p.appendPrefix(p.systemdPath)
}

// Gets the absolute path for a systemd unit, given the unit name.
func (p *Paths) SystemdUnitPath(unitName string) string {
	return filepath.Join(p.SystemdPath(), unitName)
}

// Gets the absolute path for a systemd unit and dropin, given the unit and dropin names.
func (p *Paths) SystemdDropinPath(unitName, dropinName string) string {
	// Idempotently adds the ".d" suffix onto the unit name.
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
	return p.appendPrefix(p.origParentDirPath)
}

func (p *Paths) NoOrigParentDir() string {
	return p.appendPrefix(p.noOrigParentDirPath)
}

func (p *Paths) WithUsrPath(fpath string) string {
	return filepath.Join(p.appendPrefix(p.usrPath), fpath)
}

// Idempotently appends the path prefix onto a given path.
func (p *Paths) appendPrefix(path string) string {
	if p.pathPrefix == "" || strings.HasPrefix(path, p.pathPrefix) {
		return path
	}

	return filepath.Join(p.pathPrefix, path)
}

// Idempotently appends the path prefix to all items in a given string slice.
func (p *Paths) appendToAll(paths []string) []string {
	for i := range paths {
		paths[i] = p.appendPrefix(paths[i])
	}

	return paths
}
