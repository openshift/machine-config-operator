package daemon

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func unsetSystemctlFunc() {
	systemctlFunc = nil
	rebootTarget = defaultRebootTarget
}

func fakeSystemdFunc(...string) ([]byte, error) {
	return nil, nil
}

// TestSystemdExecFunc is a quick check that the systemdFunc is
// a real function command.
func TestSystemdExecFunc(t *testing.T) {
	defer unsetSystemctlFunc()
	systemctlFunc = fakeSystemdFunc
	assert.NotNil(t, systemctlFunc)
}

// TestSystemdReboot checks to make sure that the mask is set and
// released.
func TestSystemdReboot(t *testing.T) {
	defer unsetSystemctlFunc()
	systemctlFunc = fakeSystemdFunc

	fakeRunDir, err := ioutil.TempDir("", "systemd")
	if err != nil {
		t.Fatalf("Failed to create a tempdir")
	}
	defer func() {
		_ = os.RemoveAll(fakeRunDir)
	}()

	rebootTarget = filepath.Join(fakeRunDir, rebootTarget)
	if err = os.MkdirAll(filepath.Dir(rebootTarget), 0755); err != nil {
		t.Fatalf("Failed to create mock systemd runtime directory: %v", err)
	}

	if err = maskRebootTarget(); err != nil {
		t.Fatalf("Failed to mask systemd reboot.target: %v", err)
	}
	r, err := os.Readlink(rebootTarget)
	if err != nil {
		t.Fatalf("Failed to resolve symlink for %s: %v", rebootTarget, err)
	}
	if r != rebootTargetLink {
		t.Errorf("Link target is incorrect:\n  want: %s\n   got: %s\n", rebootTargetLink, r)
	}
	if err = unmaskRebootTarget(); err != nil {
		t.Errorf("Failed to unmask: %v", err)
	}
	if _, err = os.Lstat(rebootTarget); err == nil {
		t.Errorf("Link should have been removed")
	}
}
