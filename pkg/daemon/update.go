package daemon

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	drain "github.com/openshift/kubernetes-drain"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DefaultDirectoryPermissions os.FileMode = 0755
	DefaultFilePermissions      os.FileMode = 0644
)

// update the node to the provided node configuration.
func (dn *Daemon) update(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	var err error

	glog.Info("Updating node with new config")

	// make sure we can actually reconcile this state
	reconcilable, err := dn.reconcilable(oldConfig, newConfig)
	if err != nil {
		return err
	}
	if !reconcilable {
		return fmt.Errorf("daemon can't reconcile this config")
	}

	// update files on disk that need updating
	if err = dn.updateFiles(oldConfig, newConfig); err != nil {
		return err
	}

	if err = dn.updateOS(oldConfig, newConfig); err != nil {
		return err
	}

	glog.Info("Update completed. Draining the node.")

	node, err := dn.kubeClient.CoreV1().Nodes().Get(dn.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	err = drain.Drain(dn.kubeClient, []*corev1.Node{node}, &drain.DrainOptions{
		DeleteLocalData:    true,
		Force:              true,
		GracePeriodSeconds: 600,
		IgnoreDaemonsets:   true,
	})
	if err != nil {
		return err
	}
	glog.V(2).Infof("Node successfully drained")

	// reboot. this function shouldn't actually return.
	return dn.reboot()
}

// reconcilable checks the configs to make sure that the only changes requested
// are ones we know how to do in-place. if we can't do it in place, the node is
// marked as degraded.
//
// we can only update machine configs that have changes to the files,
// directories, links, and systemd units sections of the included ignition
// config currently.
func (dn *Daemon) reconcilable(oldConfig, newConfig *mcfgv1.MachineConfig) (bool, error) {
	glog.Info("Checking if configs are reconcilable")

	// We skip out of reconcilable if there is no Kind and we are in runOnce mode. The
	// reason is that there is a good chance a previous state is not available to match against.
	if oldConfig.Kind == "" && dn.onceFrom != "" {
		glog.Infof("Missing kind in old config. Assuming reconcilable with new.")
		return true, nil
	}
	oldIgn := oldConfig.Spec.Config
	newIgn := newConfig.Spec.Config

	// Ignition section

	// if the config versions are different, all bets are off. this probably
	// shouldn't happen, but if it does, we can't deal with it.
	if oldIgn.Ignition.Version != newIgn.Ignition.Version {
		glog.Warningf("daemon can't reconcile state!")
		glog.Warningf("Ignition version mismatch between old and new config: old: %s new: %s",
			oldIgn.Ignition.Version, newIgn.Ignition.Version)
		return false, nil
	}
	// everything else in the ignition section doesn't matter to us, since the
	// rest of the stuff in this section has to do with fetching remote
	// resources, and the mcc should've fully rendered those out before the
	// config gets here.

	// Networkd section

	// we don't currently configure the network in place. we can't fix it if
	// something changed here.
	if !reflect.DeepEqual(oldIgn.Networkd, newIgn.Networkd) {
		glog.Warningf("daemon can't reconcile state!")
		glog.Warningf("Ignition networkd section contains changes")
		return false, nil
	}

	// Passwd section

	// we don't currently configure groups or users in place. we can't fix it if
	// something changed here.
	if !reflect.DeepEqual(oldIgn.Passwd, newIgn.Passwd) {
		glog.Warningf("daemon can't reconcile state!")
		glog.Warningf("Ignition passwd section contains changes")
		return false, nil
	}

	// Storage section

	// there are six subsections here - directories, files, and links, which we
	// can reconcile, and disks, filesystems, and raid, which we can't. make
	// sure the sections we can't fix aren't changed.
	if !reflect.DeepEqual(oldIgn.Storage.Disks, newIgn.Storage.Disks) {
		glog.Warningf("daemon can't reconcile state!")
		glog.Warningf("Ignition disks section contains changes")
		return false, nil
	}
	if !reflect.DeepEqual(oldIgn.Storage.Filesystems, newIgn.Storage.Filesystems) {
		glog.Warningf("daemon can't reconcile state!")
		glog.Warningf("Ignition filesystems section contains changes")
		return false, nil
	}
	if !reflect.DeepEqual(oldIgn.Storage.Raid, newIgn.Storage.Raid) {
		glog.Warningf("daemon can't reconcile state!")
		glog.Warningf("Ignition raid section contains changes")
		return false, nil
	}

	// Special case files append: if the new config wants us to append, then we
	// have to force a reprovision since it's not idempotent
	for _, f := range newIgn.Storage.Files {
		if f.Append {
			glog.Warningf("daemon can't reconcile state!")
			glog.Warningf("Ignition files includes append")
			return false, nil
		}
	}

	// Systemd section

	// we can reconcile any state changes in the systemd section.

	// we made it through all the checks. reconcile away!
	glog.V(2).Infof("Configs are reconcilable")
	return true, nil
}

// updateFiles writes files specified by the nodeconfig to disk. it also writes
// systemd units. there is no support for multiple filesystems at this point.
//
// in addition to files, we also write systemd units to disk. we mask, enable,
// and disable unit files when appropriate. this function relies on the system
// being restarted after an upgrade, so it doesn't daemon-reload or restart
// any services.
//
// it is worth noting that this function explicitly doesn't rely on the ignition
// implementation of file, unit writing, enabling or disabling. this is because
// ignition is built on the assumption that it is working with a fresh system,
// where as we are trying to reconcile a system that has already been running.
//
// in the future, this function should do any additional work to confirm that
// whatever has been written is picked up by the appropriate daemons, if
// required. in particular, a daemon-reload and restart for any unit files
// touched.
func (dn *Daemon) updateFiles(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	glog.Info("Updating files")

	if err := dn.writeFiles(newConfig.Spec.Config.Storage.Files); err != nil {
		return err
	}

	if err := dn.writeUnits(newConfig.Spec.Config.Systemd.Units); err != nil {
		return err
	}

	dn.deleteStaleData(oldConfig, newConfig)

	return nil
}

// deleteStaleData performs a diff of the new and the old config. It then deletes
// all the files, units that are present in the old config but not in the new one.
// this function doesn't cause the agent to stop on failures and logs any errors
// it encounters.
func (dn *Daemon) deleteStaleData(oldConfig, newConfig *mcfgv1.MachineConfig) {
	var path string
	glog.Info("Deleting stale data")
	newFileSet := make(map[string]struct{})
	for _, f := range newConfig.Spec.Config.Storage.Files {
		newFileSet[f.Path] = struct{}{}
	}

	glog.V(2).Info("Removing stale config storage files")
	for _, f := range oldConfig.Spec.Config.Storage.Files {
		if _, ok := newFileSet[f.Path]; !ok {
			dn.fileSystemClient.RemoveAll(path)
		}
	}

	glog.V(2).Info("Removing stale config systemd units")
	newUnitSet := make(map[string]struct{})
	newDropinSet := make(map[string]struct{})
	for _, u := range newConfig.Spec.Config.Systemd.Units {
		for j := range u.Dropins {
			path = filepath.Join(pathSystemd, u.Name+".d", u.Dropins[j].Name)
			newDropinSet[path] = struct{}{}
		}
		path = filepath.Join(pathSystemd, u.Name)
		newUnitSet[path] = struct{}{}
	}

	for _, u := range oldConfig.Spec.Config.Systemd.Units {
		for j := range u.Dropins {
			path = filepath.Join(pathSystemd, u.Name+".d", u.Dropins[j].Name)
			if _, ok := newDropinSet[path]; !ok {
				dn.fileSystemClient.RemoveAll(path)
			}
		}
		path = filepath.Join(pathSystemd, u.Name)
		if _, ok := newUnitSet[path]; !ok {
			if err := dn.disableUnit(u); err != nil {
				glog.Warningf("Unable to disable %s: %s", u.Name, err)
			}
			dn.fileSystemClient.RemoveAll(path)
		}
	}
}

// enableUnit enables a systemd unit via symlink
func (dn *Daemon) enableUnit(unit ignv2_2types.Unit) error {
	// The link location
	wantsPath := filepath.Join(wantsPathSystemd, unit.Name)
	// sanity check that we don't return an error when the link already exists
	if _, err := dn.fileSystemClient.Stat(wantsPath); err == nil {
		glog.Infof("%s already exists. Not making a new symlink", wantsPath)
		return nil
	}
	// The originating file to link
	servicePath := filepath.Join(pathSystemd, unit.Name)
	err := dn.fileSystemClient.Symlink(servicePath, wantsPath)
	if err != nil {
		glog.Warningf("Cannot enable unit %s: %s", unit.Name, err)
	} else {
		glog.Infof("Enabled %s", unit.Name)
		glog.V(2).Infof("Symlinked %s to %s", servicePath, wantsPath)
	}
	return err
}

// disableUnit disables a systemd unit via symlink removal
func (dn *Daemon) disableUnit(unit ignv2_2types.Unit) error {
	// The link location
	wantsPath := filepath.Join(wantsPathSystemd, unit.Name)
	// sanity check so we don't return an error when the unit was already disabled
	if _, err := dn.fileSystemClient.Stat(wantsPath); err != nil {
		glog.Infof("%s was not present. No need to remove", wantsPath)
		return nil
	}
	glog.V(2).Infof("Disabling unit at %s", wantsPath)

	return dn.fileSystemClient.Remove(wantsPath)
}

// writeUnits writes the systemd units to disk
func (dn *Daemon) writeUnits(units []ignv2_2types.Unit) error {
	var path string
	for _, u := range units {
		// write the dropin to disk
		for i := range u.Dropins {
			glog.Infof("Writing systemd unit dropin %q", u.Dropins[i].Name)
			path = filepath.Join(pathSystemd, u.Name+".d", u.Dropins[i].Name)
			if err := dn.fileSystemClient.MkdirAll(filepath.Dir(path), DefaultDirectoryPermissions); err != nil {
				return fmt.Errorf("Failed to create directory %q: %v", filepath.Dir(path), err)
			}
			glog.V(2).Infof("Created directory: %s", path)

			err := ioutil.WriteFile(path, []byte(u.Dropins[i].Contents), os.FileMode(0644))
			if err != nil {
				return fmt.Errorf("Failed to write systemd unit dropin %q: %v", u.Dropins[i].Name, err)
			}
			glog.V(2).Infof("Wrote systemd unit dropin at %s", path)
		}

		if u.Contents == "" {
			continue
		}

		glog.Infof("Writing systemd unit %q", u.Name)
		path = filepath.Join(pathSystemd, u.Name)
		if err := dn.fileSystemClient.MkdirAll(filepath.Dir(path), DefaultDirectoryPermissions); err != nil {
			return fmt.Errorf("Failed to create directory %q: %v", filepath.Dir(path), err)
		}
		glog.V(2).Infof("Created directory: %s", path)

		// check if the unit is masked. if it is, we write a symlink to
		// /dev/null and continue
		if u.Mask {
			glog.V(2).Infof("Systemd unit masked.")
			if err := dn.fileSystemClient.RemoveAll(path); err != nil {
				return fmt.Errorf("Failed to remove unit %q: %v", u.Name, err)
			}
			glog.V(2).Infof("Removed unit %q", u.Name)

			if err := dn.fileSystemClient.Symlink(pathDevNull, path); err != nil {
				return fmt.Errorf("Failed to symlink unit %q to %s: %v", u.Name, pathDevNull, err)
			}
			glog.V(2).Infof("Created symlink unit %q to %s", u.Name, pathDevNull)

			continue
		}

		// write the unit to disk
		err := ioutil.WriteFile(path, []byte(u.Contents), os.FileMode(DefaultFilePermissions))
		if err != nil {
			return fmt.Errorf("Failed to write systemd unit %q: %v", u.Name, err)
		}
		glog.V(2).Infof("Successfully wrote systemd unit %q: ", u.Name)

		// if the unit doesn't note if it should be enabled or disabled then
		// skip all linking.
		// if the unit should be enabled, then enable it.
		// otherwise the unit is disabled. run disableUnit to ensure the unit is
		// disabled. even if the unit wasn't previously enabled the result will
		// be fine as disableUnit is idempotent.
		// Note: we have to check for legacy unit.Enable and honor it
		glog.Infof("Enabling systemd unit %q", u.Name)
		if u.Enable == true {
			if err := dn.enableUnit(u); err != nil {
				return err
			}
			glog.V(2).Infof("Enabled systemd unit %q: ", u.Name)
		}
		if u.Enabled != nil {
			if *u.Enabled {
				if err := dn.enableUnit(u); err != nil {
					return err
				}
				glog.V(2).Infof("Enabled systemd unit %q: ", u.Name)
			} else {
				if err := dn.disableUnit(u); err != nil {
					return err
				}
				glog.V(2).Infof("Disabled systemd unit %q: ", u.Name)
			}
		}
	}
	return nil
}

// writeFiles writes the given files to disk.
// it doesn't fetch remote files and expects a flattened config file.
func (dn *Daemon) writeFiles(files []ignv2_2types.File) error {
	for _, f := range files {
		glog.Infof("Writing file %q", f.Path)
		// create any required directories for the file
		if err := dn.fileSystemClient.MkdirAll(filepath.Dir(f.Path), DefaultDirectoryPermissions); err != nil {
			return fmt.Errorf("Failed to create directory %q: %v", filepath.Dir(f.Path), err)
		}

		// create the file
		file, err := dn.fileSystemClient.Create(f.Path)
		if err != nil {
			return fmt.Errorf("Failed to create file %q: %v", f.Path, err)
		}

		// write the file to disk, using the inlined file contents
		contents, err := dataurl.DecodeString(f.Contents.Source)
		if err != nil {
			return err
		}
		_, err = file.WriteString(string(contents.Data))
		if err != nil {
			return fmt.Errorf("Failed to write inline contents to file %q: %v", f.Path, err)
		}

		// chmod and chown
		mode := DefaultFilePermissions
		if f.Mode != nil {
			mode = os.FileMode(*f.Mode)
		}
		err = file.Chmod(mode)
		if err != nil {
			return fmt.Errorf("Failed to set file mode for file %q: %v", f.Path, err)
		}

		// set chown if file information is provided
		if f.User != nil || f.Group != nil {
			uid, gid, err := getFileOwnership(f)
			if err != nil {
				return fmt.Errorf("Failed to retrieve file ownership for file %q: %v", f.Path, err)
			}
			err = file.Chown(uid, gid)
			if err != nil {
				return fmt.Errorf("Failed to set file ownership for file %q: %v", f.Path, err)
			}
		}

		err = file.Sync()
		if err != nil {
			return fmt.Errorf("Failed to sync file %q: %v", f.Path, err)
		}

		err = file.Close()
		if err != nil {
			return fmt.Errorf("Failed to close file %q: %v", f.Path, err)
		}
	}
	return nil
}

// This is essentially ResolveNodeUidAndGid() from Ignition; XXX should dedupe
func getFileOwnership(file ignv2_2types.File) (int, int, error) {
	uid, gid := 0, 0 // default to root
	if file.User != nil {
		if file.User.ID != nil {
			uid = *file.User.ID
		} else if file.User.Name != "" {
			osUser, err := user.Lookup(file.User.Name)
			if err != nil {
				return uid, gid, fmt.Errorf("Failed to retrieve UserID for username: %s", file.User.Name)
			}
			glog.V(2).Infof("Retrieved UserId: %s for username: %s", osUser.Uid, file.User.Name)
			uid, _ = strconv.Atoi(osUser.Uid)
		}
	}
	if file.Group != nil {
		if file.Group.ID != nil {
			gid = *file.Group.ID
		} else if file.Group.Name != "" {
			osGroup, err := user.LookupGroup(file.Group.Name)
			if err != nil {
				return uid, gid, fmt.Errorf("Failed to retrieve GroupID for group: %s", file.Group.Name)
			}
			glog.V(2).Infof("Retrieved GroupID: %s for group: %s", osGroup.Gid, file.Group.Name)
			gid, _ = strconv.Atoi(osGroup.Gid)
		}
	}
	return uid, gid, nil
}

// updateOS updates the system OS to the one specified in newConfig
func (dn *Daemon) updateOS(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// see similar logic in checkOS()
	if newConfig.Spec.OSImageURL == "://dummy" {
		glog.Warningf(`Working around "://dummy" OS image URL until installer ➰ pivots`)
		return nil
	}

	if newConfig.Spec.OSImageURL == dn.bootedOSImageURL {
		return nil
	}

	glog.Infof("Updating OS to %s", newConfig.Spec.OSImageURL)
	return dn.NodeUpdaterClient.RunPivot(newConfig.Spec.OSImageURL)
}

// reboot is the final step. it tells systemd-logind to reboot the machine,
// cleans up the agent's connections, and then sleeps for 7 days. if it wakes up
// and manages to return, it returns a scary error message.
func (dn *Daemon) reboot() error {
	glog.Info("Rebooting")

	// reboot
	dn.loginClient.Reboot(false)

	// cleanup
	dn.Close()

	// cross fingers
	time.Sleep(24 * 7 * time.Hour)

	// if everything went well, this should be unreachable.
	return fmt.Errorf("Reboot failed; this error should be unreachable, something is seriously wrong")
}
