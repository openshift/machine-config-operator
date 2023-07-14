package daemon

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/google/renameio"
	"k8s.io/klog/v2"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

var (
	origParentDirPath   = filepath.Join("/etc", "machine-config-daemon", "orig")
	noOrigParentDirPath = filepath.Join("/etc", "machine-config-daemon", "noorig")
	usrPath             = "/usr"
)

func origParentDir() string {
	return origParentDirPath
}

func noOrigParentDir() string {
	return noOrigParentDirPath
}

func origFileName(fpath string) string {
	return filepath.Join(origParentDir(), fpath+".mcdorig")
}

// We use this to create a file that indicates that no original file existed on disk
// when we write a file via a MachineConfig. Otherwise the MCD does not differentiate
// between "a file existed due to a previous machineconfig" vs "a file existed on disk
// before the MCD took over". Also see deleteStaleData() above.
//
// The "stamp" part of the name indicates it is not an actual backup file, just an
// empty file to indicate lack of previous existence.
func noOrigFileStampName(fpath string) string {
	return filepath.Join(noOrigParentDir(), fpath+".mcdnoorig")
}

func createOrigFile(fromPath, fpath string) error {
	if _, err := os.Stat(noOrigFileStampName(fpath)); err == nil {
		// we already created the no orig file for this default file
		return nil
	}
	if _, err := os.Stat(fpath); os.IsNotExist(err) {
		// create a noorig file that tells the MCD that the file wasn't present on disk before MCD
		// took over so it can just remove it when deleting stale data, as opposed as restoring a file
		// that was shipped _with_ the underlying OS (e.g. a default chrony config).
		if makeErr := os.MkdirAll(filepath.Dir(noOrigFileStampName(fpath)), 0o755); makeErr != nil {
			return fmt.Errorf("creating no orig parent dir: %w", makeErr)
		}
		return writeFileAtomicallyWithDefaults(noOrigFileStampName(fpath), nil)
	}

	// https://bugzilla.redhat.com/show_bug.cgi?id=1970959
	// orig file might exist, but be a relative/dangling symlink
	if symlinkTarget, err := os.Readlink(origFileName(fpath)); err == nil {
		if symlinkTarget != "" {
			return nil
		}
	}
	if _, err := os.Stat(origFileName(fpath)); err == nil {
		// the orig file is already there and we avoid creating a new one to preserve the real default
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(origFileName(fpath)), 0o755); err != nil {
		return fmt.Errorf("creating orig parent dir: %w", err)
	}
	if out, err := exec.Command("cp", "-a", "--reflink=auto", fromPath, origFileName(fpath)).CombinedOutput(); err != nil {
		return fmt.Errorf("creating orig file for %q: %s: %w", fpath, string(out), err)
	}
	return nil
}

func writeFileAtomicallyWithDefaults(fpath string, b []byte) error {
	return writeFileAtomically(fpath, b, defaultDirectoryPermissions, defaultFilePermissions, -1, -1)
}

// writeFileAtomically uses the renameio package to provide atomic file writing, we can't use renameio.WriteFile
// directly since we need to 1) Chown 2) go through a buffer since files provided can be big
func writeFileAtomically(fpath string, b []byte, dirMode, fileMode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(fpath)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}
	t, err := renameio.TempFile(dir, fpath)
	if err != nil {
		return err
	}
	defer t.Cleanup()
	// Set permissions before writing data, in case the data is sensitive.
	if err := t.Chmod(fileMode); err != nil {
		return err
	}
	w := bufio.NewWriter(t)
	if _, err := w.Write(b); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if uid != -1 && gid != -1 {
		if err := t.Chown(uid, gid); err != nil {
			return err
		}
	}
	return t.CloseAtomicallyReplace()
}

// write dropins to disk
func writeDropins(u ign3types.Unit, systemdRoot string, isCoreOSVariant bool) error {
	for i := range u.Dropins {
		dpath := filepath.Join(systemdRoot, u.Name+".d", u.Dropins[i].Name)
		if u.Dropins[i].Contents == nil || *u.Dropins[i].Contents == "" {
			klog.Infof("Dropin for %s has no content, skipping write", u.Dropins[i].Name)
			if _, err := os.Stat(dpath); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			klog.Infof("Removing %q, updated file has zero length", dpath)
			if err := os.Remove(dpath); err != nil {
				return err
			}
			continue
		}

		klog.Infof("Writing systemd unit dropin %q", u.Dropins[i].Name)
		if _, err := os.Stat(withUsrPath(dpath)); err == nil &&
			isCoreOSVariant {
			if err := createOrigFile(withUsrPath(dpath), dpath); err != nil {
				return err
			}
		}
		if err := writeFileAtomicallyWithDefaults(dpath, []byte(*u.Dropins[i].Contents)); err != nil {
			return fmt.Errorf("failed to write systemd unit dropin %q: %w", u.Dropins[i].Name, err)
		}

		klog.V(2).Infof("Wrote systemd unit dropin at %s", dpath)
	}

	return nil
}

// writeFiles writes the given files to disk.
// it doesn't fetch remote files and expects a flattened config file.
func writeFiles(files []ign3types.File, skipCertificateWrite bool) error {
	for _, file := range files {
		if skipCertificateWrite && file.Path == caBundleFilePath {
			// TODO remove this special case once we have a better way to do this
			klog.V(4).Infof("Skipping file %s during writeFiles", caBundleFilePath)
			continue
		}
		klog.Infof("Writing file %q", file.Path)

		// We don't support appends in the file section, so instead of waiting to fail validation,
		// let's explicitly fail here.
		if len(file.Append) > 0 {
			return fmt.Errorf("found an append section when writing files. Append is not supported")
		}

		decodedContents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
		if err != nil {
			return fmt.Errorf("could not decode file %q: %w", file.Path, err)
		}

		mode := defaultFilePermissions
		if file.Mode != nil {
			mode = os.FileMode(*file.Mode)
		}

		// set chown if file information is provided
		uid, gid, err := getFileOwnership(file)
		if err != nil {
			return fmt.Errorf("failed to retrieve file ownership for file %q: %w", file.Path, err)
		}
		if err := createOrigFile(file.Path, file.Path); err != nil {
			return err
		}
		if err := writeFileAtomically(file.Path, decodedContents, defaultDirectoryPermissions, mode, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// writeUnit writes a systemd unit and its dropins to disk
func writeUnit(u ign3types.Unit, systemdRoot string, isCoreOSVariant bool) error {
	if err := writeDropins(u, systemdRoot, isCoreOSVariant); err != nil {
		return err
	}

	// write (or cleanup) path in /etc/systemd/system
	fpath := filepath.Join(systemdRoot, u.Name)
	if u.Mask != nil && *u.Mask {
		// if the unit is masked, symlink fpath to /dev/null and return early.

		klog.V(2).Info("Systemd unit masked")
		if err := os.RemoveAll(fpath); err != nil {
			return fmt.Errorf("failed to remove unit %q: %w", u.Name, err)
		}
		klog.V(2).Infof("Removed unit %q", u.Name)

		if err := renameio.Symlink(pathDevNull, fpath); err != nil {
			return fmt.Errorf("failed to symlink unit %q to %s: %w", u.Name, pathDevNull, err)
		}
		klog.V(2).Infof("Created symlink unit %q to %s", u.Name, pathDevNull)

		// Return early since we don't need to write the file contents in this case.
		return nil
	}

	if u.Contents != nil && *u.Contents != "" {
		klog.Infof("Writing systemd unit %q", u.Name)
		if _, err := os.Stat(withUsrPath(fpath)); err == nil &&
			isCoreOSVariant {
			if err := createOrigFile(withUsrPath(fpath), fpath); err != nil {
				return err
			}
		}
		if err := writeFileAtomicallyWithDefaults(fpath, []byte(*u.Contents)); err != nil {
			return fmt.Errorf("failed to write systemd unit %q: %w", u.Name, err)
		}

		klog.V(2).Infof("Successfully wrote systemd unit %q: ", u.Name)
	} else if u.Mask != nil && !*u.Mask {
		// if mask is explicitly set to false, make sure to remove a previous mask
		// see https://bugzilla.redhat.com/show_bug.cgi?id=1966445
		// Note that this does not catch all cleanup cases; for example, if the previous machine config specified
		// Contents, and the current one does not, the previous content will not get cleaned up. For now we're ignoring some
		// of those edge cases rather than introducing more complexity.
		klog.V(2).Infof("Ensuring systemd unit %q has no mask at %q", u.Name, fpath)
		if err := os.RemoveAll(fpath); err != nil {
			return fmt.Errorf("failed to cleanup %s: %w", fpath, err)
		}
	}

	return nil
}

// writeUnits writes systemd units and their dropins to disk
func writeUnits(units []ign3types.Unit, systemdRoot string, isCoreOSVariant bool) error {
	for _, u := range units {
		if err := writeUnit(u, systemdRoot, isCoreOSVariant); err != nil {
			return err
		}
	}

	return nil
}

func lookupUID(username string) (int, error) {
	osUser, err := user.Lookup(username)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve UserID for username: %s", username)
	}
	klog.V(2).Infof("Retrieved UserId: %s for username: %s", osUser.Uid, username)
	uid, _ := strconv.Atoi(osUser.Uid)
	return uid, nil
}

func lookupGID(group string) (int, error) {
	osGroup, err := user.LookupGroup(group)
	if err != nil {
		return 0, fmt.Errorf("failed to retrieve GroupID for group: %v", group)
	}
	klog.V(2).Infof("Retrieved GroupID: %s for group: %s", osGroup.Gid, group)
	gid, _ := strconv.Atoi(osGroup.Gid)
	return gid, nil
}

// This is essentially ResolveNodeUidAndGid() from Ignition; XXX should dedupe
func getFileOwnership(file ign3types.File) (int, int, error) {
	uid, gid := 0, 0 // default to root
	var err error    // create default error var
	if file.User.ID != nil {
		uid = *file.User.ID
	} else if file.User.Name != nil && *file.User.Name != "" {
		uid, err = lookupUID(*file.User.Name)
		if err != nil {
			return uid, gid, err
		}
	}

	if file.Group.ID != nil {
		gid = *file.Group.ID
	} else if file.Group.Name != nil && *file.Group.Name != "" {
		gid, err = lookupGID(*file.Group.Name)
		if err != nil {
			return uid, gid, err
		}
	}
	return uid, gid, nil
}

// Appends the usrPath variable (/usr) to a given path
func withUsrPath(path string) string {
	return filepath.Join(usrPath, path)
}
