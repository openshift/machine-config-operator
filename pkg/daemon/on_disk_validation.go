package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/golang/glog"
	"github.com/google/go-cmp/cmp"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Validates that the on-disk state matches a given MachineConfig.
func validateOnDiskState(currentConfig *mcfgv1.MachineConfig, paths Paths) error {
	// And the rest of the disk state
	// We want to verify the disk state in the spec version that it was created with,
	// to remove possibilities of behaviour changes due to translation
	ignconfigi, err := ctrlcommon.IgnParseWrapper(currentConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("failed to parse Ignition for validation: %w", err)
	}

	switch typedConfig := ignconfigi.(type) {
	case ign3types.Config:
		if err := checkV3Files(ignconfigi.(ign3types.Config).Storage.Files); err != nil {
			return &fileConfigDriftErr{err}
		}
		if err := checkV3Units(ignconfigi.(ign3types.Config).Systemd.Units, paths); err != nil {
			return &unitConfigDriftErr{err}
		}

		if err := checkV3SSHKeys(ignconfigi.(ign3types.Config).Passwd.Users, paths); err != nil {
			return &sshConfigDriftErr{err}
		}

		return nil
	case ign2types.Config:
		if err := checkV2Files(ignconfigi.(ign2types.Config).Storage.Files); err != nil {
			return &fileConfigDriftErr{err}
		}
		if err := checkV2Units(ignconfigi.(ign2types.Config).Systemd.Units, paths); err != nil {
			return &unitConfigDriftErr{err}
		}
		// TODO: Do we need to check for Ign V2 SSH keys too?
		return nil
	default:
		return fmt.Errorf("unexpected type for ignition config: %v", typedConfig)
	}
}

// Checks the expected key path for the following conditions:
// 1. The dir mode differs from what we expect. (TODO: Should we also verify ownership?)
// 2. The file contents and mode differ from what is expected, while ignoring
// newlines at the end of the file. Ignition seems ot add a trailing newline
// ("\n") onto the end of the file, which breaks this check.
func checkExpectedSSHKeyPath(users []ign3types.PasswdUser, paths Paths) error {
	authKeyPath := paths.ExpectedSSHKeyPath()

	if err := checkDirMode(filepath.Dir(authKeyPath), os.FileMode(0o700)); err != nil {
		return err
	}

	if err := checkFileMode(authKeyPath, os.FileMode(0o600)); err != nil {
		return err
	}

	contents, err := os.ReadFile(authKeyPath)
	if err != nil {
		return err
	}

	// Trim newlines from the end of the expected file contents as well as the
	// actual file contents to avoid a false positive caused by Ignition adding
	// newlines when it applies the initial config.
	contents = []byte(strings.TrimRight(string(contents), "\n"))
	expectedContent := []byte(strings.TrimRight(concatSSHKeys(users), "\n"))

	if !bytes.Equal(contents, expectedContent) {
		glog.Errorf("content mismatch for file %q (-want +got):\n%s", authKeyPath, cmp.Diff(expectedContent, contents))
		return fmt.Errorf("content mismatch for file: %q", authKeyPath)
	}

	return nil
}

func errIfFileIsFound(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return &unexpectedSSHFileErr{
			filename: path,
			error:    fmt.Errorf("expected not to find SSH keys in %s", path),
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

// Checks the old (unexpected) SSH key path for the presence of unexpected SSH
// keys or other files.
func checkUnexpectedSSHKeyPath(paths Paths) error {
	expectedSSHKeyPath := paths.ExpectedSSHKeyPath()

	// First, check if we have any unexpected path fragments.
	for _, path := range paths.UnexpectedSSHPathFragments() {
		if err := errIfFileIsFound(path); err != nil {
			return err
		}
	}

	// If we haven't found any unexpected key paths and we're on RHCOS 8, we're all done.
	if strings.HasSuffix(expectedSSHKeyPath, constants.RHCOS8SSHKeyPath) {
		return nil
	}

	// Next, we walk the directory path of /home/core/.ssh/authorized_keys.d and
	// scan for any unknonwn path fragments.
	keyDir := filepath.Dir(expectedSSHKeyPath)
	ignored := sets.NewString(keyDir, expectedSSHKeyPath)
	return filepath.WalkDir(keyDir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Ignore /home/core/.ssh/authoried_keys.d/ignition and
		// /home/core/.ssh/authorized_keys.d
		if ignored.Has(path) {
			return nil
		}

		return errIfFileIsFound(path)
	})
}

// Checks if SSH keys match the expected state and check that unexpected SSH
// key path fragments are not present.
func checkV3SSHKeys(users []ign3types.PasswdUser, paths Paths) error {
	if err := checkExpectedSSHKeyPath(users, paths); err != nil {
		return err
	}

	return checkUnexpectedSSHKeyPath(paths)
}

// Checks that the ondisk state for a systemd dropin matches the expected state.
func checkV3Dropin(paths Paths, unit ign3types.Unit, dropin ign3types.Dropin) error {
	path := paths.SystemdDropinPath(unit.Name, dropin.Name)

	var content string
	if dropin.Contents == nil {
		content = ""
	} else {
		content = *dropin.Contents
	}

	// As of 4.7 we now remove any empty defined dropins, check for that first
	if _, err := os.Stat(path); content == "" && err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	// To maintain backwards compatibility, we allow existing zero length files to exist.
	// Thus we are also ok if the dropin exists but has no content
	return checkFileContentsAndMode(path, []byte(content), defaultFilePermissions)
}

// checkV3Units validates the contents of an individual unit in the
// target config and returns nil if they match.
func checkV3Unit(unit ign3types.Unit, paths Paths) error {
	for _, dropin := range unit.Dropins {
		if err := checkV3Dropin(paths, unit, dropin); err != nil {
			return err
		}
	}

	if unit.Contents == nil || *unit.Contents == "" {
		// Return early if the contents are empty.
		return nil
	}

	path := paths.SystemdUnitPath(unit.Name)
	if unit.Mask != nil && *unit.Mask {
		link, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("state validation: error while evaluation symlink for path %q: %w", path, err)
		}

		if link != pathDevNull {
			return fmt.Errorf("state validation: invalid unit masked setting. path: %q; expected: %v; received: %v", path, pathDevNull, link)
		}

		// Return early if the unit is masked.
		// See: https://issues.redhat.com/browse/OCPBUGS-3636
		return nil
	}

	if err := checkFileContentsAndMode(path, []byte(*unit.Contents), defaultFilePermissions); err != nil {
		return err
	}

	return nil
}

// checkV3Units validates the contents of all the units in the
// target config and returns nil if they match.
func checkV3Units(units []ign3types.Unit, paths Paths) error {
	for _, unit := range units {
		if err := checkV3Unit(unit, paths); err != nil {
			return err
		}
	}

	return nil
}

// checkV2Units validates the contents of a given unit in the
// target config and returns nil if they match.
func checkV2Unit(unit ign2types.Unit, paths Paths) error {
	for _, dropin := range unit.Dropins {
		path := paths.SystemdDropinPath(unit.Name, dropin.Name)
		if err := checkFileContentsAndMode(path, []byte(dropin.Contents), defaultFilePermissions); err != nil {
			return err
		}
	}

	if unit.Contents == "" {
		// Return early if contents are empty
		return nil
	}

	path := paths.SystemdUnitPath(unit.Name)
	if unit.Mask {
		link, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("state validation: error while evaluation symlink for path %q: %w", path, err)
		}

		if link != pathDevNull {
			return fmt.Errorf("state validation: invalid unit masked setting. path: %q; expected: %v; received: %v", path, pathDevNull, link)
		}

		// Return early if unit is masked
		// See: https://issues.redhat.com/browse/OCPBUGS-3636
		return nil
	}

	if err := checkFileContentsAndMode(path, []byte(unit.Contents), defaultFilePermissions); err != nil {
		return err
	}

	return nil
}

// checkV2Units validates the contents of all the units in the
// target config and returns nil if they match.
func checkV2Units(units []ign2types.Unit, paths Paths) error {
	for _, unit := range units {
		if err := checkV2Unit(unit, paths); err != nil {
			return err
		}
	}

	return nil
}

// checkV3Files validates the contents of all the files in the target config.
// V3 files should not have any duplication anymore, so there is no need to
// check for overwrites.
func checkV3Files(files []ign3types.File) error {
	for _, f := range files {
		if len(f.Append) > 0 {
			return fmt.Errorf("found an append section when checking files. Append is not supported")
		}
		mode := defaultFilePermissions
		if f.Mode != nil {
			mode = os.FileMode(*f.Mode)
		}
		contents, err := ctrlcommon.DecodeIgnitionFileContents(f.Contents.Source, f.Contents.Compression)
		if err != nil {
			return fmt.Errorf("couldn't decode file %q: %w", f.Path, err)
		}
		if err := checkFileContentsAndMode(f.Path, contents, mode); err != nil {
			return err
		}
	}
	return nil
}

// checkV2Files validates the contents of all the files in the target config.
func checkV2Files(files []ign2types.File) error {
	checkedFiles := make(map[string]bool)
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		// skip over checked validated files
		if _, ok := checkedFiles[f.Path]; ok {
			continue
		}
		if f.Append {
			return fmt.Errorf("found an append section when checking files. Append is not supported")
		}
		mode := defaultFilePermissions
		if f.Mode != nil {
			mode = os.FileMode(*f.Mode)
		}
		contents, err := ctrlcommon.DecodeIgnitionFileContents(&f.Contents.Source, &f.Contents.Compression)
		if err != nil {
			return fmt.Errorf("couldn't decode file %q: %w", f.Path, err)
		}
		if err := checkFileContentsAndMode(f.Path, contents, mode); err != nil {
			return err
		}
		checkedFiles[f.Path] = true
	}
	return nil
}

// Checks the mode of a given file.
func checkFileMode(filePath string, mode os.FileMode) error {
	fi, err := os.Lstat(filePath)
	if err != nil {
		return fmt.Errorf("could not stat file %q: %w", filePath, err)
	}
	if fi.Mode() != mode {
		return fmt.Errorf("mode mismatch for file: %q; expected: %[2]v/%[2]d/%#[2]o; received: %[3]v/%[3]d/%#[3]o", filePath, mode, fi.Mode())
	}

	return nil
}

// checkFileContentsAndMode reads the file from the filepath and compares its
// contents and mode with the expectedContent and mode parameters. It logs an
// error in case of an error or mismatch and returns the status of the
// evaluation.
func checkFileContentsAndMode(filePath string, expectedContent []byte, mode os.FileMode) error {
	if err := checkFileMode(filePath, mode); err != nil {
		return err
	}

	contents, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("could not read file %q: %w", filePath, err)
	}

	if !bytes.Equal(contents, expectedContent) {
		glog.Errorf("content mismatch for file %q (-want +got):\n%s", filePath, cmp.Diff(expectedContent, contents))
		return fmt.Errorf("content mismatch for file: %q", filePath)
	}

	return nil
}

// checkDirMode checks a given directory path and compares its mode to what is
// expected. It logs an error in the event of a mismatch.
func checkDirMode(dirPath string, expectedMode os.FileMode) error {
	fi, err := os.Lstat(dirPath)

	if err != nil {
		return fmt.Errorf("could not stat dir: %q: %w", dirPath, err)
	}

	if !fi.IsDir() {
		return fmt.Errorf("expected %q to be a dir", dirPath)
	}

	// We've already checked for the presence of the Dir mode bit above, so we
	// subtract it from the actual mode since it will cause a false negative otherwise.
	actualMode := fi.Mode() - os.ModeDir

	if actualMode != expectedMode {
		return fmt.Errorf("mode mismatch for dir: %q; expected: %[2]v/%[2]d/%#[2]o; received: %[3]v/%[3]d/%#[3]o", dirPath, expectedMode, actualMode)
	}

	return nil
}
