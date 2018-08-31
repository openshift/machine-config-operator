package daemon

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/vincent-petithory/dataurl"
	drain "github.com/openshift/kubernetes-drain"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// update the node to the provided node configuration.
func (dn *Daemon) update(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	var err error
	// update files on disk that need updating
	err = dn.updateFiles(oldConfig, newConfig)
	if err != nil {
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

	// reboot. this function shouldn't actually return.
	return dn.reboot()
}

// updateFiles writes files specified by the nodeconfig to disk. it also writes
// systemd units. there is no support for multiple filesystems at this point.
//
// in addition to files, we also write systemd units to disk. we mask unit files
// when appropriate, but we ignore requests to enable or disable units.
// this function relies on the system being restarted after an upgrade,
// so it doesn't daemon-reload or restart any services.
//
// it is worth noting that this function explicitly doesn't rely on the ignition
// implementation of file and unit writing. this is because ignition is built on
// the assumption that it is working with a fresh system, where as we are trying
// to reconcile a system that has already been running.
//
// in the future, this function should do any additional work to confirm that
// whatever has been written is picked up by the appropriate daemons, if
// required. in particular, a daemon-reload and restart for any unit files
// touched. enabling and disabling systemd units should be supported as well.
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
	newFileSet := make(map[string]struct{})
	for _, f := range newConfig.Spec.Config.Storage.Files {
		path = filepath.Join(dn.prefix, f.Path)
		newFileSet[path] = struct{}{}
	}

	for _, f := range oldConfig.Spec.Config.Storage.Files {
		path = filepath.Join(dn.prefix, f.Path)
		if _, ok := newFileSet[path]; !ok {
			os.RemoveAll(path)
		}
	}

	newUnitSet := make(map[string]struct{})
	newDropinSet := make(map[string]struct{})
	for _, u := range newConfig.Spec.Config.Systemd.Units {
		for j := range u.Dropins {
			path = filepath.Join(dn.prefix, pathSystemd, u.Name+".d", u.Dropins[j].Name)
			newDropinSet[path] = struct{}{}
		}
		path = filepath.Join(dn.prefix, pathSystemd, u.Name)
		newUnitSet[path] = struct{}{}
	}

	for _, u := range oldConfig.Spec.Config.Systemd.Units {
		for j := range u.Dropins {
			path = filepath.Join(dn.prefix, pathSystemd, u.Name+".d", u.Dropins[j].Name)
			if _, ok := newDropinSet[path]; !ok {
				os.RemoveAll(path)
			}
		}
		path = filepath.Join(dn.prefix, pathSystemd, u.Name)
		if _, ok := newUnitSet[path]; !ok {
			os.RemoveAll(path)
		}
	}
}

// writeUnits writes the systemd units to disk
func (dn *Daemon) writeUnits(units []ignv2_2types.Unit) error {
	var path string
	for _, u := range units {
		// write the dropin to disk
		for i := range u.Dropins {
			glog.Infof("writing systemd unit dropin %q", u.Dropins[i].Name)
			path = filepath.Join(dn.prefix, pathSystemd, u.Name+".d", u.Dropins[i].Name)
			if err := os.MkdirAll(filepath.Dir(path), os.FileMode(0655)); err != nil {
				return fmt.Errorf("Failed to create directory %q: %v", filepath.Dir(path), err)
			}

			err := ioutil.WriteFile(path, []byte(u.Dropins[i].Contents), os.FileMode(0644))
			if err != nil {
				return fmt.Errorf("Failed to write systemd unit dropin %q: %v", u.Dropins[i].Name, err)
			}
		}

		if u.Contents == "" {
			continue
		}

		glog.Infof("writing systemd unit %q", u.Name)
		path = filepath.Join(dn.prefix, pathSystemd, u.Name)
		if err := os.MkdirAll(filepath.Dir(path), os.FileMode(0655)); err != nil {
			return fmt.Errorf("Failed to create directory %q: %v", filepath.Dir(path), err)
		}

		// check if the unit is masked. if it is, we write a symlink to
		// /dev/null and contiue
		if u.Mask {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("Failed to remove unit %q: %v", u.Name, err)
			}
			if err := os.Symlink(pathDevNull, path); err != nil {
				return fmt.Errorf("Failed to symlink unit %q to %s: %v", u.Name, pathDevNull, err)
			}
			continue
		}

		// write the unit to disk
		err := ioutil.WriteFile(path, []byte(u.Contents), os.FileMode(0644))
		if err != nil {
			return fmt.Errorf("Failed to write systemd unit %q: %v", u.Name, err)
		}

	}
	return nil
}

// writeFiles writes the given files to disk.
// it doesn't fetch remote files and expects a flattened config file.
func (dn *Daemon) writeFiles(files []ignv2_2types.File) error {
	for _, f := range files {
		path := dn.prefix + f.Path

		glog.Infof("Writing file %q", f.Path)
		// create any required directories for the file
		if err := os.MkdirAll(filepath.Dir(path), os.FileMode(0655)); err != nil {
			return fmt.Errorf("Failed to create directory %q: %v", filepath.Dir(f.Path), err)
		}

		// create the file
		file, err := os.Create(path)
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
		mode := os.FileMode(0644)
		if f.Mode != nil {
			mode = os.FileMode(*f.Mode)
		}
		err = file.Chmod(mode)
		if err != nil {
			return fmt.Errorf("Failed to set file mode for file %q: %v", f.Path, err)
		}

		// set chown if file information is provided
		if f.User.ID != nil || f.User.Name != "" {
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

func getFileOwnership(file ignv2_2types.File) (int, int, error) {
	var uid, gid int
	if file.User.ID != nil && file.Group.ID != nil {
		uid = *file.User.ID
		gid = *file.Group.ID
	} else if file.User.Name != "" {
		osUser, err := user.Lookup(file.User.Name)
		if err != nil {
			return uid, gid, fmt.Errorf("Failed to retrieve UserID for username: %s", file.User.Name)
		}

		uid, _ = strconv.Atoi(osUser.Uid)
		gid, _ = strconv.Atoi(osUser.Gid)
	}
	return uid, gid, nil
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
