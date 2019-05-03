package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"time"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/coreos/ignition/config/validate"
	"github.com/golang/glog"
	"github.com/google/renameio"
	drain "github.com/openshift/kubernetes-drain"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	errors "github.com/pkg/errors"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// defaultDirectoryPermissions houses the default mode to use when no directory permissions are provided
	defaultDirectoryPermissions os.FileMode = 0755
	// defaultFilePermissions houses the default mode to use when no file permissions are provided
	defaultFilePermissions os.FileMode = 0644
	// coreUser is "core" and currently the only permissible user name
	coreUserName = "core"
	// SSH Keys for user "core" will only be written at /home/core/.ssh
	coreUserSSHPath = "/home/core/.ssh/"
)

func writeFileAtomicallyWithDefaults(fpath string, b []byte) error {
	return writeFileAtomically(fpath, b, defaultDirectoryPermissions, defaultFilePermissions, -1, -1)
}

// writeFileAtomically uses the renameio package to provide atomic file writing, we can't use renameio.WriteFile
// directly since we need to 1) Chown 2) go through a buffer since files provided can be big
func writeFileAtomically(fpath string, b []byte, dirMode, fileMode os.FileMode, uid, gid int) error {
	if err := os.MkdirAll(filepath.Dir(fpath), dirMode); err != nil {
		return fmt.Errorf("failed to create directory %q: %v", filepath.Dir(fpath), err)
	}
	t, err := renameio.TempFile("", fpath)
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

func (dn *Daemon) writePendingState(desiredConfig *mcfgv1.MachineConfig) error {
	t := &pendingConfigState{
		PendingConfig: desiredConfig.GetName(),
		BootID:        dn.bootID,
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return writeFileAtomicallyWithDefaults(pathStateJSON, b)
}

func getNodeRef(node *corev1.Node) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		Kind: "Node",
		Name: node.GetName(),
		UID:  types.UID(node.GetUID()),
	}
}

// updateOSAndReboot is the last step in an update(), and it can also
// be called as a special case for the "bootstrap pivot".
func (dn *Daemon) updateOSAndReboot(newConfig *mcfgv1.MachineConfig) (retErr error) {
	if err := dn.updateOS(newConfig); err != nil {
		return err
	}

	if err := dn.writePendingState(newConfig); err != nil {
		return errors.Wrapf(err, "writing pending state")
	}
	defer func() {
		if retErr != nil {
			if err := os.Remove(pathStateJSON); err != nil {
				retErr = errors.Wrapf(retErr, "error removing pending config file %v", err)
				return
			}
		}
	}()
	if dn.recorder != nil {
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "PendingConfig", fmt.Sprintf("Written pending config %s", newConfig.GetName()))
	}

	// Skip draining of the node when we're not cluster driven
	if dn.onceFrom == "" {
		dn.logSystem("Update prepared; beginning drain")

		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "Drain", "Draining node to update config.")

		backoff := wait.Backoff{
			Steps:    5,
			Duration: 10 * time.Second,
			Factor:   2,
		}
		var lastErr error
		if err := wait.ExponentialBackoff(backoff, func() (bool, error) {
			err := drain.Drain(dn.kubeClient, []*corev1.Node{dn.node}, &drain.DrainOptions{
				DeleteLocalData:    true,
				Force:              true,
				GracePeriodSeconds: 600,
				IgnoreDaemonsets:   true,
			})
			if err == nil {
				return true, nil
			}
			lastErr = err
			glog.Infof("Draining failed with: %v, retrying", err)
			return false, nil
		}); err != nil {
			if err == wait.ErrWaitTimeout {
				return errors.Wrapf(lastErr, "failed to drain node (%d tries): %v", backoff.Steps, err)
			}
			return errors.Wrap(err, "failed to drain node")
		}
		dn.logSystem("drain complete")
	}

	// reboot. this function shouldn't actually return.
	return dn.reboot(fmt.Sprintf("Node will reboot into config %v", newConfig.GetName()), defaultRebootTimeout, exec.Command(defaultRebootCommand))
}

// isUpdating returns true if the MCD is actively applying an update
func (dn *Daemon) catchIgnoreSIGTERM() {
	if dn.installedSigterm {
		return
	}

	termChan := make(chan os.Signal, 1)
	signal.Notify(termChan, syscall.SIGTERM)

	dn.installedSigterm = true

	// Catch SIGTERM - if we're actively updating, we should avoid
	// having the process be killed.
	// https://github.com/openshift/machine-config-operator/issues/407
	go func() {
		for {
			<-termChan
			glog.Info("Got SIGTERM, but actively updating")
		}
	}()
}

var errUnreconcilable = errors.New("unreconcilable")

// update the node to the provided node configuration.
func (dn *Daemon) update(oldConfig, newConfig *mcfgv1.MachineConfig) (retErr error) {
	if dn.nodeWriter != nil {
		state, err := getNodeAnnotationExt(dn.node, constants.MachineConfigDaemonStateAnnotationKey, true)
		if err != nil {
			return err
		}
		if state != constants.MachineConfigDaemonStateDegraded && state != constants.MachineConfigDaemonStateUnreconcilable {
			if err := dn.nodeWriter.SetWorking(dn.kubeClient.CoreV1().Nodes(), dn.nodeLister, dn.name); err != nil {
				return errors.Wrap(err, "error setting node's state to Working")
			}
		}
	}

	dn.catchIgnoreSIGTERM()
	defer func() {
		if retErr != nil {
			dn.cancelSIGTERM()
		}
	}()

	oldConfigName := oldConfig.GetName()
	newConfigName := newConfig.GetName()
	glog.Infof("Checking reconcilable for config %v to %v", oldConfigName, newConfigName)
	// make sure we can actually reconcile this state
	reconcilableError := dn.reconcilable(oldConfig, newConfig)

	if reconcilableError != nil {
		wrappedErr := fmt.Errorf("can't reconcile config %s with %s: %v", oldConfigName, newConfigName, reconcilableError)
		if dn.recorder != nil {
			mcRef := &corev1.ObjectReference{
				Kind: "MachineConfig",
				Name: newConfig.GetName(),
				UID:  newConfig.GetUID(),
			}
			dn.recorder.Eventf(mcRef, corev1.EventTypeWarning, "FailedToReconcile", wrappedErr.Error())
		}
		dn.logSystem(wrappedErr.Error())
		return errors.Wrapf(errUnreconcilable, "%v", wrappedErr)
	}
	dn.logSystem("Starting update from %s to %s", oldConfigName, newConfigName)

	// update files on disk that need updating
	if err := dn.updateFiles(oldConfig, newConfig); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.updateFiles(newConfig, oldConfig); err != nil {
				retErr = errors.Wrapf(retErr, "error rolling back files writes %v", err)
				return
			}
		}
	}()

	if err := dn.updateSSHKeys(newConfig.Spec.Config.Passwd.Users); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.updateSSHKeys(oldConfig.Spec.Config.Passwd.Users); err != nil {
				retErr = errors.Wrapf(retErr, "error rolling back SSH keys updates %v", err)
				return
			}
		}
	}()

	return dn.updateOSAndReboot(newConfig)
}

// reconcilable checks the configs to make sure that the only changes requested
// are ones we know how to do in-place.  If we can reconcile, (nil, nil) is returned.
// Otherwise, if we can't do it in place, the node is marked as degraded;
// the returned string value includes the rationale.
//
// we can only update machine configs that have changes to the files,
// directories, links, and systemd units sections of the included ignition
// config currently.

func (dn *Daemon) reconcilable(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	glog.Info("Checking if configs are reconcilable")
	// We skip out of reconcilable if there is no Kind and we are in runOnce mode. The
	// reason is that there is a good chance a previous state is not available to match against.
	if oldConfig.Kind == "" && dn.onceFrom != "" {
		glog.Info("Missing kind in old config. Assuming no prior state.")
		return nil
	}
	oldIgn := oldConfig.Spec.Config
	newIgn := newConfig.Spec.Config

	// Ignition section
	// First check if this is a generally valid Ignition Config
	rpt := validate.ValidateWithoutSource(reflect.ValueOf(newIgn))
	if rpt.IsFatal() {
		return errors.Errorf("invalid Ignition config found: %v", rpt)
	}

	// if the config versions are different, all bets are off. this probably
	// shouldn't happen, but if it does, we can't deal with it.
	if oldIgn.Ignition.Version != newIgn.Ignition.Version {
		return fmt.Errorf("ignition version mismatch between old and new config: old: %s new: %s",
			oldIgn.Ignition.Version, newIgn.Ignition.Version)
	}
	// everything else in the ignition section doesn't matter to us, since the
	// rest of the stuff in this section has to do with fetching remote
	// resources, and the mcc should've fully rendered those out before the
	// config gets here.

	// Networkd section

	// we don't currently configure the network in place. we can't fix it if
	// something changed here.
	if !reflect.DeepEqual(oldIgn.Networkd, newIgn.Networkd) {
		return errors.New("ignition networkd section contains changes")
	}

	// Passwd section

	// we don't currently configure Groups in place. we don't configure Users except
	// for setting/updating SSHAuthorizedKeys for the only allowed user "core".
	// otherwise we can't fix it if something changed here.
	if !reflect.DeepEqual(oldIgn.Passwd, newIgn.Passwd) {
		if !reflect.DeepEqual(oldIgn.Passwd.Groups, newIgn.Passwd.Groups) {
			return errors.New("ignition Passwd Groups section contains changes")
		}
		if !reflect.DeepEqual(oldIgn.Passwd.Users, newIgn.Passwd.Users) {
			// check if the prior config is empty and that this is the first time running.
			// if so, the SSHKey from the cluster config and user "core" must be added to machine config.
			if len(oldIgn.Passwd.Users) >= 0 && len(newIgn.Passwd.Users) >= 1 {
				// there is an update to Users, we must verify that it is ONLY making an acceptable
				// change to the SSHAuthorizedKeys for the user "core"
				for _, user := range newIgn.Passwd.Users {
					if user.Name != coreUserName {
						return errors.New("ignition passwd user section contains unsupported changes: non-core user")
					}
				}
				glog.Infof("user data to be verified before ssh update: %v", newIgn.Passwd.Users[len(newIgn.Passwd.Users)-1])
				if err := verifyUserFields(newIgn.Passwd.Users[len(newIgn.Passwd.Users)-1]); err != nil {
					return err
				}
			}
		}
	}

	// Storage section

	// we can only reconcile files right now. make sure the sections we can't
	// fix aren't changed.
	if !reflect.DeepEqual(oldIgn.Storage.Disks, newIgn.Storage.Disks) {
		return errors.New("ignition disks section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Filesystems, newIgn.Storage.Filesystems) {
		return errors.New("ignition filesystems section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Raid, newIgn.Storage.Raid) {
		return errors.New("ignition raid section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Directories, newIgn.Storage.Directories) {
		return errors.New("ignition directories section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Links, newIgn.Storage.Links) {
		// This means links have been added, as opposed as being removed as it happened with
		// https://bugzilla.redhat.com/show_bug.cgi?id=1677198. This doesn't really change behavior
		// since we still don't support links but we allow old MC to remove links when upgrading.
		if len(newIgn.Storage.Links) != 0 {
			return errors.New("ignition links section contains changes")
		}
	}

	// Special case files append: if the new config wants us to append, then we
	// have to force a reprovision since it's not idempotent
	for _, f := range newIgn.Storage.Files {
		if f.Append {
			return fmt.Errorf("ignition file %v includes append", f.Path)
		}
	}

	// Systemd section

	// we can reconcile any state changes in the systemd section.

	// we made it through all the checks. reconcile away!
	glog.V(2).Info("Configs are reconcilable")
	return nil
}

// verifyUserFields returns nil if the user Name = "core", if 1 or more SSHKeys exist for
// this user and if all other fields in User are empty.
// Otherwise, an error will be returned and the proposed config will not be reconcilable.
// At this time we do not support non-"core" users or any changes to the "core" user
// outside of SSHAuthorizedKeys.
func verifyUserFields(pwdUser ignv2_2types.PasswdUser) error {
	emptyUser := ignv2_2types.PasswdUser{}
	tempUser := pwdUser
	if tempUser.Name == coreUserName && len(tempUser.SSHAuthorizedKeys) >= 1 {
		tempUser.Name = ""
		tempUser.SSHAuthorizedKeys = nil
		if !reflect.DeepEqual(emptyUser, tempUser) {
			return errors.New("ignition passwd user section contains unsupported changes: non-sshKey changes")
		}
		glog.Info("SSH Keys reconcilable")
	} else {
		return errors.New("ignition passwd user section contains unsupported changes: user must be core and have 1 or more sshKeys")
	}
	return nil
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
	if err := dn.deleteStaleData(oldConfig, newConfig); err != nil {
		return err
	}
	return nil
}

// deleteStaleData performs a diff of the new and the old config. It then deletes
// all the files, units that are present in the old config but not in the new one.
// this function will error out if it fails to delete a file (with the exception
// of simply warning if the error is ENOENT since that's the desired state).
func (dn *Daemon) deleteStaleData(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	glog.Info("Deleting stale data")
	newFileSet := make(map[string]struct{})
	for _, f := range newConfig.Spec.Config.Storage.Files {
		newFileSet[f.Path] = struct{}{}
	}

	for _, f := range oldConfig.Spec.Config.Storage.Files {
		if _, ok := newFileSet[f.Path]; !ok {
			glog.V(2).Infof("Deleting stale config file: %s", f.Path)
			if err := os.Remove(f.Path); err != nil {
				newErr := fmt.Errorf("unable to delete %s: %s", f.Path, err)
				if !os.IsNotExist(err) {
					return newErr
				}
				// otherwise, just warn
				glog.Warningf("%v", newErr)
			}
			glog.Infof("Removed stale file %q", f.Path)
		}
	}

	newUnitSet := make(map[string]struct{})
	newDropinSet := make(map[string]struct{})
	for _, u := range newConfig.Spec.Config.Systemd.Units {
		for j := range u.Dropins {
			path := filepath.Join(pathSystemd, u.Name+".d", u.Dropins[j].Name)
			newDropinSet[path] = struct{}{}
		}
		path := filepath.Join(pathSystemd, u.Name)
		newUnitSet[path] = struct{}{}
	}

	for _, u := range oldConfig.Spec.Config.Systemd.Units {
		for j := range u.Dropins {
			path := filepath.Join(pathSystemd, u.Name+".d", u.Dropins[j].Name)
			if _, ok := newDropinSet[path]; !ok {
				glog.V(2).Infof("Deleting stale systemd dropin file: %s", path)
				if err := os.Remove(path); err != nil {
					newErr := fmt.Errorf("unable to delete %s: %s", path, err)
					if !os.IsNotExist(err) {
						return newErr
					}
					// otherwise, just warn
					glog.Warningf("%v", newErr)
				}
				glog.Infof("Removed stale systemd dropin %q", path)
			}
		}
		path := filepath.Join(pathSystemd, u.Name)
		if _, ok := newUnitSet[path]; !ok {
			if err := dn.disableUnit(u); err != nil {
				glog.Warningf("Unable to disable %s: %s", u.Name, err)
			}
			glog.V(2).Infof("Deleting stale systemd unit file: %s", path)
			if err := os.Remove(path); err != nil {
				newErr := fmt.Errorf("unable to delete %s: %s", path, err)
				if !os.IsNotExist(err) {
					return newErr
				}
				// otherwise, just warn
				glog.Warningf("%v", newErr)
			}
			glog.Infof("Removed stale systemd unit %q", path)
		}
	}

	return nil
}

// enableUnit enables a systemd unit via symlink
func (dn *Daemon) enableUnit(unit ignv2_2types.Unit) error {
	// The link location
	wantsPath := filepath.Join(wantsPathSystemd, unit.Name)
	// sanity check that we don't return an error when the link already exists
	if _, err := os.Stat(wantsPath); err == nil {
		glog.Infof("%s already exists. Not making a new symlink", wantsPath)
		return nil
	}
	// The originating file to link
	servicePath := filepath.Join(pathSystemd, unit.Name)
	err := renameio.Symlink(servicePath, wantsPath)
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
	if _, err := os.Stat(wantsPath); err != nil {
		glog.Infof("%s was not present. No need to remove", wantsPath)
		return nil
	}
	glog.V(2).Infof("Disabling unit at %s", wantsPath)

	return os.Remove(wantsPath)
}

// writeUnits writes the systemd units to disk
func (dn *Daemon) writeUnits(units []ignv2_2types.Unit) error {
	for _, u := range units {
		// write the dropin to disk
		for i := range u.Dropins {
			glog.Infof("Writing systemd unit dropin %q", u.Dropins[i].Name)
			dpath := filepath.Join(pathSystemd, u.Name+".d", u.Dropins[i].Name)
			if err := writeFileAtomicallyWithDefaults(dpath, []byte(u.Dropins[i].Contents)); err != nil {
				return fmt.Errorf("failed to write systemd unit dropin %q: %v", u.Dropins[i].Name, err)
			}

			glog.V(2).Infof("Wrote systemd unit dropin at %s", dpath)
		}

		fpath := filepath.Join(pathSystemd, u.Name)

		// check if the unit is masked. if it is, we write a symlink to
		// /dev/null and continue
		if u.Mask {
			glog.V(2).Info("Systemd unit masked")
			if err := os.RemoveAll(fpath); err != nil {
				return fmt.Errorf("failed to remove unit %q: %v", u.Name, err)
			}
			glog.V(2).Infof("Removed unit %q", u.Name)

			if err := renameio.Symlink(pathDevNull, fpath); err != nil {
				return fmt.Errorf("failed to symlink unit %q to %s: %v", u.Name, pathDevNull, err)
			}
			glog.V(2).Infof("Created symlink unit %q to %s", u.Name, pathDevNull)

			continue
		}

		if u.Contents != "" {
			glog.Infof("Writing systemd unit %q", u.Name)

			// write the unit to disk
			if err := writeFileAtomicallyWithDefaults(fpath, []byte(u.Contents)); err != nil {
				return fmt.Errorf("failed to write systemd unit %q: %v", u.Name, err)
			}

			glog.V(2).Infof("Successfully wrote systemd unit %q: ", u.Name)
		}

		// if the unit doesn't note if it should be enabled or disabled then
		// skip all linking.
		// if the unit should be enabled, then enable it.
		// otherwise the unit is disabled. run disableUnit to ensure the unit is
		// disabled. even if the unit wasn't previously enabled the result will
		// be fine as disableUnit is idempotent.
		// Note: we have to check for legacy unit.Enable and honor it
		glog.Infof("Enabling systemd unit %q", u.Name)
		if u.Enable {
			if err := dn.enableUnit(u); err != nil {
				return err
			}
			glog.V(2).Infof("Enabled systemd unit %q", u.Name)
		}
		if u.Enabled != nil {
			if *u.Enabled {
				if err := dn.enableUnit(u); err != nil {
					return err
				}
				glog.V(2).Infof("Enabled systemd unit %q", u.Name)
			} else {
				if err := dn.disableUnit(u); err != nil {
					return err
				}
				glog.V(2).Infof("Disabled systemd unit %q", u.Name)
			}
		}
	}
	return nil
}

// writeFiles writes the given files to disk.
// it doesn't fetch remote files and expects a flattened config file.
func (dn *Daemon) writeFiles(files []ignv2_2types.File) error {
	for _, file := range files {
		glog.Infof("Writing file %q", file.Path)

		contents, err := dataurl.DecodeString(file.Contents.Source)
		if err != nil {
			return err
		}
		mode := defaultFilePermissions
		if file.Mode != nil {
			mode = os.FileMode(*file.Mode)
		}
		var (
			uid, gid = -1, -1
		)
		// set chown if file information is provided
		if file.User != nil || file.Group != nil {
			uid, gid, err = getFileOwnership(file)
			if err != nil {
				return fmt.Errorf("failed to retrieve file ownership for file %q: %v", file.Path, err)
			}
		}
		if err := writeFileAtomically(file.Path, contents.Data, defaultDirectoryPermissions, mode, uid, gid); err != nil {
			return err
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
				return uid, gid, fmt.Errorf("failed to retrieve UserID for username: %s", file.User.Name)
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
				return uid, gid, fmt.Errorf("failed to retrieve GroupID for group: %s", file.Group.Name)
			}
			glog.V(2).Infof("Retrieved GroupID: %s for group: %s", osGroup.Gid, file.Group.Name)
			gid, _ = strconv.Atoi(osGroup.Gid)
		}
	}
	return uid, gid, nil
}

func (dn *Daemon) atomicallyWriteSSHKey(newUser ignv2_2types.PasswdUser, keys string) error {
	authKeyPath := filepath.Join(coreUserSSHPath, "authorized_keys")

	// Keys should only be written to "/home/core/.ssh"
	// Once Users are supported fully this should be writing to PasswdUser.HomeDir
	glog.Infof("Writing SSHKeys at %q", authKeyPath)

	if err := writeFileAtomicallyWithDefaults(authKeyPath, []byte(keys)); err != nil {
		return err
	}

	glog.V(2).Infof("Wrote SSHKeys at %s", authKeyPath)

	return nil
}

// Update a given PasswdUser's SSHKey
func (dn *Daemon) updateSSHKeys(newUsers []ignv2_2types.PasswdUser) error {
	if len(newUsers) == 0 {
		return nil
	}

	// we're also appending all keys for any user to core, so for now
	// we pass this to atomicallyWriteSSHKeys to write.
	var concatSSHKeys string
	for _, k := range newUsers[len(newUsers)-1].SSHAuthorizedKeys {
		concatSSHKeys = concatSSHKeys + string(k) + "\n"
	}
	// newUsers[0] is currently unused since we write keys only for the core user
	if err := dn.atomicSSHKeysWriter(newUsers[0], concatSSHKeys); err != nil {
		return err
	}
	return nil
}

// updateOS updates the system OS to the one specified in newConfig
func (dn *Daemon) updateOS(config *mcfgv1.MachineConfig) error {
	if dn.OperatingSystem != machineConfigDaemonOSRHCOS {
		glog.V(2).Info("Updating of non RHCOS nodes are not supported")
		return nil
	}

	newURL := config.Spec.OSImageURL
	osMatch, err := compareOSImageURL(dn.bootedOSImageURL, newURL)
	if err != nil {
		return err
	}
	if osMatch {
		return nil
	}
	if dn.recorder != nil {
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "InClusterUpgrade", fmt.Sprintf("In cluster upgrade to %s", newURL))
	}

	glog.Infof("Updating OS to %s", newURL)
	if err := dn.NodeUpdaterClient.RunPivot(newURL); err != nil {
		return fmt.Errorf("failed to run pivot: %v", err)
	}

	return nil
}

// Log a message to the systemd journal as well as our stdout
func (dn *Daemon) logSystem(format string, a ...interface{}) {
	message := fmt.Sprintf(format, a...)
	glog.Info(message)
	// Since we're chrooted into the host rootfs with /run mounted,
	// we can just talk to the journald socket.  Doing this as a
	// subprocess rather than talking to journald in process since
	// I worry about the golang library having a connection pre-chroot.
	logger := exec.Command("logger")
	stdin, err := logger.StdinPipe()
	if err != nil {
		glog.Errorf("failed to get stdin pipe: %v", err)
		return
	}

	go func() {
		defer stdin.Close()
		io.WriteString(stdin, "machine-config-daemon: ")
		io.WriteString(stdin, message)
	}()
	err = logger.Run()
	if err != nil {
		glog.Errorf("failed to invoke logger: %v", err)
		return
	}
}

func (dn *Daemon) cancelSIGTERM() {
	if dn.installedSigterm {
		signal.Reset(syscall.SIGTERM)
		dn.installedSigterm = false
	}
}

// reboot is the final step. it tells systemd-logind to reboot the machine,
// cleans up the agent's connections, and then sleeps for 7 days. if it wakes up
// and manages to return, it returns a scary error message.
func (dn *Daemon) reboot(rationale string, timeout time.Duration, rebootCmd *exec.Cmd) error {
	// We'll only have a recorder if we're cluster driven
	if dn.recorder != nil {
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "Reboot", rationale)
	}
	dn.logSystem("initiating reboot: %s", rationale)

	// Now that everything is done, avoid delaying shutdown.
	dn.cancelSIGTERM()

	dn.Close()

	if dn.skipReboot && dn.onceFrom != "" { // the dn.onceFrom check is just a triple check that we're not messing with in-cluster MCDs
		glog.Info("MCD is not rebooting in onceFrom with --skip-reboot")
		return nil
	}

	// reboot, executed async via systemd-run so that the reboot command is executed
	// in the context of the host asynchronously from us
	err := rebootCmd.Run()
	if err != nil {
		return errors.Wrapf(err, "failed to reboot")
	}

	// wait to be killed via SIGTERM from the kubelet shutting down
	time.Sleep(timeout)

	// if everything went well, this should be unreachable.
	return fmt.Errorf("reboot failed; this error should be unreachable, something is seriously wrong")
}
