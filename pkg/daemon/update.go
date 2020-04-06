package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	systemdDbus "github.com/coreos/go-systemd/dbus"
	ign "github.com/coreos/ignition/config/v2_2"
	igntypes "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	"github.com/google/renameio"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	pivotutils "github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
	errors "github.com/pkg/errors"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubectl/pkg/drain"
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
	// fipsFile is the file to check if FIPS is enabled
	fipsFile = "/proc/sys/crypto/fips_enabled"
)

// HostInfo contains information of an OSTree based system
type HostInfo struct {
	Deployments []Deployment `json:"deployments"`
}

// Deployment contains information about a particular OSTree deployment
type Deployment struct {
	RequestedLocalPkgs []string `json:"requested-local-packages"`
}

func installedRTKernelRpmsOnHost() ([]string, error) {
	var out []byte
	var err error
	var rtKernelRpms = []string{}
	if out, err = exec.Command("rpm-ostree", "status", "--json").Output(); err != nil {
		return rtKernelRpms, fmt.Errorf("Failed to execute rpm-ostre status --json %v", err)
	}

	var rpms HostInfo
	if err := json.Unmarshal(out, &rpms); err != nil {
		return rtKernelRpms, err
	}

	rtRegex := regexp.MustCompile("kernel-rt-.*")
	for _, localPkg := range rpms.Deployments[0].RequestedLocalPkgs {
		if rtRegex.MatchString(localPkg) {
			rtKernelRpms = append(rtKernelRpms, localPkg)
		}
	}
	return rtKernelRpms, nil
}

func writeFileAtomicallyWithDefaults(fpath string, b []byte) error {
	return writeFileAtomically(fpath, b, defaultDirectoryPermissions, defaultFilePermissions, -1, -1)
}

// writeFileAtomically uses the renameio package to provide atomic file writing, we can't use renameio.WriteFile
// directly since we need to 1) Chown 2) go through a buffer since files provided can be big
func writeFileAtomically(fpath string, b []byte, dirMode, fileMode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(fpath)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("failed to create directory %q: %v", filepath.Dir(fpath), err)
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

func getNodeRef(node *corev1.Node) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		Kind: "Node",
		Name: node.GetName(),
		UID:  node.GetUID(),
	}
}

func (dn *Daemon) drainAndReboot(newConfig *mcfgv1.MachineConfig) error {
	if err := dn.drain(); err != nil {
		return err
	}
	return dn.finalizeAndReboot(newConfig)
}

// updateOSAndReboot is the last step in an update(), and it can also
// be called as a special case for the "bootstrap pivot".
func (dn *Daemon) updateOSAndReboot(newConfig *mcfgv1.MachineConfig) (retErr error) {
	if err := dn.updateOS(newConfig); err != nil {
		return err
	}
	return dn.finalizeAndReboot(newConfig)
}

func (dn *Daemon) finalizeAndReboot(newConfig *mcfgv1.MachineConfig) (retErr error) {
	if out, err := dn.storePendingState(newConfig, 1); err != nil {
		return errors.Wrapf(err, "failed to log pending config: %s", string(out))
	}
	defer func() {
		if retErr != nil {
			if dn.recorder != nil {
				dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "PendingConfigRollBack", fmt.Sprintf("Rolling back pending config %s: %v", newConfig.GetName(), retErr))
			}
			if out, err := dn.storePendingState(newConfig, 0); err != nil {
				retErr = errors.Wrapf(retErr, "error rolling back pending config %v: %s", err, string(out))
				return
			}
		}
	}()
	if dn.recorder != nil {
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "PendingConfig", fmt.Sprintf("Written pending config %s", newConfig.GetName()))
	}

	// reboot. this function shouldn't actually return.
	return dn.reboot(fmt.Sprintf("Node will reboot into config %v", newConfig.GetName()))
}

func (dn *Daemon) drain() error {
	// Skip draining of the node when we're not cluster driven
	if dn.kubeClient == nil {
		return nil
	}

	dn.logSystem("Update prepared; beginning drain")
	startTime := time.Now()

	dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "Drain", "Draining node to update config.")

	backoff := wait.Backoff{
		Steps:    5,
		Duration: 10 * time.Second,
		Factor:   2,
	}
	var lastErr error
	if err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		err := drain.RunCordonOrUncordon(dn.drainer, dn.node, true)
		if err != nil {
			lastErr = err
			glog.Infof("Cordon failed with: %v, retrying", err)
			return false, nil
		}
		err = drain.RunNodeDrain(dn.drainer, dn.node.Name)
		if err == nil {
			return true, nil
		}
		lastErr = err
		glog.Infof("Draining failed with: %v, retrying", err)
		return false, nil
	}); err != nil {
		failTime := fmt.Sprintf("%v sec", time.Since(startTime).Seconds())
		if err == wait.ErrWaitTimeout {
			failMsg := fmt.Sprintf("%d tries: %v", backoff.Steps, lastErr)
			MCDDrainErr.WithLabelValues(failTime, failMsg).SetToCurrentTime()
			dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeWarning, "FailedToDrain", failMsg)
			return errors.Wrapf(lastErr, "failed to drain node (%d tries): %v", backoff.Steps, err)
		}
		MCDDrainErr.WithLabelValues(failTime, err.Error()).SetToCurrentTime()
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeWarning, "FailedToDrain", err.Error())
		return errors.Wrap(err, "failed to drain node")
	}

	dn.logSystem("drain complete")
	t := time.Since(startTime).Seconds()
	glog.Infof("Successful drain took %v seconds", t)
	successTime := fmt.Sprintf("%v sec", t)
	MCDDrainErr.WithLabelValues(successTime, "").Set(0)

	return nil
}

var errUnreconcilable = errors.New("unreconcilable")

func canonicalizeEmptyMC(config *mcfgv1.MachineConfig) *mcfgv1.MachineConfig {
	if config != nil {
		return config
	}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	rawNewIgnCfg, err := json.Marshal(newIgnCfg)
	if err != nil {
		// This should never happen
		panic(err)
	}
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "mco-empty-mc"},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: rawNewIgnCfg,
			},
		},
	}
}

// Returns true if updated packages are available
func rtKernelUpdateAvailable(rpms []os.FileInfo, rtKernelPkg []string) (bool, error) {
	for _, pkg := range rtKernelPkg {
		var out []byte
		var err error
		found := false

		if out, err = exec.Command("rpm", "-q", pkg).Output(); err != nil {
			wrappedErr := fmt.Errorf("Failed to run rpm -q : %v", err)
			return false, wrappedErr
		}
		searchRpm := strings.TrimSpace(string(out)) + ".rpm"
		for _, rpm := range rpms {
			if rpm.Name() == searchRpm {
				found = true
				break
			}
		}
		if !found {
			return true, nil
		}
	}

	return false, nil
}

// return true if the MachineConfigDiff is not empty
func (dn *Daemon) compareMachineConfig(oldConfig, newConfig *mcfgv1.MachineConfig) (bool, error) {
	oldConfig = canonicalizeEmptyMC(oldConfig)
	oldConfigName := oldConfig.GetName()
	newConfigName := newConfig.GetName()
	mcDiff, err := NewMachineConfigDiff(oldConfig, newConfig)
	if err != nil {
		return true, errors.Wrapf(err, "error creating MachineConfigDiff for comparison")
	}
	if mcDiff.IsEmpty() {
		glog.Infof("No changes from %s to %s", oldConfigName, newConfigName)
		return false, nil
	}
	return true, nil
}

// update the node to the provided node configuration.
func (dn *Daemon) update(oldConfig, newConfig *mcfgv1.MachineConfig) (retErr error) {
	oldConfig = canonicalizeEmptyMC(oldConfig)

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

	glog.Infof("Checking Reconcilable for config %v to %v", oldConfigName, newConfigName)

	// make sure we can actually reconcile this state
	diff, reconcilableError := Reconcilable(oldConfig, newConfig)

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
		return errors.Wrapf(errUnreconcilable, "%v", wrappedErr)
	}

	oldIgnConfig, report, err := ign.Parse(oldConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing old Ignition config failed with error: %v\nReport: %v", err, report)
	}
	newIgnConfig, report, err := ign.Parse(newConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing new Ignition config failed with error: %v\nReport: %v", err, report)
	}

	dn.logSystem("Starting update from %s to %s: %+v", oldConfigName, newConfigName, diff)
	rebootRequired := diff.osUpdate || diff.kargs || diff.fips || diff.kernelType

	systemdConnection, dbusConnErr := systemdDbus.NewSystemConnection()
	if dbusConnErr == nil {
		defer systemdConnection.Close()
	} else {
		glog.Warningf("Unable to establish systemd dbus connection: %s", dbusConnErr)
		// No more actions needed here as a systemd connection is not always
		// required (only if there is systemd related post update action
		// present). If a connection should be required, getPostUpdateActions
		// function will return error if nil connection is provided and then
		// rebootRequired will be se to true
	}

	postUpdateActions, err := getPostUpdateActions(
		getFilesChanges(oldIgnConfig.Storage.Files, newIgnConfig.Storage.Files),
		getUnitsChanges(oldIgnConfig.Systemd.Units, newIgnConfig.Systemd.Units),
		systemdConnection,
	)
	if err != nil {
		rebootRequired = true
	}

	drainRequired := rebootRequired || isDrainRequired(postUpdateActions)

	if drainRequired {
		if err := dn.drain(); err != nil {
			return err
		}
	} else {
		glog.Info("Draining node skipped as it is not required")
	}

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

	if err := dn.updateSSHKeys(newIgnConfig.Passwd.Users); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.updateSSHKeys(oldIgnConfig.Passwd.Users); err != nil {
				retErr = errors.Wrapf(retErr, "error rolling back SSH keys updates %v", err)
				return
			}
		}
	}()

	if err := dn.storeCurrentConfigOnDisk(newConfig); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			if err := dn.storeCurrentConfigOnDisk(oldConfig); err != nil {
				retErr = errors.Wrapf(retErr, "error rolling back current config on disk %v", err)
				return
			}
		}
	}()

	// kargs
	if err := dn.updateKernelArguments(oldConfig, newConfig); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			if err := dn.updateKernelArguments(newConfig, oldConfig); err != nil {
				retErr = errors.Wrapf(retErr, "error rolling back kernel arguments %v", err)
				return
			}
		}
	}()

	// Switch to real time kernel
	if err := dn.switchKernel(oldConfig, newConfig); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.switchKernel(newConfig, oldConfig); err != nil {
				retErr = errors.Wrapf(retErr, "error rolling back Real time Kernel %v", err)
				return
			}
		}
	}()

	if err := dn.updateOS(newConfig); err != nil {
		return err
	}
	if rebootRequired || runPostUpdateActions(postUpdateActions) {
		return dn.drainAndReboot(newConfig)
	}
	glog.Info("Reboot skipped as it is not required")
	if err := dn.nodeWriter.SetDone(
		dn.kubeClient.CoreV1().Nodes(),
		dn.nodeLister,
		dn.name,
		newConfigName,
	); err != nil {
		glog.Errorf("Setting node's state to Done failed, node will reboot: %v", err)
		return dn.drainAndReboot(newConfig)
	}
	if drainRequired {
		glog.Infof("Starting uncordoning node %v", dn.node.GetName())
		if err := drain.RunCordonOrUncordon(dn.drainer, dn.node, false); err != nil {
			glog.Errorf("Uncordoning node failed, node will reboot: %v", err)
			return dn.finalizeAndReboot(newConfig)
		}
	}
	glog.Infof("In desired config %s", newConfigName)
	MCDUpdateState.WithLabelValues(newConfigName, "").SetToCurrentTime()
	return nil
}

// MachineConfigDiff represents an ad-hoc difference between two MachineConfig objects.
// At some point this may change into holding just the files/units that changed
// and the MCO would just operate on that.  For now we're just doing this to get
// improved logging.
type MachineConfigDiff struct {
	osUpdate   bool
	kargs      bool
	fips       bool
	passwd     bool
	files      bool
	units      bool
	kernelType bool
}

// canonicalizeKernelType returns a valid kernelType. We consider empty("") and default kernelType as same
func canonicalizeKernelType(kernelType string) string {
	if kernelType == ctrlcommon.KernelTypeRealtime {
		return ctrlcommon.KernelTypeRealtime
	}
	return ctrlcommon.KernelTypeDefault
}

// NewMachineConfigDiff compares two MachineConfig objects.
func NewMachineConfigDiff(oldConfig, newConfig *mcfgv1.MachineConfig) (*MachineConfigDiff, error) {
	oldIgn, report, err := ign.Parse(oldConfig.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing old Ignition config failed with error: %v\nReport: %v", err, report)
	}
	newIgn, report, err := ign.Parse(newConfig.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing new Ignition config failed with error: %v\nReport: %v", err, report)
	}

	// Both nil and empty slices are of zero length,
	// consider them as equal while comparing KernelArguments in both MachineConfigs
	kargsEmpty := len(oldConfig.Spec.KernelArguments) == 0 && len(newConfig.Spec.KernelArguments) == 0

	return &MachineConfigDiff{
		osUpdate:   oldConfig.Spec.OSImageURL != newConfig.Spec.OSImageURL,
		kargs:      !(kargsEmpty || reflect.DeepEqual(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments)),
		fips:       oldConfig.Spec.FIPS != newConfig.Spec.FIPS,
		passwd:     !reflect.DeepEqual(oldIgn.Passwd, newIgn.Passwd),
		files:      !reflect.DeepEqual(oldIgn.Storage.Files, newIgn.Storage.Files),
		units:      !reflect.DeepEqual(oldIgn.Systemd.Units, newIgn.Systemd.Units),
		kernelType: canonicalizeKernelType(oldConfig.Spec.KernelType) != canonicalizeKernelType(newConfig.Spec.KernelType),
	}, nil
}

// IsEmpty returns true if the MachineConfigDiff has no changes, or
// in other words if the two MachineConfig objects are equivalent from
// the MCD's point of view.  This is mainly relevant if e.g. two MC
// objects happen to have different Ignition versions but are otherwise
// the same.  (Probably a better way would be to canonicalize)
func (d *MachineConfigDiff) IsEmpty() bool {
	emptyDiff := MachineConfigDiff{}
	return reflect.DeepEqual(d, &emptyDiff)
}

// Reconcilable checks the configs to make sure that the only changes requested
// are ones we know how to do in-place.  If we can reconcile, (nil, nil) is returned.
// Otherwise, if we can't do it in place, the node is marked as degraded;
// the returned string value includes the rationale.
//
// we can only update machine configs that have changes to the files,
// directories, links, and systemd units sections of the included ignition
// config currently.
func Reconcilable(oldConfig, newConfig *mcfgv1.MachineConfig) (*MachineConfigDiff, error) {
	// The parser will try to translate versions less than maxVersion to maxVersion, or output an err.
	// The ignition output in case of success will always have maxVersion
	oldIgn, report, err := ign.Parse(oldConfig.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing old Ignition config failed with error: %v\nReport: %v", err, report)
	}
	newIgn, report, err := ign.Parse(newConfig.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing new Ignition config failed with error: %v\nReport: %v", err, report)
	}

	// Check if this is a generally valid Ignition Config
	if err := ctrlcommon.ValidateIgnition(newIgn); err != nil {
		return nil, err
	}

	// Networkd section

	// we don't currently configure the network in place. we can't fix it if
	// something changed here.
	if !reflect.DeepEqual(oldIgn.Networkd, newIgn.Networkd) {
		return nil, errors.New("ignition networkd section contains changes")
	}

	// Passwd section

	// we don't currently configure Groups in place. we don't configure Users except
	// for setting/updating SSHAuthorizedKeys for the only allowed user "core".
	// otherwise we can't fix it if something changed here.
	passwdChanged := !reflect.DeepEqual(oldIgn.Passwd, newIgn.Passwd)
	if passwdChanged {
		if !reflect.DeepEqual(oldIgn.Passwd.Groups, newIgn.Passwd.Groups) {
			return nil, errors.New("ignition Passwd Groups section contains changes")
		}
		if !reflect.DeepEqual(oldIgn.Passwd.Users, newIgn.Passwd.Users) {
			// check if the prior config is empty and that this is the first time running.
			// if so, the SSHKey from the cluster config and user "core" must be added to machine config.
			if len(oldIgn.Passwd.Users) > 0 && len(newIgn.Passwd.Users) >= 1 {
				// there is an update to Users, we must verify that it is ONLY making an acceptable
				// change to the SSHAuthorizedKeys for the user "core"
				for _, user := range newIgn.Passwd.Users {
					if user.Name != coreUserName {
						return nil, errors.New("ignition passwd user section contains unsupported changes: non-core user")
					}
				}
				glog.Infof("user data to be verified before ssh update: %v", newIgn.Passwd.Users[len(newIgn.Passwd.Users)-1])
				if err := verifyUserFields(newIgn.Passwd.Users[len(newIgn.Passwd.Users)-1]); err != nil {
					return nil, err
				}
			}
		}
	}

	// Storage section

	// we can only reconcile files right now. make sure the sections we can't
	// fix aren't changed.
	if !reflect.DeepEqual(oldIgn.Storage.Disks, newIgn.Storage.Disks) {
		return nil, errors.New("ignition disks section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Filesystems, newIgn.Storage.Filesystems) {
		return nil, errors.New("ignition filesystems section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Raid, newIgn.Storage.Raid) {
		return nil, errors.New("ignition raid section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Directories, newIgn.Storage.Directories) {
		return nil, errors.New("ignition directories section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Links, newIgn.Storage.Links) {
		// This means links have been added, as opposed as being removed as it happened with
		// https://bugzilla.redhat.com/show_bug.cgi?id=1677198. This doesn't really change behavior
		// since we still don't support links but we allow old MC to remove links when upgrading.
		if len(newIgn.Storage.Links) != 0 {
			return nil, errors.New("ignition links section contains changes")
		}
	}

	// Special case files append: if the new config wants us to append, then we
	// have to force a reprovision since it's not idempotent
	for _, f := range newIgn.Storage.Files {
		if f.Append {
			return nil, fmt.Errorf("ignition file %v includes append", f.Path)
		}
	}

	// Systemd section

	// we can reconcile any state changes in the systemd section.

	// FIPS section
	// We do not allow update to FIPS for a running cluster, so any changes here will be an error
	if err := checkFIPS(oldConfig, newConfig); err != nil {
		return nil, err
	}

	// we made it through all the checks. reconcile away!
	glog.V(2).Info("Configs are reconcilable")
	mcDiff, err := NewMachineConfigDiff(oldConfig, newConfig)
	if err != nil {
		return nil, errors.Wrapf(err, "error creating MachineConfigDiff")
	}
	return mcDiff, nil
}

// verifyUserFields returns nil if the user Name = "core", if 1 or more SSHKeys exist for
// this user and if all other fields in User are empty.
// Otherwise, an error will be returned and the proposed config will not be reconcilable.
// At this time we do not support non-"core" users or any changes to the "core" user
// outside of SSHAuthorizedKeys.
func verifyUserFields(pwdUser igntypes.PasswdUser) error {
	emptyUser := igntypes.PasswdUser{}
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

// checkFIPS verifies the state of FIPS on the system before an update.
// Our new thought around this is that really FIPS should be a "day 1"
// operation, and we don't want to make it editable after the fact.
// See also https://github.com/openshift/installer/pull/2594
// Anyone who wants to force this can change the MC flag, then
// `oc debug node` and run the disable command by hand, then reboot.
// If we detect that FIPS has been changed, we reject the update.
func checkFIPS(current, desired *mcfgv1.MachineConfig) error {
	content, err := ioutil.ReadFile(fipsFile)
	if err != nil {
		if os.IsNotExist(err) {
			// we just exit cleanly if we're not even on linux
			glog.Infof("no %s on this system, skipping FIPS check", fipsFile)
			return nil
		}
		return errors.Wrapf(err, "Error reading FIPS file at %s: %s", fipsFile, string(content))
	}
	nodeFIPS, err := strconv.ParseBool(strings.TrimSuffix(string(content), "\n"))
	if err != nil {
		return errors.Wrapf(err, "Error parsing FIPS file at %s", fipsFile)
	}
	if desired.Spec.FIPS == nodeFIPS {
		// Check if FIPS on the system is at the desired setting
		current.Spec.FIPS = nodeFIPS
		return nil
	}
	return errors.New("detected change to FIPS flag. Refusing to modify FIPS on a running cluster")
}

// generateKargsCommand performs a diff between the old/new MC kernelArguments,
// and generates the command line arguments suitable for `rpm-ostree kargs`.
// Note what we really should be doing though is also looking at the *current*
// kernel arguments in case there was drift.  But doing that requires us knowing
// what the "base" arguments are.  See https://github.com/ostreedev/ostree/issues/479
func generateKargsCommand(oldConfig, newConfig *mcfgv1.MachineConfig) []string {
	oldKargs := make(map[string]bool)
	for _, arg := range oldConfig.Spec.KernelArguments {
		oldKargs[arg] = true
	}
	newKargs := make(map[string]bool)
	for _, arg := range newConfig.Spec.KernelArguments {
		newKargs[arg] = true
	}
	cmdArgs := []string{}
	for _, arg := range oldConfig.Spec.KernelArguments {
		if !newKargs[arg] {
			cmdArgs = append(cmdArgs, "--delete="+arg)
		}
	}
	for _, arg := range newConfig.Spec.KernelArguments {
		if !oldKargs[arg] {
			cmdArgs = append(cmdArgs, "--append="+arg)
		}
	}
	return cmdArgs
}

// updateKernelArguments adjusts the kernel args
func (dn *Daemon) updateKernelArguments(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	diff := generateKargsCommand(oldConfig, newConfig)
	if len(diff) == 0 {
		return nil
	}
	if dn.OperatingSystem != machineConfigDaemonOSRHCOS && dn.OperatingSystem != machineConfigDaemonOSFCOS {
		return fmt.Errorf("Updating kargs on non-CoreOS nodes is not supported: %v", diff)
	}

	args := append([]string{"kargs"}, diff...)
	dn.logSystem("Running rpm-ostree %v", args)
	return exec.Command("rpm-ostree", args...).Run()
}

// mountOSContainer mounts the container and returns the mountpoint
func (dn *Daemon) mountOSContainer(container string) (mnt, containerName string, err error) {
	var authArgs []string
	if _, err = os.Stat(kubeletAuthFile); err == nil {
		authArgs = append(authArgs, "--authfile", kubeletAuthFile)
	}
	// Pull the image
	args := []string{"pull", "-q"}
	args = append(args, authArgs...)
	args = append(args, container)
	pivotutils.RunExt(false, numRetriesNetCommands, "podman", args...)

	containerName = "mcd-" + string(uuid.NewUUID())
	// `podman mount` wants a container, so let's create a dummy one, but not run it
	var cidBuf []byte
	cidBuf, err = runGetOut("podman", "create", "--net=none", "--annotation=org.openshift.machineconfigoperator.pivot=true", "--name", containerName, container)
	if err != nil {
		return
	}

	cid := strings.TrimSpace(string(cidBuf))
	// Use the container ID to find its mount point
	mntBuf, err := runGetOut("podman", "mount", cid)
	if err != nil {
		return
	}
	mnt = strings.TrimSpace(string(mntBuf))
	return
}

// switchKernel updates kernel on host with the kernelType specified in MachineConfig.
// Right now it supports default (traditional) and realtime kernel
func (dn *Daemon) switchKernel(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// Do nothing if both old and new KernelType are of type default
	if canonicalizeKernelType(oldConfig.Spec.KernelType) == ctrlcommon.KernelTypeDefault && canonicalizeKernelType(newConfig.Spec.KernelType) == ctrlcommon.KernelTypeDefault {
		return nil
	}
	// We support Kernel update only on RHCOS nodes
	if dn.OperatingSystem != machineConfigDaemonOSRHCOS {
		return fmt.Errorf("Updating kernel on non-RHCOS nodes is not supported")
	}

	defaultKernel := []string{"kernel", "kernel-core", "kernel-modules", "kernel-modules-extra"}
	var args []string

	dn.logSystem("Initiating switch from kernel %s to %s", canonicalizeKernelType(oldConfig.Spec.KernelType), canonicalizeKernelType(newConfig.Spec.KernelType))

	if canonicalizeKernelType(oldConfig.Spec.KernelType) == ctrlcommon.KernelTypeRealtime && canonicalizeKernelType(newConfig.Spec.KernelType) == ctrlcommon.KernelTypeDefault {
		var installedRTKernelRpms []string
		var err error
		args = []string{"override", "reset"}
		args = append(args, defaultKernel...)
		if installedRTKernelRpms, err = installedRTKernelRpmsOnHost(); err != nil {
			return fmt.Errorf("Error while fetching installed RT kernel on host %v", err)
		}
		if len(installedRTKernelRpms) == 0 {
			return fmt.Errorf("No kernel-rt package installed on host")
		}
		for _, installedRTKernelRpm := range installedRTKernelRpms {
			args = append(args, "--uninstall", installedRTKernelRpm)
		}
		dn.logSystem("Switching to kernelType=%s, invoking rpm-ostree %+q", newConfig.Spec.KernelType, args)
		if err := exec.Command("rpm-ostree", args...).Run(); err != nil {
			return fmt.Errorf("Failed to execute rpm-ostree %+q : %v", args, err)
		}
		return nil
	}

	var mnt, containerName string
	var err error
	if mnt, containerName, err = dn.mountOSContainer(newConfig.Spec.OSImageURL); err != nil {
		return err
	}

	defer func() {
		// Delete container and remove image once we are done with using rpms available in OSContainer
		podmanRemove(containerName)
		exec.Command("podman", "rmi", newConfig.Spec.OSImageURL).Run()
		dn.logSystem("Deleted container and removed OSContainer image")
	}()

	// Get kernel-rt packages from OSContainer
	rtRegex := regexp.MustCompile("kernel-rt(.*).rpm")
	files, err := ioutil.ReadDir(mnt)
	if err != nil {
		return err
	}

	rtKernelRpms := []os.FileInfo{}
	for _, file := range files {
		if rtRegex.MatchString(file.Name()) {
			rtKernelRpms = append(rtKernelRpms, file)
		}
	}

	if len(rtKernelRpms) == 0 {
		// No kernel-rt rpm package found
		return fmt.Errorf("No kernel-rt package available in the OSContainer with URL %s", newConfig.Spec.OSImageURL)
	}

	if canonicalizeKernelType(oldConfig.Spec.KernelType) == ctrlcommon.KernelTypeDefault && canonicalizeKernelType(newConfig.Spec.KernelType) == ctrlcommon.KernelTypeRealtime {
		// Switch to RT kernel
		args = []string{"override", "remove"}
		args = append(args, defaultKernel...)
		for _, rpm := range rtKernelRpms {
			args = append(args, "--install", fmt.Sprintf("%s/%s", mnt, rpm.Name()))
		}

		dn.logSystem("Switching to kernelType=%s, invoking rpm-ostree %+q", newConfig.Spec.KernelType, args)
		if err := exec.Command("rpm-ostree", args...).Run(); err != nil {
			return fmt.Errorf("Failed to execute rpm-ostree %+q : %v", args, err)
		}
	}

	if canonicalizeKernelType(oldConfig.Spec.KernelType) == ctrlcommon.KernelTypeRealtime && canonicalizeKernelType(newConfig.Spec.KernelType) == ctrlcommon.KernelTypeRealtime {
		if oldConfig.Spec.OSImageURL != newConfig.Spec.OSImageURL {
			var installedRTKernelRpms []string
			var err error
			args = []string{"uninstall"}
			if installedRTKernelRpms, err = installedRTKernelRpmsOnHost(); err != nil {
				return fmt.Errorf("Error while fetching installed RT kernel on host %v", err)
			}
			if len(installedRTKernelRpms) == 0 {
				return fmt.Errorf("No kernel-rt package installed on host")
			}
			for _, installedRTKernelRpm := range installedRTKernelRpms {
				args = append(args, installedRTKernelRpm)
			}
			// Perform kernel-rt package update only if updated packages are available
			var updateAvailable bool
			if updateAvailable, err = rtKernelUpdateAvailable(rtKernelRpms, installedRTKernelRpms); err != nil {
				return err
			} else if !updateAvailable {
				return nil
			}
			for _, rpm := range rtKernelRpms {
				args = append(args, "--install", fmt.Sprintf("%s/%s", mnt, rpm.Name()))
			}
			dn.logSystem("Updating rt-kernel packages on host: %+q", args)
			if err := exec.Command("rpm-ostree", args...).Run(); err != nil {
				return fmt.Errorf("Failed to execute rpm-ostree %+q : %v", args, err)
			}
		}
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
	oldIgnConfig, report, err := ign.Parse(oldConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("failed to update files. Parsing old Ignition config failed with error: %v\nReport: %v", err, report)
	}
	newIgnConfig, report, err := ign.Parse(newConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("failed to update files. Parsing new Ignition config failed with error: %v\nReport: %v", err, report)
	}
	if err := dn.writeFiles(newIgnConfig.Storage.Files); err != nil {
		return err
	}
	if err := dn.writeUnits(newIgnConfig.Systemd.Units); err != nil {
		return err
	}
	if err := dn.deleteStaleData(&oldIgnConfig, &newIgnConfig); err != nil {
		return err
	}
	return nil
}

// deleteStaleData performs a diff of the new and the old Ignition config. It then deletes
// all the files, units that are present in the old config but not in the new one.
// this function will error out if it fails to delete a file (with the exception
// of simply warning if the error is ENOENT since that's the desired state).
func (dn *Daemon) deleteStaleData(oldIgnConfig, newIgnConfig *igntypes.Config) error {
	glog.Info("Deleting stale data")
	newFileSet := make(map[string]struct{})
	for _, f := range newIgnConfig.Storage.Files {
		newFileSet[f.Path] = struct{}{}
	}

	operatingSystem, err := getHostRunningOS()
	if err != nil {
		return errors.Wrapf(err, "checking operating system")
	}

	for _, f := range oldIgnConfig.Storage.Files {
		if _, ok := newFileSet[f.Path]; !ok {
			if _, err := os.Stat(noOrigFileStampName(f.Path)); err == nil {
				if err := os.Remove(noOrigFileStampName(f.Path)); err != nil {
					return errors.Wrapf(err, "deleting noorig file stamp %q: %v", noOrigFileStampName(f.Path), err)
				}
				glog.V(2).Infof("Removing file %q completely", f.Path)
			} else if _, err := os.Stat(origFileName(f.Path)); err == nil {
				// Add a check for backwards compatibility: basically if the file doesn't exist in /usr/etc (on FCOS/RHCOS)
				// and no rpm is claiming it, we assume that the orig file came from a wrongful backup of a MachineConfig
				// file instead of a file originally on disk. See https://bugzilla.redhat.com/show_bug.cgi?id=1814397
				if _, err := exec.Command("rpm", "-qf", f.Path).CombinedOutput(); err != nil {
					if err := os.Remove(origFileName(f.Path)); err != nil {
						return errors.Wrapf(err, "deleting orig file %q: %v", origFileName(f.Path), err)
					}
				} else if _, err := os.Stat("/usr" + f.Path); strings.HasPrefix(f.Path, "/etc") && os.IsNotExist(err) &&
					(operatingSystem == machineConfigDaemonOSRHCOS || operatingSystem == machineConfigDaemonOSFCOS) {
					if err := os.Remove(origFileName(f.Path)); err != nil {
						return errors.Wrapf(err, "deleting orig file %q: %v", origFileName(f.Path), err)
					}
				} else {
					if out, err := exec.Command("cp", "-a", "--reflink=auto", origFileName(f.Path), f.Path).CombinedOutput(); err != nil {
						return errors.Wrapf(err, "restoring %q from orig file %q: %s", f.Path, origFileName(f.Path), string(out))
					}
					if err := os.Remove(origFileName(f.Path)); err != nil {
						return errors.Wrapf(err, "deleting orig file %q: %v", origFileName(f.Path), err)
					}
					glog.V(2).Infof("Restored file %q", f.Path)
					continue
				}
			}
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
	for _, u := range newIgnConfig.Systemd.Units {
		for j := range u.Dropins {
			path := filepath.Join(pathSystemd, u.Name+".d", u.Dropins[j].Name)
			newDropinSet[path] = struct{}{}
		}
		path := filepath.Join(pathSystemd, u.Name)
		newUnitSet[path] = struct{}{}
	}

	for _, u := range oldIgnConfig.Systemd.Units {
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
func (dn *Daemon) enableUnit(unit igntypes.Unit) error {
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
		return err
	}
	glog.Infof("Enabled %s", unit.Name)
	glog.V(2).Infof("Symlinked %s to %s", servicePath, wantsPath)
	return nil
}

// disableUnit disables a systemd unit via symlink removal
func (dn *Daemon) disableUnit(unit igntypes.Unit) error {
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
func (dn *Daemon) writeUnits(units []igntypes.Unit) error {
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
func (dn *Daemon) writeFiles(files []igntypes.File) error {
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
		if err := createOrigFile(file.Path); err != nil {
			return err
		}
		if err := writeFileAtomically(file.Path, contents.Data, defaultDirectoryPermissions, mode, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func origParentDir() string {
	return filepath.Join("/etc", "machine-config-daemon", "orig")
}

func noOrigParentDir() string {
	return filepath.Join("/etc", "machine-config-daemon", "noorig")
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

func createOrigFile(fpath string) error {
	if _, err := os.Stat(noOrigFileStampName(fpath)); err == nil {
		// we already created the no orig file for this default file
		return nil
	}
	if _, err := os.Stat(fpath); os.IsNotExist(err) {
		// create a noorig file that tells the MCD that the file wasn't present on disk before MCD
		// took over so it can just remove it when deleting stale data, as opposed as restoring a file
		// that was shipped _with_ the underlying OS (e.g. a default chrony config).
		if err := os.MkdirAll(filepath.Dir(noOrigFileStampName(fpath)), 0755); err != nil {
			return errors.Wrapf(err, "creating no orig parent dir: %v", err)
		}
		return writeFileAtomicallyWithDefaults(noOrigFileStampName(fpath), nil)
	}
	if _, err := os.Stat(origFileName(fpath)); err == nil {
		// the orig file is already there and we avoid creating a new one to preserve the real default
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(origFileName(fpath)), 0755); err != nil {
		return errors.Wrapf(err, "creating orig parent dir: %v", err)
	}
	if out, err := exec.Command("cp", "-a", "--reflink=auto", fpath, origFileName(fpath)).CombinedOutput(); err != nil {
		return errors.Wrapf(err, "creating orig file for %q: %s", fpath, string(out))
	}
	return nil
}

// This is essentially ResolveNodeUidAndGid() from Ignition; XXX should dedupe
func getFileOwnership(file igntypes.File) (int, int, error) {
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

func (dn *Daemon) atomicallyWriteSSHKey(keys string) error {
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
func (dn *Daemon) updateSSHKeys(newUsers []igntypes.PasswdUser) error {
	if len(newUsers) == 0 {
		return nil
	}

	// we're also appending all keys for any user to core, so for now
	// we pass this to atomicallyWriteSSHKeys to write.
	var concatSSHKeys string
	for _, k := range newUsers[len(newUsers)-1].SSHAuthorizedKeys {
		concatSSHKeys = concatSSHKeys + string(k) + "\n"
	}
	if !dn.mock {
		// Note we write keys only for the core user and so this ignores the user list
		if err := dn.atomicallyWriteSSHKey(concatSSHKeys); err != nil {
			return err
		}
	}
	return nil
}

// updateOS updates the system OS to the one specified in newConfig
func (dn *Daemon) updateOS(config *mcfgv1.MachineConfig) error {
	if dn.OperatingSystem != machineConfigDaemonOSRHCOS && dn.OperatingSystem != machineConfigDaemonOSFCOS {
		glog.V(2).Info("Updating of non-CoreOS nodes are not supported")
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
		MCDPivotErr.WithLabelValues(newURL, err.Error()).SetToCurrentTime()
		return fmt.Errorf("failed to run pivot: %v", err)
	}

	return nil
}

func (dn *Daemon) getPendingStateLegacyLogger() (*journalMsg, error) {
	glog.Info("logger doesn't support --jounald, grepping the journal")

	cmdLiteral := "journalctl -o cat _UID=0 | grep -v audit | grep OPENSHIFT_MACHINE_CONFIG_DAEMON_LEGACY_LOG_HACK"
	cmd := exec.Command("bash", "-c", cmdLiteral)
	var combinedOutput bytes.Buffer
	cmd.Stdout = &combinedOutput
	cmd.Stderr = &combinedOutput
	if err := cmd.Start(); err != nil {
		return nil, errors.Wrap(err, "failed shelling out to journalctl -o cat")
	}
	if err := cmd.Wait(); err != nil {
		if exiterr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			status, ok := exiterr.Sys().(syscall.WaitStatus)
			if ok {
				// grep exit with 1 if it doesn't find anything
				// from man: Normally, the exit status is 0 if selected lines are found and 1 otherwise. But the exit status is 2 if an error occurred
				if status.ExitStatus() == 1 {
					return nil, nil
				}
				if status.ExitStatus() > 1 {
					return nil, errors.Wrapf(fmt.Errorf("grep exited with %s", combinedOutput.Bytes()), "failed to grep on journal output: %v", exiterr)
				}
			}
		} else {
			return nil, errors.Wrap(err, "command wait error")
		}
	}
	journalOutput := combinedOutput.Bytes()
	// just an extra safety check?
	if len(journalOutput) == 0 {
		return nil, nil
	}
	return dn.processJournalOutput(journalOutput)
}

type journalMsg struct {
	Message   string `json:"MESSAGE,omitempty"`
	BootID    string `json:"BOOT_ID,omitempty"`
	Pending   string `json:"PENDING,omitempty"`
	OldLogger string `json:"OPENSHIFT_MACHINE_CONFIG_DAEMON_LEGACY_LOG_HACK,omitempty"` // unused today
}

func (dn *Daemon) processJournalOutput(journalOutput []byte) (*journalMsg, error) {
	lines := strings.Split(strings.TrimSpace(string(journalOutput)), "\n")
	last := lines[len(lines)-1]

	entry := &journalMsg{}
	if err := json.Unmarshal([]byte(last), entry); err != nil {
		return nil, errors.Wrap(err, "getting pending state from journal")
	}
	if entry.Pending == "0" {
		return nil, nil
	}
	return entry, nil
}

// getPendingState loads the JSON state we cache across attempting to apply
// a config+reboot.  If no pending state is available, ("", nil) will be returned.
// The bootID is stored in the pending state; if it is unchanged, we assume
// that we failed to reboot; that for now should be a fatal error, in order to avoid
// reboot loops.
func (dn *Daemon) getPendingState() (*journalMsg, error) {
	if !dn.loggerSupportsJournal {
		return dn.getPendingStateLegacyLogger()
	}
	journalOutput, err := exec.Command("journalctl", "-o", "json", "_UID=0", fmt.Sprintf("MESSAGE_ID=%s", pendingStateMessageID)).CombinedOutput()
	if err != nil {
		return nil, errors.Wrap(err, "error running journalctl -o json")
	}
	if len(journalOutput) == 0 {
		return nil, nil
	}
	return dn.processJournalOutput(journalOutput)
}

func (dn *Daemon) storePendingStateLegacyLogger(pending *mcfgv1.MachineConfig, isPending int) ([]byte, error) {
	glog.Info("logger doesn't support --jounald, logging json directly")

	if isPending == 1 {
		if err := dn.writePendingConfig(pending); err != nil {
			return nil, err
		}
	} else {
		if err := os.Remove(pendingConfigPath); err != nil {
			return nil, err
		}
	}

	oldLogger := exec.Command("logger", fmt.Sprintf(`{"MESSAGE": "%s", "BOOT_ID": "%s", "PENDING": "%d", "OPENSHIFT_MACHINE_CONFIG_DAEMON_LEGACY_LOG_HACK": "1"}`, pending.GetName(), dn.bootID, isPending))
	return oldLogger.CombinedOutput()
}

func (dn *Daemon) storePendingState(pending *mcfgv1.MachineConfig, isPending int) ([]byte, error) {
	if !dn.loggerSupportsJournal {
		return dn.storePendingStateLegacyLogger(pending, isPending)
	}
	logger := exec.Command("logger", "--journald")

	var pendingState bytes.Buffer
	pendingState.WriteString(fmt.Sprintf(`MESSAGE_ID=%s
MESSAGE=%s
BOOT_ID=%s
PENDING=%d`, pendingStateMessageID, pending.GetName(), dn.bootID, isPending))

	logger.Stdin = &pendingState
	return logger.CombinedOutput()
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

	var log bytes.Buffer
	log.WriteString(fmt.Sprintf("machine-config-daemon[%d]: %s", os.Getpid(), message))

	logger.Stdin = &log
	if err := logger.Run(); err != nil {
		glog.Errorf("failed to invoke logger: %v", err)
	}
}

func (dn *Daemon) catchIgnoreSIGTERM() {
	dn.updateActiveLock.Lock()
	defer dn.updateActiveLock.Unlock()
	if dn.updateActive {
		return
	}
	dn.updateActive = true
}

func (dn *Daemon) cancelSIGTERM() {
	dn.updateActiveLock.Lock()
	defer dn.updateActiveLock.Unlock()
	if dn.updateActive {
		dn.updateActive = false
	}
}

// reboot is the final step. it tells systemd-logind to reboot the machine,
// cleans up the agent's connections, and then sleeps for 7 days. if it wakes up
// and manages to return, it returns a scary error message.
func (dn *Daemon) reboot(rationale string) error {
	// Now that everything is done, avoid delaying shutdown.
	dn.cancelSIGTERM()
	dn.Close()

	if dn.skipReboot {
		return nil
	}

	// We'll only have a recorder if we're cluster driven
	if dn.recorder != nil {
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "Reboot", rationale)
	}
	dn.logSystem("initiating reboot: %s", rationale)

	rebootCmd := rebootCommand(rationale)

	// reboot, executed async via systemd-run so that the reboot command is executed
	// in the context of the host asynchronously from us
	// We're not returning the error from the reboot command as it can be terminated by
	// the system itself with signal: terminated. We can't catch the subprocess termination signal
	// either, we just have one for the MCD itself.
	if err := rebootCmd.Run(); err != nil {
		dn.logSystem("failed to run reboot: %v", err)
		MCDRebootErr.WithLabelValues("failed to run reboot", err.Error()).SetToCurrentTime()
	}

	// wait to be killed via SIGTERM from the kubelet shutting down
	time.Sleep(defaultRebootTimeout)

	// if everything went well, this should be unreachable.
	MCDRebootErr.WithLabelValues("reboot failed", "this error should be unreachable, something is seriously wrong").SetToCurrentTime()
	return fmt.Errorf("reboot failed; this error should be unreachable, something is seriously wrong")
}
