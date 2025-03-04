package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/google/go-cmp/cmp"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

// Validates that the on-disk state matches a given MachineConfig.
func validateOnDiskState(currentConfig *mcfgv1.MachineConfig, systemdPath string) error {
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
		if err := checkV3Units(ignconfigi.(ign3types.Config).Systemd.Units, systemdPath); err != nil {
			return &unitConfigDriftErr{err}
		}
		return nil
	case ign2types.Config:
		if err := checkV2Files(ignconfigi.(ign2types.Config).Storage.Files); err != nil {
			return &fileConfigDriftErr{err}
		}
		if err := checkV2Units(ignconfigi.(ign2types.Config).Systemd.Units, systemdPath); err != nil {
			return &unitConfigDriftErr{err}
		}
		return nil
	default:
		return fmt.Errorf("unexpected type for ignition config: %v", typedConfig)
	}
}

// Checks that the ondisk state for a systemd dropin matches the expected state.
func checkV3Dropin(systemdPath string, unit ign3types.Unit, dropin ign3types.Dropin) error {
	path := getIgn3SystemdDropinPath(systemdPath, unit, dropin)

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
func checkV3Unit(unit ign3types.Unit, systemdPath string) error {
	for _, dropin := range unit.Dropins {
		if err := checkV3Dropin(systemdPath, unit, dropin); err != nil {
			return err
		}
	}

	if unit.Contents == nil || *unit.Contents == "" {
		// Return early if the contents are empty.
		return nil
	}

	path := getIgn3SystemdUnitPath(systemdPath, unit)
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

	return checkFileContentsAndMode(path, []byte(*unit.Contents), defaultFilePermissions)
}

// checkV3Units validates the contents of all the units in the
// target config and returns nil if they match.
func checkV3Units(units []ign3types.Unit, systemdPath string) error {
	for _, unit := range units {
		if err := checkV3Unit(unit, systemdPath); err != nil {
			return err
		}
	}

	return nil
}

// checkV2Units validates the contents of a given unit in the
// target config and returns nil if they match.
func checkV2Unit(unit ign2types.Unit, systemdPath string) error {
	for _, dropin := range unit.Dropins {
		path := getIgn2SystemdDropinPath(systemdPath, unit, dropin)
		if err := checkFileContentsAndMode(path, []byte(dropin.Contents), defaultFilePermissions); err != nil {
			return err
		}
	}

	if unit.Contents == "" {
		// Return early if contents are empty
		return nil
	}

	path := getIgn2SystemdUnitPath(systemdPath, unit)
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

	return checkFileContentsAndMode(path, []byte(unit.Contents), defaultFilePermissions)
}

// checkV2Units validates the contents of all the units in the
// target config and returns nil if they match.
func checkV2Units(units []ign2types.Unit, systemdPath string) error {
	for _, unit := range units {
		if err := checkV2Unit(unit, systemdPath); err != nil {
			return err
		}
	}

	return nil
}

// To transition certain files from being under MachineConfig management to
// certificate_Writer, we must ignore these files at validation time since
// there is the possibility that certificate_writer may write different
// contents to the file during the transition.
func getFilesToIgnore() sets.Set[string] {
	return sets.New[string](
		caBundleFilePath,
		cloudCABundleFilePath,
		internalRegistryAuthFile,
	)
}

// checkV3Files validates the contents of all the files in the target config.
// V3 files should not have any duplication anymore, so there is no need to
// check for overwrites.
func checkV3Files(files []ign3types.File) error {
	filesToIgnore := getFilesToIgnore()

	for _, f := range files {
		if filesToIgnore.Has(f.Path) {
			klog.V(4).Infof("Skipping file %s during checkV3Files", f.Path)
			continue
		}
		if len(f.Append) > 0 {
			return fmt.Errorf("found an append section when checking files. Append is not supported")
		}
		mode := defaultFilePermissions
		if f.Mode != nil {
			mode = os.FileMode(*f.Mode) //nolint:gosec
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
			mode = os.FileMode(*f.Mode) //nolint:gosec
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

// checkFileContentsAndMode reads the file from the filepath and compares its
// contents and mode with the expectedContent and mode parameters. It logs an
// error in case of an error or mismatch and returns the status of the
// evaluation.
func checkFileContentsAndMode(filePath string, expectedContent []byte, mode os.FileMode) error {
	fi, err := os.Lstat(filePath)
	if err != nil {
		return fmt.Errorf("could not stat file %q: %w", filePath, err)
	}
	if fi.Mode() != mode {
		return fmt.Errorf("mode mismatch for file: %q; expected: %[2]v/%[2]d/%#[2]o; received: %[3]v/%[3]d/%#[3]o", filePath, mode, fi.Mode())
	}
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("could not read file %q: %w", filePath, err)
	}
	if !bytes.Equal(contents, expectedContent) {
		klog.Errorf("content mismatch for file %q (-want +got):\n%s", filePath, cmp.Diff(expectedContent, contents))
		return fmt.Errorf("content mismatch for file %q", filePath)
	}
	return nil
}

// Gets the absolute path for a systemd unit and dropin, given a root path.
func getIgn2SystemdDropinPath(systemdPath string, unit ign2types.Unit, dropin ign2types.SystemdDropin) string {
	return filepath.Join(getSystemdPath(systemdPath), unit.Name+".d", dropin.Name)
}

// Gets the absolute path for a systemd unit and dropin, given a root path.
func getIgn3SystemdDropinPath(systemdPath string, unit ign3types.Unit, dropin ign3types.Dropin) string {
	return filepath.Join(getSystemdPath(systemdPath), unit.Name+".d", dropin.Name)
}

// Computes the absolute path for a given system unit file.
func getIgn2SystemdUnitPath(systemdPath string, unit ign2types.Unit) string {
	return filepath.Join(getSystemdPath(systemdPath), unit.Name)
}

// Computes the absolute path for a given system unit file.
func getIgn3SystemdUnitPath(systemdPath string, unit ign3types.Unit) string {
	return filepath.Join(getSystemdPath(systemdPath), unit.Name)
}

// Gets the systemd path. Defaults to pathSystemd, if empty.
func getSystemdPath(systemdPath string) string {
	if systemdPath == "" {
		systemdPath = pathSystemd
	}

	return systemdPath
}
