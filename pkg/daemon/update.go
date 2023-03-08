package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/clarketm/json"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/uuid"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	pivottypes "github.com/openshift/machine-config-operator/pkg/daemon/pivot/types"
	pivotutils "github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
)

const (
	// defaultDirectoryPermissions houses the default mode to use when no directory permissions are provided
	defaultDirectoryPermissions os.FileMode = 0o755
	// defaultFilePermissions houses the default mode to use when no file permissions are provided
	defaultFilePermissions os.FileMode = 0o644
	// fipsFile is the file to check if FIPS is enabled
	fipsFile                   = "/proc/sys/crypto/fips_enabled"
	extensionsRepo             = "/etc/yum.repos.d/coreos-extensions.repo"
	osImageContentBaseDir      = "/run/mco-machine-os-content/"
	osExtensionsContentBaseDir = "/run/mco-extensions/"

	// These are the actions for a node to take after applying config changes. (e.g. a new machineconfig is applied)
	// "None" means no special action needs to be taken
	// This happens for example when ssh keys or the pull secret (/var/lib/kubelet/config.json) is changed
	postConfigChangeActionNone = "none"
	// The "reload crio" action will run "systemctl reload crio"
	postConfigChangeActionReloadCrio = "reload crio"
	// Rebooting is still the default scenario for any other change
	postConfigChangeActionReboot = "reboot"

	// GPGNoRebootPath is the path MCO expects will contain GPG key updates. MCO will attempt to only reload crio for
	// changes to this path. Note that other files added to the parent directory will not be handled specially
	GPGNoRebootPath = "/etc/machine-config-daemon/no-reboot/containers-gpg.pub"
)

func getNodeRef(node *corev1.Node) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		Kind: "Node",
		Name: node.GetName(),
		UID:  node.GetUID(),
	}
}

func reloadService(name string) error {
	return runCmdSync("systemctl", "reload", name)
}

// performPostConfigChangeAction takes action based on what postConfigChangeAction has been asked.
// For non-reboot action, it applies configuration, updates node's config and state.
// In the end uncordon node to schedule workload.
// If at any point an error occurs, we reboot the node so that node has correct configuration.
func (dn *Daemon) performPostConfigChangeAction(postConfigChangeActions []string, configName string) error {
	if ctrlcommon.InSlice(postConfigChangeActionReboot, postConfigChangeActions) {
		dn.logSystem("Rebooting node")
		return dn.reboot(fmt.Sprintf("Node will reboot into config %s", configName))
	}

	if ctrlcommon.InSlice(postConfigChangeActionNone, postConfigChangeActions) {
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "SkipReboot", "Config changes do not require reboot.")
		}
		dn.logSystem("Node has Desired Config %s, skipping reboot", configName)
	}

	if ctrlcommon.InSlice(postConfigChangeActionReloadCrio, postConfigChangeActions) {
		serviceName := "crio"

		if err := reloadService(serviceName); err != nil {
			if dn.nodeWriter != nil {
				dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedServiceReload", fmt.Sprintf("Reloading %s service failed. Error: %v", serviceName, err))
			}
			return fmt.Errorf("could not apply update: reloading %s configuration failed. Error: %w", serviceName, err)
		}

		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "SkipReboot", "Config changes do not require reboot. Service %s was reloaded.", serviceName)
		}
		dn.logSystem("%s config reloaded successfully! Desired config %s has been applied, skipping reboot", serviceName, configName)
	}

	// We are here, which means reboot was not needed to apply the configuration.

	// Get current state of node, in case of an error reboot
	state, err := dn.getStateAndConfigs(configName)
	if err != nil {
		return fmt.Errorf("could not apply update: error processing state and configs. Error: %w", err)
	}

	var inDesiredConfig bool
	if inDesiredConfig, err = dn.updateConfigAndState(state); err != nil {
		return fmt.Errorf("could not apply update: setting node's state to Done failed. Error: %w", err)
	}
	if inDesiredConfig {
		// (re)start the config drift monitor since rebooting isn't needed.
		dn.startConfigDriftMonitor()
		return nil
	}

	// currentConfig != desiredConfig, kick off an update
	return dn.triggerUpdateWithMachineConfig(state.currentConfig, state.desiredConfig)
}

// finalizeBeforeReboot is the last step in an update() and then we take appropriate postConfigChangeAction.
// It can also be called as a special case for the "bootstrap pivot".
func (dn *Daemon) finalizeBeforeReboot(newConfig *mcfgv1.MachineConfig) (retErr error) {
	if out, err := dn.storePendingState(newConfig, 1); err != nil {
		return fmt.Errorf("failed to log pending config: %s: %w", string(out), err)
	}
	defer func() {
		if retErr != nil {
			if dn.nodeWriter != nil {
				dn.nodeWriter.Eventf(corev1.EventTypeNormal, "PendingConfigRollBack", fmt.Sprintf("Rolling back pending config %s: %v", newConfig.GetName(), retErr))
			}
			if out, err := dn.storePendingState(newConfig, 0); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back pending config %s: %w", string(out), errs)
				return
			}
		}
	}()
	if dn.nodeWriter != nil {
		dn.nodeWriter.Eventf(corev1.EventTypeNormal, "PendingConfig", fmt.Sprintf("Written pending config %s", newConfig.GetName()))
	}

	return nil
}

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

// return true if the machineConfigDiff is not empty
func (dn *Daemon) compareMachineConfig(oldConfig, newConfig *mcfgv1.MachineConfig) (bool, error) {
	oldConfig = canonicalizeEmptyMC(oldConfig)
	oldConfigName := oldConfig.GetName()
	newConfigName := newConfig.GetName()
	mcDiff, err := newMachineConfigDiff(oldConfig, newConfig)
	if err != nil {
		return true, fmt.Errorf("error creating machineConfigDiff for comparison: %w", err)
	}
	if mcDiff.isEmpty() {
		glog.Infof("No changes from %s to %s", oldConfigName, newConfigName)
		return false, nil
	}
	return true, nil
}

// addExtensionsRepo adds a repo into /etc/yum.repos.d/ which we use later to
// install extensions and rt-kernel
func addExtensionsRepo(osImageContentDir string) error {
	repoContent := "[coreos-extensions]\nenabled=1\nmetadata_expire=1m\nbaseurl=" + osImageContentDir + "/extensions/\ngpgcheck=0\nskip_if_unavailable=False\n"
	if err := writeFileAtomicallyWithDefaults(extensionsRepo, []byte(repoContent)); err != nil {
		return err
	}
	return nil
}

// addLayeredExtensionsRepo adds a repo into /etc/yum.repos.d/ which we use later to
// install extensions and rt-kernel. This is separate from addExtensionsRepo because when we're
// extracting only the extensions container (because with the new format images they are packaged separately),
// we extract to a different location
func addLayeredExtensionsRepo(extensionsImageContentDir string) error {
	repoContent := "[coreos-extensions]\nenabled=1\nmetadata_expire=1m\nbaseurl=" + extensionsImageContentDir + "/usr/share/rpm-ostree/extensions/\ngpgcheck=0\nskip_if_unavailable=False\n"
	if err := writeFileAtomicallyWithDefaults(extensionsRepo, []byte(repoContent)); err != nil {
		return err
	}
	return nil
}

// podmanRemove kills and removes a container
func podmanRemove(cid string) {
	// Ignore errors here
	exec.Command("podman", "kill", cid).Run()
	exec.Command("podman", "rm", "-f", cid).Run()
}

func podmanCopy(imgURL, osImageContentDir string) (err error) {
	// make sure that osImageContentDir doesn't exist
	os.RemoveAll(osImageContentDir)

	// Pull the container image
	var authArgs []string
	if _, err := os.Stat(kubeletAuthFile); err == nil {
		authArgs = append(authArgs, "--authfile", kubeletAuthFile)
	}
	args := []string{"pull", "-q"}
	args = append(args, authArgs...)
	args = append(args, imgURL)
	_, err = pivotutils.RunExtBackground(numRetriesNetCommands, "podman", args...)
	if err != nil {
		return
	}

	// create a container
	var cidBuf []byte
	containerName := pivottypes.PivotNamePrefix + string(uuid.NewUUID())
	cidBuf, err = runGetOut("podman", "create", "--net=none", "--annotation=org.openshift.machineconfigoperator.pivot=true", "--name", containerName, imgURL)
	if err != nil {
		return
	}

	// only delete created container, we will delete container image later as we may need it for podmanInspect()
	defer podmanRemove(containerName)

	// copy the content from create container locally into a temp directory under /run/machine-os-content/
	cid := strings.TrimSpace(string(cidBuf))
	args = []string{"cp", fmt.Sprintf("%s:/", cid), osImageContentDir}
	_, err = pivotutils.RunExtBackground(numRetriesNetCommands, "podman", args...)
	if err != nil {
		return
	}

	// Set selinux context to var_run_t to avoid selinux denial
	args = []string{"-R", "-t", "var_run_t", osImageContentDir}
	err = runCmdSync("chcon", args...)
	if err != nil {
		err = fmt.Errorf("changing selinux context on path %s: %w", osImageContentDir, err)
		return
	}
	return
}

// ExtractOSImage extracts OS image content in a temporary directory under /run/machine-os-content/
// and returns the path on successful extraction.
// Note that since we do this in the MCD container, cluster proxy configuration must also be injected
// into the container. See the MCD daemonset.
func ExtractOSImage(imgURL string) (osImageContentDir string, err error) {
	var registryConfig []string
	if _, err := os.Stat(kubeletAuthFile); err == nil {
		registryConfig = append(registryConfig, "--registry-config", kubeletAuthFile)
	}
	if err = os.MkdirAll(osImageContentBaseDir, 0o755); err != nil {
		err = fmt.Errorf("error creating directory %s: %w", osImageContentBaseDir, err)
		return
	}

	if osImageContentDir, err = os.MkdirTemp(osImageContentBaseDir, "os-content-"); err != nil {
		return
	}

	// Extract the image
	args := []string{"image", "extract", "-v", "10", "--path", "/:" + osImageContentDir}
	args = append(args, registryConfig...)
	args = append(args, imgURL)
	if _, err = pivotutils.RunExtBackground(cmdRetriesCount, "oc", args...); err != nil {
		// Workaround fixes for the environment where oc image extract fails.
		// See https://bugzilla.redhat.com/show_bug.cgi?id=1862979
		glog.Infof("Falling back to using podman cp to fetch OS image content")
		if err = podmanCopy(imgURL, osImageContentDir); err != nil {
			return
		}
	}

	return
}

// ExtractExtensionsImage extracts the OS extensions content in a temporary directory under /run/machine-os-extensions
// and returns the path on successful extraction
func ExtractExtensionsImage(imgURL string) (osExtensionsImageContentDir string, err error) {
	var registryConfig []string
	if _, err := os.Stat(kubeletAuthFile); err == nil {
		registryConfig = append(registryConfig, "--registry-config", kubeletAuthFile)
	}
	if err = os.MkdirAll(osExtensionsContentBaseDir, 0o755); err != nil {
		err = fmt.Errorf("error creating directory %s: %w", osExtensionsContentBaseDir, err)
		return
	}

	if osExtensionsImageContentDir, err = os.MkdirTemp(osExtensionsContentBaseDir, "os-extensions-content-"); err != nil {
		return
	}

	// Extract the image
	args := []string{"image", "extract", "-v", "10", "--path", "/:" + osExtensionsImageContentDir}
	args = append(args, registryConfig...)
	args = append(args, imgURL)
	if _, err = pivotutils.RunExtBackground(cmdRetriesCount, "oc", args...); err != nil {
		// Workaround fixes for the environment where oc image extract fails.
		// See https://bugzilla.redhat.com/show_bug.cgi?id=1862979
		glog.Infof("Falling back to using podman cp to fetch OS image content")
		if err = podmanCopy(imgURL, osExtensionsImageContentDir); err != nil {
			return
		}
	}

	return
}

// Remove pending deployment on OSTree based system
func removePendingDeployment() error {
	return runRpmOstree("cleanup", "-p")
}

func (dn *CoreOSDaemon) applyOSChanges(mcDiff machineConfigDiff, oldConfig, newConfig *mcfgv1.MachineConfig) (retErr error) {
	// Extract image and add coreos-extensions repo if we have either OS update or package layering to perform

	if dn.nodeWriter != nil {
		dn.nodeWriter.Eventf(corev1.EventTypeNormal, "OSUpdateStarted", mcDiff.osChangesString())
	}

	// We previously did not emit this event when kargs changed, so we still don't
	if mcDiff.osUpdate || mcDiff.extensions || mcDiff.kernelType {
		// We emitted this event before, so keep it
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "InClusterUpgrade", fmt.Sprintf("Updating from oscontainer %s", newConfig.Spec.OSImageURL))
		}
	}

	// Only check the image type and excute OS changes if:
	// - machineconfig changed
	// - we're staying on a realtime kernel ( need to run rpm-ostree update )
	// - we have extensions ( need to run rpm-ostree update )
	// We have at least one customer that removes the pull secret from the cluster to "shrinkwrap" it for distribution and we want
	// to make sure we don't break that use case, but realtime kernel update and extensions update always ran
	// if they were in use, so we also need to preserve that behavior.
	// https://issues.redhat.com/browse/OCPBUGS-4049
	if mcDiff.osUpdate || mcDiff.extensions || mcDiff.kernelType || mcDiff.kargs ||
		canonicalizeKernelType(newConfig.Spec.KernelType) == ctrlcommon.KernelTypeRealtime || len(newConfig.Spec.Extensions) > 0 {

		// The steps from here on are different depending on the image type, so check the image type
		isLayeredImage, err := dn.NodeUpdaterClient.IsBootableImage(newConfig.Spec.OSImageURL)
		if err != nil {
			return fmt.Errorf("Error checking type of update image: %w", err)
		}

		// TODO(jkyros): we can remove the format check and simplify this once we retire the "old format" images
		if isLayeredImage {
			// If it's a layered/bootable image, then apply it the "new" way
			if err := dn.applyLayeredOSChanges(mcDiff, oldConfig, newConfig); err != nil {
				return err
			}
		} else {
			// Otherwise fall back to the old way -- we can take this out someday when it goes away
			if err := dn.applyLegacyOSChanges(mcDiff, oldConfig, newConfig); err != nil {
				return err
			}
		}
	}

	if dn.nodeWriter != nil {
		var nodeName string
		var nodeObjRef corev1.ObjectReference
		if dn.node != nil {
			nodeName = dn.node.ObjectMeta.GetName()
			nodeObjRef = corev1.ObjectReference{
				Kind: "Node",
				Name: dn.node.GetName(),
				UID:  dn.node.GetUID(),
			}
		}
		// We send out the event OSUpdateStaged synchronously to ensure it is recorded.
		// Remove this when we can ensure all events are sent before exiting.
		t := metav1.NewTime(time.Now())
		event := &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%v.%x", nodeName, t.UnixNano()),
				Namespace: metav1.NamespaceDefault,
			},
			InvolvedObject: nodeObjRef,
			Reason:         "OSUpdateStaged",
			Type:           corev1.EventTypeNormal,
			Message:        "Changes to OS staged",
			FirstTimestamp: t,
			LastTimestamp:  t,
			Count:          1,
			Source:         corev1.EventSource{Component: "machineconfigdaemon", Host: dn.name},
		}
		// its ok to create a unique event for this low volume event
		if _, err := dn.kubeClient.CoreV1().Events(metav1.NamespaceDefault).Create(context.TODO(),
			event, metav1.CreateOptions{}); err != nil {
			glog.Errorf("Failed to create event with reason 'OSUpdateStaged': %v", err)
		}
	}

	return nil
}

func calculatePostConfigChangeActionFromFileDiffs(diffFileSet []string) (actions []string) {
	filesPostConfigChangeActionNone := []string{
		"/etc/kubernetes/kubelet-ca.crt",
		"/var/lib/kubelet/config.json",
	}
	filesPostConfigChangeActionReloadCrio := []string{
		constants.ContainerRegistryConfPath,
		GPGNoRebootPath,
		"/etc/containers/policy.json",
	}

	actions = []string{postConfigChangeActionNone}
	for _, path := range diffFileSet {
		if ctrlcommon.InSlice(path, filesPostConfigChangeActionNone) {
			continue
		} else if ctrlcommon.InSlice(path, filesPostConfigChangeActionReloadCrio) {
			actions = []string{postConfigChangeActionReloadCrio}
		} else {
			actions = []string{postConfigChangeActionReboot}
			return
		}
	}
	return
}

func calculatePostConfigChangeAction(diff *machineConfigDiff, diffFileSet []string) ([]string, error) {
	// If a machine-config-daemon-force file is present, it means the user wants to
	// move to desired state without additional validation. We will reboot the node in
	// this case regardless of what MachineConfig diff is.
	if _, err := os.Stat(constants.MachineConfigDaemonForceFile); err == nil {
		if err := os.Remove(constants.MachineConfigDaemonForceFile); err != nil {
			return []string{}, fmt.Errorf("failed to remove force validation file: %w", err)
		}
		glog.Infof("Setting post config change action to postConfigChangeActionReboot; %s present", constants.MachineConfigDaemonForceFile)
		return []string{postConfigChangeActionReboot}, nil
	}

	if diff.osUpdate || diff.kargs || diff.fips || diff.units || diff.kernelType || diff.extensions {
		// must reboot
		return []string{postConfigChangeActionReboot}, nil
	}

	// We don't actually have to consider ssh keys changes, which is the only section of passwd that is allowed to change
	return calculatePostConfigChangeActionFromFileDiffs(diffFileSet), nil
}

// update the node to the provided node configuration.
//
//nolint:gocyclo
func (dn *Daemon) update(oldConfig, newConfig *mcfgv1.MachineConfig) (retErr error) {
	oldConfig = canonicalizeEmptyMC(oldConfig)

	if dn.nodeWriter != nil {
		state, err := getNodeAnnotationExt(dn.node, constants.MachineConfigDaemonStateAnnotationKey, true)
		if err != nil {
			return err
		}
		if state != constants.MachineConfigDaemonStateDegraded && state != constants.MachineConfigDaemonStateUnreconcilable {
			if err := dn.nodeWriter.SetWorking(); err != nil {
				return fmt.Errorf("error setting node's state to Working: %w", err)
			}
		}
	}

	dn.catchIgnoreSIGTERM()
	defer func() {
		// now that we do rebootless updates, we need to turn off our SIGTERM protection
		// regardless of how we leave the "update loop"
		dn.cancelSIGTERM()
	}()

	oldConfigName := oldConfig.GetName()
	newConfigName := newConfig.GetName()

	oldIgnConfig, err := ctrlcommon.ParseAndConvertConfig(oldConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing old Ignition config failed: %w", err)
	}
	newIgnConfig, err := ctrlcommon.ParseAndConvertConfig(newConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing new Ignition config failed: %w", err)
	}

	glog.Infof("Checking Reconcilable for config %v to %v", oldConfigName, newConfigName)

	// make sure we can actually reconcile this state
	diff, reconcilableError := reconcilable(oldConfig, newConfig)

	if reconcilableError != nil {
		wrappedErr := fmt.Errorf("can't reconcile config %s with %s: %w", oldConfigName, newConfigName, reconcilableError)
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedToReconcile", wrappedErr.Error())
		}
		return &unreconcilableErr{wrappedErr}
	}

	dn.logSystem("Starting update from %s to %s: %+v", oldConfigName, newConfigName, diff)

	diffFileSet := ctrlcommon.CalculateConfigFileDiffs(&oldIgnConfig, &newIgnConfig)
	actions, err := calculatePostConfigChangeAction(diff, diffFileSet)
	if err != nil {
		return err
	}

	// Check and perform node drain if required
	drain, err := isDrainRequired(actions, diffFileSet, oldIgnConfig, newIgnConfig)
	if err != nil {
		return err
	}
	if drain {
		if err := dn.performDrain(); err != nil {
			return err
		}
	} else {
		glog.Info("Changes do not require drain, skipping.")
	}

	// update files on disk that need updating
	if err := dn.updateFiles(oldIgnConfig, newIgnConfig); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.updateFiles(newIgnConfig, oldIgnConfig); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back files writes: %w", errs)
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
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back SSH keys updates: %w", errs)
				return
			}
		}
	}()

	// Set password hash
	if err := dn.SetPasswordHash(newIgnConfig.Passwd.Users); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.SetPasswordHash(oldIgnConfig.Passwd.Users); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back password hash updates: %w", errs)
				return
			}
		}
	}()

	if dn.os.IsCoreOSVariant() {
		coreOSDaemon := CoreOSDaemon{dn}
		if err := coreOSDaemon.applyOSChanges(*diff, oldConfig, newConfig); err != nil {
			return err
		}

		defer func() {
			if retErr != nil {
				if err := coreOSDaemon.applyOSChanges(*diff, newConfig, oldConfig); err != nil {
					errs := kubeErrs.NewAggregate([]error{err, retErr})
					retErr = fmt.Errorf("error rolling back changes to OS: %w", errs)
					return
				}
			}
		}()
	} else {
		glog.Info("updating the OS on non-CoreOS nodes is not supported")
	}

	// Ideally we would want to update kernelArguments only via MachineConfigs.
	// We are keeping this to maintain compatibility and OKD requirement.
	if err := UpdateTuningArgs(KernelTuningFile, CmdLineFile); err != nil {
		return err
	}

	// At this point, we write the now expected to be "current" config to /etc.
	// When we reboot, we'll find this file and validate that we're in this state,
	// and that completes an update.
	if err := dn.storeCurrentConfigOnDisk(newConfig); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			if err := dn.storeCurrentConfigOnDisk(oldConfig); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back current config on disk: %w", errs)
				return
			}
		}
	}()

	if err := dn.finalizeBeforeReboot(newConfig); err != nil {
		return err
	}

	return dn.performPostConfigChangeAction(actions, newConfig.GetName())
}

// This is currently a subsection copied over from update() since we need to be more nuanced. Should eventually
// de-dupe the functions.
func (dn *Daemon) updateHypershift(oldConfig, newConfig *mcfgv1.MachineConfig, diff *machineConfigDiff) (retErr error) {
	oldIgnConfig, err := ctrlcommon.ParseAndConvertConfig(oldConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing old Ignition config failed: %w", err)
	}
	newIgnConfig, err := ctrlcommon.ParseAndConvertConfig(newConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing new Ignition config failed: %w", err)
	}

	// update files on disk that need updating
	if err := dn.updateFiles(oldIgnConfig, newIgnConfig); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.updateFiles(newIgnConfig, oldIgnConfig); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back files writes: %w", errs)
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
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back SSH keys updates: %w", errs)
				return
			}
		}
	}()

	if dn.os.IsCoreOSVariant() {
		coreOSDaemon := CoreOSDaemon{dn}
		if err := coreOSDaemon.applyOSChanges(*diff, oldConfig, newConfig); err != nil {
			return err
		}

		defer func() {
			if retErr != nil {
				if err := coreOSDaemon.applyOSChanges(*diff, newConfig, oldConfig); err != nil {
					errs := kubeErrs.NewAggregate([]error{err, retErr})
					retErr = fmt.Errorf("error rolling back changes to OS: %w", errs)
					return
				}
			}
		}()
	} else {
		glog.Info("updating the OS on non-CoreOS nodes is not supported")
	}

	if err := UpdateTuningArgs(KernelTuningFile, CmdLineFile); err != nil {
		return err
	}

	glog.Info("Successfully completed Hypershift config update")
	return nil
}

// removeRollback removes the rpm-ostree rollback deployment.
// It takes up space and can cause issues when /boot contains multiple
// initramfs images: https://bugzilla.redhat.com/show_bug.cgi?id=2104619.
// We don't generally expect administrators to use this versus e.g. removing
// broken configuration. We only remove the rollback once the MCD pod has
// landed on a node, so we know kubelet is working.
func (dn *Daemon) removeRollback() error {
	if !dn.os.IsCoreOSVariant() {
		// do not attempt to rollback on non-RHCOS/FCOS machines
		return nil
	}
	return runRpmOstree("cleanup", "-r")
}

// machineConfigDiff represents an ad-hoc difference between two MachineConfig objects.
// At some point this may change into holding just the files/units that changed
// and the MCO would just operate on that.  For now we're just doing this to get
// improved logging.
type machineConfigDiff struct {
	osUpdate   bool
	kargs      bool
	fips       bool
	passwd     bool
	files      bool
	units      bool
	kernelType bool
	extensions bool
}

// isEmpty returns true if the machineConfigDiff has no changes, or
// in other words if the two MachineConfig objects are equivalent from
// the MCD's point of view.  This is mainly relevant if e.g. two MC
// objects happen to have different Ignition versions but are otherwise
// the same.  (Probably a better way would be to canonicalize)
func (mcDiff *machineConfigDiff) isEmpty() bool {
	emptyDiff := machineConfigDiff{}
	return reflect.DeepEqual(mcDiff, &emptyDiff)
}

// osChangesString generates a human-readable set of changes from the diff
func (mcDiff *machineConfigDiff) osChangesString() string {
	changes := []string{}
	if mcDiff.osUpdate {
		changes = append(changes, "Upgrading OS")
	}
	if mcDiff.extensions {
		changes = append(changes, "Installing extensions")
	}
	if mcDiff.kernelType {
		changes = append(changes, "Changing kernel type")
	}
	if mcDiff.kargs {
		changes = append(changes, "Changing kernel arguments")
	}

	return strings.Join(changes, "; ")
}

// canonicalizeKernelType returns a valid kernelType. We consider empty("") and default kernelType as same
func canonicalizeKernelType(kernelType string) string {
	if kernelType == ctrlcommon.KernelTypeRealtime {
		return ctrlcommon.KernelTypeRealtime
	}
	return ctrlcommon.KernelTypeDefault
}

// newMachineConfigDiff compares two MachineConfig objects.
func newMachineConfigDiff(oldConfig, newConfig *mcfgv1.MachineConfig) (*machineConfigDiff, error) {
	oldIgn, err := ctrlcommon.ParseAndConvertConfig(oldConfig.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing old Ignition config failed with error: %w", err)
	}
	newIgn, err := ctrlcommon.ParseAndConvertConfig(newConfig.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing new Ignition config failed with error: %w", err)
	}

	// Both nil and empty slices are of zero length,
	// consider them as equal while comparing KernelArguments in both MachineConfigs
	kargsEmpty := len(oldConfig.Spec.KernelArguments) == 0 && len(newConfig.Spec.KernelArguments) == 0
	extensionsEmpty := len(oldConfig.Spec.Extensions) == 0 && len(newConfig.Spec.Extensions) == 0

	return &machineConfigDiff{
		osUpdate:   oldConfig.Spec.OSImageURL != newConfig.Spec.OSImageURL,
		kargs:      !(kargsEmpty || reflect.DeepEqual(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments)),
		fips:       oldConfig.Spec.FIPS != newConfig.Spec.FIPS,
		passwd:     !reflect.DeepEqual(oldIgn.Passwd, newIgn.Passwd),
		files:      !reflect.DeepEqual(oldIgn.Storage.Files, newIgn.Storage.Files),
		units:      !reflect.DeepEqual(oldIgn.Systemd.Units, newIgn.Systemd.Units),
		kernelType: canonicalizeKernelType(oldConfig.Spec.KernelType) != canonicalizeKernelType(newConfig.Spec.KernelType),
		extensions: !(extensionsEmpty || reflect.DeepEqual(oldConfig.Spec.Extensions, newConfig.Spec.Extensions)),
	}, nil
}

// reconcilable checks the configs to make sure that the only changes requested
// are ones we know how to do in-place.  If we can reconcile, (nil, nil) is returned.
// Otherwise, if we can't do it in place, the node is marked as degraded;
// the returned string value includes the rationale.
//
// we can only update machine configs that have changes to the files,
// directories, links, and systemd units sections of the included ignition
// config currently.
func reconcilable(oldConfig, newConfig *mcfgv1.MachineConfig) (*machineConfigDiff, error) {
	// The parser will try to translate versions less than maxVersion to maxVersion, or output an err.
	// The ignition output in case of success will always have maxVersion
	oldIgn, err := ctrlcommon.ParseAndConvertConfig(oldConfig.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing old Ignition config failed with error: %w", err)
	}
	newIgn, err := ctrlcommon.ParseAndConvertConfig(newConfig.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing new Ignition config failed with error: %w", err)
	}

	// Check if this is a generally valid Ignition Config
	if err := ctrlcommon.ValidateIgnition(newIgn); err != nil {
		return nil, err
	}

	// Passwd section

	// we don't currently configure Groups in place. we don't configure Users except
	// for setting/updating SSHAuthorizedKeys for the only allowed user "core".
	// otherwise we can't fix it if something changed here.
	passwdChanged := !reflect.DeepEqual(oldIgn.Passwd, newIgn.Passwd)

	if passwdChanged {
		if !reflect.DeepEqual(oldIgn.Passwd.Groups, newIgn.Passwd.Groups) {
			return nil, fmt.Errorf("ignition Passwd Groups section contains changes")
		}
		if !reflect.DeepEqual(oldIgn.Passwd.Users, newIgn.Passwd.Users) {
			if len(oldIgn.Passwd.Users) > 0 && len(newIgn.Passwd.Users) == 0 {
				return nil, fmt.Errorf("ignition passwd user section contains unsupported changes: user core may not be deleted")
			}
			// there is an update to Users, we must verify that it is ONLY making an acceptable
			// change to the SSHAuthorizedKeys for the user "core"
			for _, user := range newIgn.Passwd.Users {
				if user.Name != constants.CoreUserName {
					return nil, fmt.Errorf("ignition passwd user section contains unsupported changes: non-core user")
				}
			}

			glog.Infof("user data to be verified before ssh update: %v", newIgn.Passwd.Users[len(newIgn.Passwd.Users)-1])
			if err := verifyUserFields(newIgn.Passwd.Users[len(newIgn.Passwd.Users)-1]); err != nil {
				return nil, err
			}
		}
	}

	// Storage section

	// we can only reconcile files right now. make sure the sections we can't
	// fix aren't changed.
	if !reflect.DeepEqual(oldIgn.Storage.Disks, newIgn.Storage.Disks) {
		return nil, fmt.Errorf("ignition disks section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Filesystems, newIgn.Storage.Filesystems) {
		return nil, fmt.Errorf("ignition filesystems section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Raid, newIgn.Storage.Raid) {
		return nil, fmt.Errorf("ignition raid section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Directories, newIgn.Storage.Directories) {
		return nil, fmt.Errorf("ignition directories section contains changes")
	}
	if !reflect.DeepEqual(oldIgn.Storage.Links, newIgn.Storage.Links) {
		// This means links have been added, as opposed as being removed as it happened with
		// https://bugzilla.redhat.com/show_bug.cgi?id=1677198. This doesn't really change behavior
		// since we still don't support links but we allow old MC to remove links when upgrading.
		if len(newIgn.Storage.Links) != 0 {
			return nil, fmt.Errorf("ignition links section contains changes")
		}
	}

	// Special case files append: if the new config wants us to append, then we
	// have to force a reprovision since it's not idempotent
	for _, f := range newIgn.Storage.Files {
		if len(f.Append) > 0 {
			return nil, fmt.Errorf("ignition file %v includes append", f.Path)
		}
		// We also disallow writing some special files
		if f.Path == constants.MachineConfigDaemonForceFile {
			return nil, fmt.Errorf("cannot create %s via Ignition", f.Path)
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
	mcDiff, err := newMachineConfigDiff(oldConfig, newConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating machineConfigDiff: %w", err)
	}
	return mcDiff, nil
}

// verifyUserFields returns nil for the user Name = "core" if 1 or more SSHKeys exist for
// this user or if a password exists for this user and if all other fields in User are empty.
// Otherwise, an error will be returned and the proposed config will not be reconcilable.
// At this time we do not support non-"core" users or any changes to the "core" user
// outside of SSHAuthorizedKeys and passwordHash.
func verifyUserFields(pwdUser ign3types.PasswdUser) error {
	emptyUser := ign3types.PasswdUser{}
	tempUser := pwdUser
	if tempUser.Name == constants.CoreUserName && ((tempUser.PasswordHash) != nil || len(tempUser.SSHAuthorizedKeys) >= 1) {
		tempUser.Name = ""
		tempUser.SSHAuthorizedKeys = nil
		tempUser.PasswordHash = nil
		if !reflect.DeepEqual(emptyUser, tempUser) {
			return fmt.Errorf("SSH keys and password hash are not reconcilable")
		}
		glog.Info("SSH Keys reconcilable")
	} else {
		return fmt.Errorf("ignition passwd user section contains unsupported changes: user must be core and have 1 or more sshKeys")
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
	content, err := os.ReadFile(fipsFile)
	if err != nil {
		if os.IsNotExist(err) {
			// we just exit cleanly if we're not even on linux
			glog.Infof("no %s on this system, skipping FIPS check", fipsFile)
			return nil
		}
		return fmt.Errorf("error reading FIPS file at %s: %s: %w", fipsFile, string(content), err)
	}
	nodeFIPS, err := strconv.ParseBool(strings.TrimSuffix(string(content), "\n"))
	if err != nil {
		return fmt.Errorf("error parsing FIPS file at %s: %w", fipsFile, err)
	}
	if desired.Spec.FIPS == nodeFIPS {
		if desired.Spec.FIPS {
			glog.Infof("FIPS is configured and enabled")
		}
		// Check if FIPS on the system is at the desired setting
		current.Spec.FIPS = nodeFIPS
		return nil
	}
	return fmt.Errorf("detected change to FIPS flag; refusing to modify FIPS on a running cluster")
}

// checks for white-space characters in "C" and "POSIX" locales.
func isSpace(b byte) bool {
	return b == ' ' || b == '\f' || b == '\n' || b == '\r' || b == '\t' || b == '\v'
}

// You can use " around spaces, but can't escape ". See next_arg() in kernel code /lib/cmdline.c
// Gives the start and stop index for the next arg in the string, beyond the provided `begin` index
func nextArg(args string, begin int) (int, int) {
	var (
		start, stop int
		inQuote     bool
	)
	// Skip leading spaces
	for start = begin; start < len(args) && isSpace(args[start]); start++ {
	}
	stop = start
	for ; stop < len(args); stop++ {
		if isSpace(args[stop]) && !inQuote {
			break
		}

		if args[stop] == '"' {
			inQuote = !inQuote
		}
	}

	return start, stop
}

func splitKernelArguments(args string) []string {
	var (
		start, stop int
		split       []string
	)
	for stop < len(args) {
		start, stop = nextArg(args, stop)
		if start != stop {
			split = append(split, args[start:stop])
		}
	}
	return split
}

// parseKernelArguments separates out kargs from each entry and returns it as a map for
// easy comparison
func parseKernelArguments(kargs []string) []string {
	parsed := []string{}
	for _, k := range kargs {
		for _, arg := range splitKernelArguments(k) {
			parsed = append(parsed, strings.TrimSpace(arg))
		}
	}
	return parsed
}

// generateKargs performs a diff between the old/new MC kernelArguments,
// and generates the command line arguments suitable for `rpm-ostree kargs`.
// Note what we really should be doing though is also looking at the *current*
// kernel arguments in case there was drift.  But doing that requires us knowing
// what the "base" arguments are. See https://github.com/ostreedev/ostree/issues/479
func generateKargs(oldKernelArguments, newKernelArguments []string) []string {
	oldKargs := parseKernelArguments(oldKernelArguments)
	newKargs := parseKernelArguments(newKernelArguments)
	cmdArgs := []string{}

	// To keep kernel argument processing simpler and bug free, we first delete all
	// kernel arguments which have been applied by MCO previously and append all of the
	// kernel arguments present in the new rendered MachineConfig.
	// See https://bugzilla.redhat.com/show_bug.cgi?id=1866546#c10.
	for _, arg := range oldKargs {
		cmdArgs = append(cmdArgs, "--delete="+arg)
	}
	for _, arg := range newKargs {
		cmdArgs = append(cmdArgs, "--append="+arg)
	}
	return cmdArgs
}

// updateKernelArguments adjusts the kernel args
func (dn *CoreOSDaemon) updateKernelArguments(oldKernelArguments, newKernelArguments []string) error {
	kargs := generateKargs(oldKernelArguments, newKernelArguments)
	if len(kargs) == 0 {
		return nil
	}

	args := append([]string{"kargs"}, kargs...)
	dn.logSystem("Running rpm-ostree %v", args)
	return runRpmOstree(args...)
}

func (dn *Daemon) generateExtensionsArgs(oldConfig, newConfig *mcfgv1.MachineConfig) []string {
	removed := []string{}
	added := []string{}

	oldExt := make(map[string]bool)
	for _, ext := range oldConfig.Spec.Extensions {
		oldExt[ext] = true
	}
	newExt := make(map[string]bool)
	for _, ext := range newConfig.Spec.Extensions {
		newExt[ext] = true
	}

	for ext := range oldExt {
		if !newExt[ext] {
			removed = append(removed, ext)
		}
	}
	for ext := range newExt {
		if !oldExt[ext] {
			added = append(added, ext)
		}
	}

	// Supported extensions has package list info that is required
	// to enable an extension

	extArgs := []string{"update"}

	if dn.os.IsEL() {
		extensions := getSupportedExtensions()
		for _, ext := range added {
			for _, pkg := range extensions[ext] {
				extArgs = append(extArgs, "--install", pkg)
			}
		}
		for _, ext := range removed {
			for _, pkg := range extensions[ext] {
				extArgs = append(extArgs, "--uninstall", pkg)
			}
		}
	}

	// FCOS does one to one mapping of extension to package to be installed on FCOS node.
	// This is needed as OKD layers additional packages on top of official FCOS shipped,
	// See https://github.com/openshift/release/blob/959c2954344438c4eed3ec7f52a5e099e8335516/ci-operator/jobs/openshift/release/openshift-release-release-4.7-periodics.yaml#L586
	// TODO: Once the package list has been stabilized, we can make use of the group and add
	// all the packages required to enable OKD as a single extension.
	if dn.os.IsFCOS() {
		for _, ext := range added {
			extArgs = append(extArgs, "--install", ext)
		}
		for _, ext := range removed {
			extArgs = append(extArgs, "--uninstall", ext)
		}
	}

	return extArgs
}

// Returns list of extensions possible to install on a CoreOS based system.
func getSupportedExtensions() map[string][]string {
	// In future when list of extensions grow, it will make
	// more sense to populate it in a dynamic way.

	// These are RHCOS supported extensions.
	// Each extension keeps a list of packages required to get enabled on host.
	return map[string][]string{
		"usbguard":             {"usbguard"},
		"kerberos":             {"krb5-workstation", "libkadm5"},
		"kernel-devel":         {"kernel-devel", "kernel-headers"},
		"sandboxed-containers": {"kata-containers"},
	}
}

func validateExtensions(exts []string) error {
	supportedExtensions := getSupportedExtensions()
	invalidExts := []string{}
	for _, ext := range exts {
		if _, ok := supportedExtensions[ext]; !ok {
			invalidExts = append(invalidExts, ext)
		}
	}
	if len(invalidExts) != 0 {
		return fmt.Errorf("invalid extensions found: %v", invalidExts)
	}
	return nil

}

func (dn *CoreOSDaemon) applyExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	extensionsEmpty := len(oldConfig.Spec.Extensions) == 0 && len(newConfig.Spec.Extensions) == 0
	if (extensionsEmpty) ||
		(reflect.DeepEqual(oldConfig.Spec.Extensions, newConfig.Spec.Extensions) && oldConfig.Spec.OSImageURL == newConfig.Spec.OSImageURL) {
		return nil
	}

	// Validate extensions allowlist on RHCOS nodes
	if err := validateExtensions(newConfig.Spec.Extensions); err != nil && dn.os.IsEL() {
		return err
	}

	args := dn.generateExtensionsArgs(oldConfig, newConfig)
	glog.Infof("Applying extensions : %+q", args)
	return runRpmOstree(args...)
}

// switchKernel updates kernel on host with the kernelType specified in MachineConfig.
// Right now it supports default (traditional) and realtime kernel
func (dn *CoreOSDaemon) switchKernel(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// We support Kernel update only on RHCOS and SCOS nodes
	if !dn.os.IsEL() {
		glog.Info("updating kernel on non-RHCOS nodes is not supported")
		return nil
	}

	oldKtype := canonicalizeKernelType(oldConfig.Spec.KernelType)
	newKtype := canonicalizeKernelType(newConfig.Spec.KernelType)

	// In the OS update path, we removed overrides for kernel-rt.  So if the target (new) config
	// is also default (i.e. throughput) then we have nothing to do.
	if newKtype == ctrlcommon.KernelTypeDefault {
		return nil
	}

	// TODO: Drop this code and use https://github.com/coreos/rpm-ostree/issues/2542 instead
	defaultKernel := []string{"kernel", "kernel-core", "kernel-modules", "kernel-modules-core", "kernel-modules-extra"}
	// Note this list explicitly does *not* include kernel-rt as that is a meta-package that tries to pull in a lot
	// of other dependencies we don't want for historical reasons.
	// kernel-rt also has a split off kernel-rt-kvm subpackage because it's in a separate subscription in RHEL.
	realtimeKernel := []string{"kernel-rt-core", "kernel-rt-modules", "kernel-rt-modules-extra", "kernel-rt-kvm"}

	if oldKtype != newKtype {
		dn.logSystem("Initiating switch to kernel %s", newKtype)
	} else {
		dn.logSystem("Re-applying kernel type %s", newKtype)
	}

	if newKtype == ctrlcommon.KernelTypeRealtime {
		// Switch to RT kernel
		args := []string{"override", "remove"}
		args = append(args, defaultKernel...)
		for _, pkg := range realtimeKernel {
			args = append(args, "--install", pkg)
		}

		return runRpmOstree(args...)
	}
	return fmt.Errorf("Unhandled kernel type %s", newKtype)
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
func (dn *Daemon) updateFiles(oldIgnConfig, newIgnConfig ign3types.Config) error {
	glog.Info("Updating files")
	if err := dn.writeFiles(newIgnConfig.Storage.Files); err != nil {
		return err
	}
	if err := dn.writeUnits(newIgnConfig.Systemd.Units); err != nil {
		return err
	}
	if err := dn.deleteStaleData(oldIgnConfig, newIgnConfig); err != nil {
		return err
	}
	return nil
}

func restorePath(path string) error {
	if out, err := exec.Command("cp", "-a", "--reflink=auto", origFileName(path), path).CombinedOutput(); err != nil {
		return fmt.Errorf("restoring %q from orig file %q: %s: %w", path, origFileName(path), string(out), err)
	}
	if err := os.Remove(origFileName(path)); err != nil {
		return fmt.Errorf("deleting orig file %q: %w", origFileName(path), err)
	}
	return nil
}

// parse path to find out if its a systemd dropin
// Returns is dropin (true/false), service name, dropin name
func isPathASystemdDropin(path string) (bool, string, string) {
	if !strings.HasPrefix(path, "/etc/systemd/system") {
		return false, "", ""
	}
	if !strings.HasSuffix(path, ".conf") {
		return false, "", ""
	}
	pathSegments := strings.Split(path, "/")
	dropinName := pathSegments[len(pathSegments)-1]
	servicePart := pathSegments[len(pathSegments)-2]
	allServiceSegments := strings.Split(servicePart, ".")
	if allServiceSegments[len(allServiceSegments)-1] != "d" {
		return false, "", ""
	}
	serviceName := strings.Join(allServiceSegments[:len(allServiceSegments)-1], ".")
	return true, serviceName, dropinName
}

// iterate systemd units and return true if this path is already covered by a systemd dropin
func (dn *Daemon) isPathInDropins(path string, systemd *ign3types.Systemd) bool {
	if ok, service, dropin := isPathASystemdDropin(path); ok {
		for _, u := range systemd.Units {
			if u.Name == service {
				for _, j := range u.Dropins {
					if j.Name == dropin {
						return true
					}
				}
			}
		}
	}
	return false
}

// deleteStaleData performs a diff of the new and the old Ignition config. It then deletes
// all the files, units that are present in the old config but not in the new one.
// this function will error out if it fails to delete a file (with the exception
// of simply warning if the error is ENOENT since that's the desired state).
//
//nolint:gocyclo
func (dn *Daemon) deleteStaleData(oldIgnConfig, newIgnConfig ign3types.Config) error {
	glog.Info("Deleting stale data")
	newFileSet := make(map[string]struct{})
	for _, f := range newIgnConfig.Storage.Files {
		newFileSet[f.Path] = struct{}{}
	}

	for _, f := range oldIgnConfig.Storage.Files {
		if _, ok := newFileSet[f.Path]; ok {
			continue
		}
		if _, err := os.Stat(noOrigFileStampName(f.Path)); err == nil {
			if delErr := os.Remove(noOrigFileStampName(f.Path)); delErr != nil {
				return fmt.Errorf("deleting noorig file stamp %q: %w", noOrigFileStampName(f.Path), delErr)
			}
			glog.V(2).Infof("Removing file %q completely", f.Path)
		} else if _, err := os.Stat(origFileName(f.Path)); err == nil {
			// Add a check for backwards compatibility: basically if the file doesn't exist in /usr/etc (on FCOS/RHCOS)
			// and no rpm is claiming it, we assume that the orig file came from a wrongful backup of a MachineConfig
			// file instead of a file originally on disk. See https://bugzilla.redhat.com/show_bug.cgi?id=1814397
			var restore bool
			if _, err := exec.Command("rpm", "-qf", f.Path).CombinedOutput(); err == nil {
				// File is owned by an rpm
				restore = true
			} else if strings.HasPrefix(f.Path, "/etc") && dn.os.IsCoreOSVariant() {
				if _, err := os.Stat(withUsrPath(f.Path)); err != nil {
					if !os.IsNotExist(err) {
						return err
					}

					// If the error is ErrNotExist then we don't restore the file
				} else {
					restore = true
				}
			}

			if restore {
				if err := restorePath(f.Path); err != nil {
					return err
				}
				glog.V(2).Infof("Restored file %q", f.Path)
				continue
			}

			if delErr := os.Remove(origFileName(f.Path)); delErr != nil {
				return fmt.Errorf("deleting orig file %q: %w", origFileName(f.Path), delErr)
			}
		}

		// Check Systemd.Units.Dropins - don't remove the file if configuration has been converted into a dropin
		if dn.isPathInDropins(f.Path, &newIgnConfig.Systemd) {
			glog.Infof("Not removing file %q: replaced with systemd dropin", f.Path)
			continue
		}

		glog.V(2).Infof("Deleting stale config file: %s", f.Path)
		if err := os.Remove(f.Path); err != nil {
			newErr := fmt.Errorf("unable to delete %s: %w", f.Path, err)
			if !os.IsNotExist(err) {
				return newErr
			}
			// otherwise, just warn
			glog.Warningf("%v", newErr)
		}
		glog.Infof("Removed stale file %q", f.Path)
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
				if _, err := os.Stat(noOrigFileStampName(path)); err == nil {
					if delErr := os.Remove(noOrigFileStampName(path)); delErr != nil {
						return fmt.Errorf("deleting noorig file stamp %q: %w", noOrigFileStampName(path), delErr)
					}
					glog.V(2).Infof("Removing file %q completely", path)
				} else if _, err := os.Stat(origFileName(path)); err == nil {
					if err := restorePath(path); err != nil {
						return err
					}
					glog.V(2).Infof("Restored file %q", path)
					continue
				}
				glog.V(2).Infof("Deleting stale systemd dropin file: %s", path)
				if err := os.Remove(path); err != nil {
					newErr := fmt.Errorf("unable to delete %s: %w", path, err)
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
			// since the unit doesn't exist anymore within the MachineConfig,
			// look to restore defaults here, so that symlinks are removed first
			// if the system has the service disabled
			// writeUnits() will catch units that still have references in other MCs
			if err := dn.presetUnit(u); err != nil {
				glog.Infof("Did not restore preset for %s (may not exist): %s", u.Name, err)
			}
			if _, err := os.Stat(noOrigFileStampName(path)); err == nil {
				if delErr := os.Remove(noOrigFileStampName(path)); delErr != nil {
					return fmt.Errorf("deleting noorig file stamp %q: %w", noOrigFileStampName(path), delErr)
				}
				glog.V(2).Infof("Removing file %q completely", path)
			} else if _, err := os.Stat(origFileName(path)); err == nil {
				if err := restorePath(path); err != nil {
					return err
				}
				glog.V(2).Infof("Restored file %q", path)
				continue
			}
			glog.V(2).Infof("Deleting stale systemd unit file: %s", path)
			if err := os.Remove(path); err != nil {
				newErr := fmt.Errorf("unable to delete %s: %w", path, err)
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

// enableUnits enables a set of systemd units via systemctl, if any fail all fails.
func (dn *Daemon) enableUnits(units []string) error {
	args := append([]string{"enable"}, units...)
	stdouterr, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		if !dn.os.IsLikeTraditionalRHEL7() {
			return fmt.Errorf("error enabling units: %s", stdouterr)
		}
		// In RHEL7, the systemd version is too low, so it is unable to handle broken
		// symlinks during enable. Do a best-effort removal of potentially broken
		// hard coded symlinks and try again.
		// See: https://bugzilla.redhat.com/show_bug.cgi?id=1913536
		wantsPathSystemd := "/etc/systemd/system/multi-user.target.wants/"
		for _, unit := range units {
			unitLinkPath := filepath.Join(wantsPathSystemd, unit)
			fi, fiErr := os.Lstat(unitLinkPath)
			if fiErr != nil {
				if !os.IsNotExist(fiErr) {
					return fmt.Errorf("error trying to enable unit, fallback failed with %s (original error %s)",
						fiErr, stdouterr)
				}
				continue
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("error trying to enable unit, a non-symlink file exists at %s (original error %s)",
					unitLinkPath, stdouterr)
			}
			if _, evalErr := filepath.EvalSymlinks(unitLinkPath); evalErr != nil {
				// this is a broken symlink, remove
				if rmErr := os.Remove(unitLinkPath); rmErr != nil {
					return fmt.Errorf("error trying to enable unit, cannot remove broken symlink: %s (original error %s)",
						rmErr, stdouterr)
				}
			}
		}
		stdouterr, err := exec.Command("systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("error enabling units: %s", stdouterr)
		}
	}
	glog.Infof("Enabled systemd units: %v", units)
	return nil
}

// disableUnits disables a set of systemd units via systemctl, if any fail all fails.
func (dn *Daemon) disableUnits(units []string) error {
	args := append([]string{"disable"}, units...)
	stdouterr, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("error disabling unit: %s", stdouterr)
	}
	glog.Infof("Disabled systemd units %v", units)
	return nil
}

// presetUnit resets a systemd unit to its preset via systemctl
func (dn *Daemon) presetUnit(unit ign3types.Unit) error {
	args := []string{"preset", unit.Name}
	stdouterr, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("error running preset on unit: %s", stdouterr)
	}
	glog.Infof("Preset systemd unit %s", unit.Name)
	return nil
}

// writeUnits writes the systemd units to disk
func (dn *Daemon) writeUnits(units []ign3types.Unit) error {
	var enabledUnits []string
	var disabledUnits []string

	isCoreOSVariant := dn.os.IsCoreOSVariant()

	for _, u := range units {
		if err := writeUnit(u, pathSystemd, isCoreOSVariant); err != nil {
			return fmt.Errorf("daemon could not write systemd unit: %w", err)
		}
		// if the unit doesn't note if it should be enabled or disabled then
		// honour system presets. This to account for an edge case where you
		// deleted a MachineConfig that enabled/disabled the unit to revert,
		// but the unit itself is referenced in other MCs. deleteStaleData() will
		// catch fully deleted units.
		// if the unit should be enabled/disabled, then enable/disable it.
		// this is a no-op if the system wasn't change this iteration
		// Also, enable and disable as one command, as if any operation fails
		// we'd bubble up the error anyways, and we save a lot of time doing this.
		// Presets must be done individually as we don't consider a failed preset
		// as an error, but it would cause other presets that would have succeeded
		// to not go through.

		if u.Enabled != nil {
			if *u.Enabled {
				enabledUnits = append(enabledUnits, u.Name)
			} else {
				disabledUnits = append(disabledUnits, u.Name)
			}
		} else {
			if err := dn.presetUnit(u); err != nil {
				// Don't fail here, since a unit may have a dropin referencing a nonexisting actual unit
				glog.Infof("Could not reset unit preset for %s, skipping. (Error msg: %v)", u.Name, err)
			}
		}
	}

	if len(enabledUnits) > 0 {
		if err := dn.enableUnits(enabledUnits); err != nil {
			return err
		}
	}
	if len(disabledUnits) > 0 {
		if err := dn.disableUnits(disabledUnits); err != nil {
			return err
		}
	}
	return nil
}

// writeFiles writes the given files to disk.
// it doesn't fetch remote files and expects a flattened config file.
func (dn *Daemon) writeFiles(files []ign3types.File) error {
	return writeFiles(files)
}

// Ensures that both the SSH root directory (/home/core/.ssh) as well as any
// subdirectories are created with the correct (0700) permissions.
func createSSHKeyDir(authKeyDir string) error {
	glog.Infof("Creating missing SSH key dir at %s", authKeyDir)

	mkdir := func(dir string) error {
		return exec.Command("runuser", "-u", constants.CoreUserName, "--", "mkdir", "-m", "0700", "-p", dir).Run()
	}

	// Create the root SSH key directory (/home/core/.ssh) first.
	if err := mkdir(filepath.Dir(constants.RHCOS8SSHKeyPath)); err != nil {
		return err
	}

	// For RHCOS 8, creating /home/core/.ssh is all that is needed.
	if authKeyDir == constants.RHCOS8SSHKeyPath {
		return nil
	}

	// Create the next level of the SSH key directory (/home/core/.ssh/authorized_keys.d) for RHCOS 9 cases.
	return mkdir(filepath.Dir(constants.RHCOS9SSHKeyPath))
}

func (dn *Daemon) atomicallyWriteSSHKey(authKeyPath, keys string) error {
	uid, err := lookupUID(constants.CoreUserName)
	if err != nil {
		return err
	}

	gid, err := lookupGID(constants.CoreGroupName)
	if err != nil {
		return err
	}

	// Keys should only be written to "/home/core/.ssh"
	// Once Users are supported fully this should be writing to PasswdUser.HomeDir
	glog.Infof("Writing SSH keys to %q", authKeyPath)

	// Creating CoreUserSSHPath in advance if it doesn't exist in order to ensure it is owned by core user
	// See https://bugzilla.redhat.com/show_bug.cgi?id=2107113
	authKeyDir := filepath.Dir(authKeyPath)
	if _, err := os.Stat(authKeyDir); os.IsNotExist(err) {
		if err := createSSHKeyDir(authKeyDir); err != nil {
			return err
		}
	}

	if err := writeFileAtomically(authKeyPath, []byte(keys), os.FileMode(0o700), os.FileMode(0o600), uid, gid); err != nil {
		return err
	}

	glog.V(2).Infof("Wrote SSH keys to %q", authKeyPath)

	return nil
}

// Set a given PasswdUser's Password Hash
func (dn *Daemon) SetPasswordHash(newUsers []ign3types.PasswdUser) error {
	// confirm that user exits
	if len(newUsers) == 0 {
		return nil
	}

	var uErr user.UnknownUserError
	switch _, err := user.Lookup(constants.CoreUserName); {
	case err == nil:
	case errors.As(err, &uErr):
		glog.Info("core user does not exist, and creating users is not supported, so ignoring configuration specified for core user")
		return nil
	default:
		return fmt.Errorf("failed to check if user core exists: %w", err)
	}

	// SetPasswordHash sets the password hash of the specified user.
	for _, u := range newUsers {
		pwhash := "*"
		if u.PasswordHash != nil && *u.PasswordHash != "" {
			pwhash = *u.PasswordHash
		}

		if out, err := exec.Command("usermod", "-p", pwhash, u.Name).CombinedOutput(); err != nil {
			return fmt.Errorf("Failed to change password for %s: %s:%w", u.Name, out, err)
		}
		glog.Info("Password has been configured")
	}

	return nil
}

// Determines if we should use the new SSH key path
// (/home/core/.ssh/authorized_keys.d/ignition) or the old SSH key path
// (/home/core/.ssh/authorized_keys)
func (dn *Daemon) useNewSSHKeyPath() bool {
	return dn.os.IsEL9() || dn.os.IsFCOS() || dn.os.IsSCOS()
}

// Update a given PasswdUser's SSHKey
func (dn *Daemon) updateSSHKeys(newUsers []ign3types.PasswdUser) error {
	if len(newUsers) == 0 {
		return nil
	}

	var uErr user.UnknownUserError
	switch _, err := user.Lookup(constants.CoreUserName); {
	case err == nil:
	case errors.As(err, &uErr):
		glog.Info("core user does not exist, and creating users is not supported, so ignoring configuration specified for core user")
		return nil
	default:
		return fmt.Errorf("failed to check if user core exists: %w", err)
	}

	// we're also appending all keys for any user to core, so for now
	// we pass this to atomicallyWriteSSHKeys to write.
	// we know these users are "core" ones also cause this slice went through Reconcilable
	var concatSSHKeys string
	for _, u := range newUsers {
		for _, k := range u.SSHAuthorizedKeys {
			concatSSHKeys = concatSSHKeys + string(k) + "\n"
		}
	}

	authKeyPath := constants.RHCOS8SSHKeyPath

	if !dn.mock {
		// In RHCOS 8.6 or lower, the keys were written to `/home/core/.ssh/authorized_keys`.
		// RHCOS 9.0+, FCOS, and SCOS will however expect the keys at `/home/core/.ssh/authorized_keys.d/ignition`.
		// Check if the authorized_keys file at the legacy path exists. If it does, remove it.
		// It will be recreated at the new fragment path by the atomicallyWriteSSHKey function
		// that is called right after.
		if dn.useNewSSHKeyPath() {
			authKeyPath = constants.RHCOS9SSHKeyPath

			if err := cleanSSHKeyPaths(); err != nil {
				return err
			}

			if err := removeNonIgnitionKeyPathFragments(); err != nil {
				return err
			}
		}

		// Note we write keys only for the core user and so this ignores the user list
		return dn.atomicallyWriteSSHKey(authKeyPath, concatSSHKeys)
	}

	return nil
}

// Determines if a file exists by checking for the presence or lack thereof of
// an error when stat'ing the file. Returns any other error.
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	// If there is no error, the file definitely exists.
	if err == nil {
		return true, nil
	}

	// If the error matches fs.ErrNotExist, the file definitely does not exist.
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}

	// An unexpected error occurred.
	return false, fmt.Errorf("cannot stat file: %w", err)
}

// Removes the old SSH key path (/home/core/.ssh/authorized_keys), if found.
func cleanSSHKeyPaths() error {
	oldKeyExists, err := fileExists(constants.RHCOS8SSHKeyPath)
	if err != nil {
		return err
	}

	if !oldKeyExists {
		return nil
	}

	if err := os.RemoveAll(constants.RHCOS8SSHKeyPath); err != nil {
		return fmt.Errorf("failed to remove path '%s': %w", constants.RHCOS8SSHKeyPath, err)
	}

	return nil
}

// Ensures authorized_keys.d/ignition is the only fragment that exists within the /home/core/.ssh dir.
func removeNonIgnitionKeyPathFragments() error {
	// /home/core/.ssh/authorized_keys.d
	authKeyFragmentDirPath := filepath.Dir(constants.RHCOS9SSHKeyPath)
	// ignition
	authKeyFragmentBasename := filepath.Base(constants.RHCOS9SSHKeyPath)

	keyFragmentsDir, err := ctrlcommon.ReadDir(authKeyFragmentDirPath)
	if err == nil {
		for _, fragment := range keyFragmentsDir {
			if fragment.Name() != authKeyFragmentBasename {
				keyPath := filepath.Join(authKeyFragmentDirPath, fragment.Name())
				err := os.RemoveAll(keyPath)
				if err != nil {
					return fmt.Errorf("failed to remove path '%s': %w", keyPath, err)
				}
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		// This shouldn't ever happen
		return fmt.Errorf("unexpectedly failed to get info for path '%s': %w", constants.RHCOS9SSHKeyPath, err)
	}

	return nil
}

// updateOS updates the system OS to the one specified in newConfig
func (dn *Daemon) updateOS(config *mcfgv1.MachineConfig, osImageContentDir string) error {
	newURL := config.Spec.OSImageURL
	glog.Infof("Updating OS to %s", newURL)
	if _, err := dn.NodeUpdaterClient.Rebase(newURL, osImageContentDir); err != nil {
		return fmt.Errorf("failed to update OS to %s : %w", newURL, err)
	}

	return nil
}

// InplaceUpdateViaNewContainer runs rpm-ostree ex deploy-via-self
// via a privileged container.  This is needed on firstboot of old
// nodes as well as temporarily for 4.11 -> 4.12 upgrades.
func (dn *Daemon) InplaceUpdateViaNewContainer(target string) error {
	// HACK: Disable selinux enforcement for this because it's not
	// really easily possible to get the correct install_t context
	// here when run from a container image.
	// xref https://issues.redhat.com/browse/MCO-396
	enforceFile := "/sys/fs/selinux/enforce"
	enforcingBuf, err := os.ReadFile(enforceFile)
	var enforcing bool
	if err != nil {
		if os.IsNotExist(err) {
			enforcing = false
		} else {
			return fmt.Errorf("failed to read %s: %w", enforceFile, err)
		}
	} else {
		enforcingStr := string(enforcingBuf)
		v, err := strconv.Atoi(strings.TrimSpace(enforcingStr))
		if err != nil {
			return fmt.Errorf("failed to parse selinux enforcing %v: %w", enforcingBuf, err)
		}
		enforcing = (v == 1)
	}
	if enforcing {
		if err := runCmdSync("setenforce", "0"); err != nil {
			return err
		}
	} else {
		glog.Info("SELinux is not enforcing")
	}

	systemdPodmanArgs := []string{"--unit", "machine-config-daemon-update-rpmostree-via-container", "-p", "EnvironmentFile=-/etc/mco/proxy.env", "--collect", "--wait", "--", "podman"}
	pullArgs := append([]string{}, systemdPodmanArgs...)
	pullArgs = append(pullArgs, "pull", "--authfile", "/var/lib/kubelet/config.json", target)
	err = runCmdSync("systemd-run", pullArgs...)
	if err != nil {
		return err
	}

	runArgs := append([]string{}, systemdPodmanArgs...)
	runArgs = append(runArgs, "run", "--env-file", "/etc/mco/proxy.env", "--authfile", "/var/lib/kubelet/config.json", "--privileged", "--pid=host", "--net=host", "--rm", "-v", "/:/run/host", target, "rpm-ostree", "ex", "deploy-from-self", "/run/host")
	err = runCmdSync("systemd-run", runArgs...)
	if err != nil {
		return err
	}
	if enforcing {
		if err := runCmdSync("setenforce", "1"); err != nil {
			return err
		}
	}
	return nil
}

// queueRevertRTKernel undoes the layering of the RT kernel
func (dn *Daemon) queueRevertRTKernel() error {
	booted, _, err := dn.NodeUpdaterClient.GetBootedAndStagedDeployment()
	if err != nil {
		return err
	}

	// Before we attempt to do an OS update, we must remove the kernel-rt switch
	// because in the case of updating from RHEL8 to RHEL9, the kernel packages are
	// OS version dependent.  See also https://github.com/coreos/rpm-ostree/issues/2542
	// (Now really what we want to do here is something more like rpm-ostree override reset --kernel
	//  i.e. the inverse of https://github.com/coreos/rpm-ostree/pull/4322 so that
	//  we're again not hardcoding even the prefix of kernel packages)
	kernelOverrides := []string{}
	kernelRtLayers := []string{}
	for _, removal := range booted.RequestedBaseRemovals {
		if removal == "kernel" || strings.HasPrefix(removal, "kernel-") {
			kernelOverrides = append(kernelOverrides, removal)
		}
	}
	for _, pkg := range booted.RequestedPackages {
		if strings.HasPrefix(pkg, "kernel-rt-") {
			kernelRtLayers = append(kernelRtLayers, pkg)
		}
	}
	// We *only* do this switch if the node has done a switch from kernel -> kernel-rt.
	// We don't want to override any machine-local hotfixes for the kernel package.
	// Implicitly in this we don't really support machine-local hotfixes for kernel-rt.
	// The only sane way to handle that is declarative drop-ins, but really we want to
	// just go to deploying pre-built images and not doing per-node mutation with rpm-ostree
	// at all.
	if len(kernelOverrides) > 0 && len(kernelRtLayers) > 0 {
		args := []string{"override", "reset"}
		args = append(args, kernelOverrides...)
		for _, pkg := range kernelRtLayers {
			args = append(args, "--uninstall", pkg)
		}
		err := runRpmOstree(args...)
		if err != nil {
			return err
		}
	} else if len(kernelOverrides) > 0 || len(kernelRtLayers) > 0 {
		glog.Infof("notice: detected %d overrides and %d kernel-rt layers", len(kernelOverrides), len(kernelRtLayers))
	} else {
		glog.Infof("No kernel overrides or replacement detected")
	}

	return nil
}

// updateLayeredOS updates the system OS to the one specified in newConfig
func (dn *Daemon) updateLayeredOS(config *mcfgv1.MachineConfig) error {
	newURL := config.Spec.OSImageURL
	glog.Infof("Updating OS to layered image %s", newURL)

	newEnough, err := RpmOstreeIsNewEnoughForLayering()
	if err != nil {
		return err
	}
	// If the host isn't new enough to understand the new container model natively, run as a privileged container.
	// See https://github.com/coreos/rpm-ostree/pull/3961 and https://issues.redhat.com/browse/MCO-356
	// This currently will incur a double reboot; see https://github.com/coreos/rpm-ostree/issues/4018
	if !newEnough {
		dn.logSystem("rpm-ostree is not new enough for layering; forcing an update via container")
		if err := dn.InplaceUpdateViaNewContainer(newURL); err != nil {
			return err
		}
	} else if err := dn.NodeUpdaterClient.RebaseLayered(newURL); err != nil {
		return fmt.Errorf("failed to update OS to %s : %w", newURL, err)
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
		return nil, fmt.Errorf("failed shelling out to journalctl -o cat: %w", err)
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
					errs := kubeErrs.NewAggregate([]error{exiterr, fmt.Errorf("grep exited with %s", combinedOutput.Bytes())})
					return nil, fmt.Errorf("failed to grep on journal output: %w", errs)
				}
			}
		} else {
			return nil, fmt.Errorf("command wait error: %w", err)
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
		return nil, fmt.Errorf("getting pending state from journal: %w", err)
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
		return nil, fmt.Errorf("error running journalctl -o json: %w", err)
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

// Synchronously invoke a command, writing its stdout to our stdout,
// and gathering stderr into a buffer which will be returned in err
// in case of error.
func runCmdSync(cmdName string, args ...string) error {
	glog.Infof("Running: %s %s", cmdName, strings.Join(args, " "))
	cmd := exec.Command(cmdName, args...)
	var stderr bytes.Buffer
	cmd.Stdout = os.Stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running %s %s: %s: %w", cmdName, strings.Join(args, " "), string(stderr.Bytes()), err)
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
	glog.Info("Adding SIGTERM protection")
	dn.updateActive = true
}

func (dn *Daemon) cancelSIGTERM() {
	dn.updateActiveLock.Lock()
	defer dn.updateActiveLock.Unlock()
	if dn.updateActive {
		glog.Info("Removing SIGTERM protection")
		dn.updateActive = false
	}
}

// reboot is the final step. it tells systemd-logind to reboot the machine,
// cleans up the agent's connections
// on failure to reboot, it throws an error and waits for the operator to try again
func (dn *Daemon) reboot(rationale string) error {
	// Now that everything is done, avoid delaying shutdown.
	dn.cancelSIGTERM()
	dn.Close()

	if dn.skipReboot {
		return nil
	}

	// We'll only have a recorder if we're cluster driven
	if dn.nodeWriter != nil {
		dn.nodeWriter.Eventf(corev1.EventTypeNormal, "Reboot", rationale)
	}
	dn.logSystem("initiating reboot: %s", rationale)

	// reboot, executed async via systemd-run so that the reboot command is executed
	// in the context of the host asynchronously from us
	// We're not returning the error from the reboot command as it can be terminated by
	// the system itself with signal: terminated. We can't catch the subprocess termination signal
	// either, we just have one for the MCD itself.
	rebootCmd := rebootCommand(rationale)
	if err := rebootCmd.Run(); err != nil {
		dn.logSystem("failed to run reboot: %v", err)
		mcdRebootErr.Inc()
		return fmt.Errorf("reboot command failed, something is seriously wrong")
	}
	// if we're here, reboot went through successfully, so we set rebootQueued
	// and we wait for GracefulNodeShutdown
	dn.rebootQueued = true
	dn.logSystem("reboot successful")
	return nil
}

func (dn *CoreOSDaemon) applyLayeredOSChanges(mcDiff machineConfigDiff, oldConfig, newConfig *mcfgv1.MachineConfig) (retErr error) {
	// Override the computed diff if the booted state differs from the oldConfig
	// https://issues.redhat.com/browse/OCPBUGS-2757
	if mcDiff.osUpdate && dn.bootedOSImageURL == newConfig.Spec.OSImageURL {
		glog.Infof("Already in desired image %s", newConfig.Spec.OSImageURL)
		mcDiff.osUpdate = false
	}

	var osExtensionsContentDir string
	var err error
	if newConfig.Spec.BaseOSExtensionsContainerImage != "" && (mcDiff.osUpdate || mcDiff.extensions || mcDiff.kernelType) {

		// TODO(jkyros): the original intent was that we use the extensions container as a service, but that currently results
		// in a lot of complexity due to boostrap and firstboot where the service isn't easily available, so for now we are going
		// to extract them to disk like we did previously.
		if osExtensionsContentDir, err = ExtractExtensionsImage(newConfig.Spec.BaseOSExtensionsContainerImage); err != nil {
			return err
		}
		// Delete extracted OS image once we are done.
		defer os.RemoveAll(osExtensionsContentDir)

		if err := addLayeredExtensionsRepo(osExtensionsContentDir); err != nil {
			return err
		}
		defer os.Remove(extensionsRepo)
	}

	// If we have an OS update *or* a kernel type change, then we must undo the RT kernel
	// enablement.
	if mcDiff.osUpdate || mcDiff.kernelType {
		if err := dn.queueRevertRTKernel(); err != nil {
			mcdPivotErr.Inc()
			return err
		}
	}

	// Update OS
	if mcDiff.osUpdate {
		if err := dn.updateLayeredOS(newConfig); err != nil {
			mcdPivotErr.Inc()
			return err
		}
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "OSUpgradeApplied", "OS upgrade applied; new MachineConfig (%s) has new OS image (%s)", newConfig.Name, newConfig.Spec.OSImageURL)
		}
	} else { //nolint:gocritic // The nil check for dn.nodeWriter has nothing to do with an OS update being unavailable.
		// An OS upgrade is not available
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "OSUpgradeSkipped", "OS upgrade skipped; new MachineConfig (%s) has same OS image (%s) as old MachineConfig (%s)", newConfig.Name, newConfig.Spec.OSImageURL, oldConfig.Name)
		}
	}

	// if we're here, we've successfully pivoted, or pivoting wasn't necessary, so we reset the error gauge
	mcdPivotErr.Set(0)

	defer func() {
		// Operations performed by rpm-ostree on the booted system are available
		// as staged deployment. It gets applied only when we reboot the system.
		// In case of an error during any rpm-ostree transaction, removing pending deployment
		// should be sufficient to discard any applied changes.
		if retErr != nil {
			// Print out the error now so that if we fail to cleanup -p, we don't lose it.
			glog.Infof("Rolling back applied changes to OS due to error: %v", retErr)
			if err := removePendingDeployment(); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error removing staged deployment: %w", errs)
				return
			}
		}
	}()

	if mcDiff.kargs {
		if err := dn.updateKernelArguments(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments); err != nil {
			return err
		}
	}

	// Switch to real time kernel
	if err := dn.switchKernel(oldConfig, newConfig); err != nil {
		return err
	}

	// Apply extensions
	if err := dn.applyExtensions(oldConfig, newConfig); err != nil {
		return err
	}

	return nil
}

func (dn *CoreOSDaemon) applyLegacyOSChanges(mcDiff machineConfigDiff, oldConfig, newConfig *mcfgv1.MachineConfig) (retErr error) {
	var osImageContentDir string
	var err error
	if mcDiff.osUpdate || mcDiff.extensions || mcDiff.kernelType {

		if osImageContentDir, err = ExtractOSImage(newConfig.Spec.OSImageURL); err != nil {
			return err
		}
		// Delete extracted OS image once we are done.
		defer os.RemoveAll(osImageContentDir)

		if err := addExtensionsRepo(osImageContentDir); err != nil {
			return err
		}
		defer os.Remove(extensionsRepo)
	}

	// Update OS
	if mcDiff.osUpdate {
		if err := dn.updateOS(newConfig, osImageContentDir); err != nil {
			mcdPivotErr.Inc()
			return err
		}
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "OSUpgradeApplied", "OS upgrade applied; new MachineConfig (%s) has new OS image (%s)", newConfig.Name, newConfig.Spec.OSImageURL)
		}
	} else { //nolint:gocritic // The nil check for dn.nodeWriter has nothing to do with an OS update being unavailable.
		// An OS upgrade is not available
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "OSUpgradeSkipped", "OS upgrade skipped; new MachineConfig (%s) has same OS image (%s) as old MachineConfig (%s)", newConfig.Name, newConfig.Spec.OSImageURL, oldConfig.Name)
		}
	}

	// if we're here, we've successfully pivoted, or pivoting wasn't necessary, so we reset the error gauge
	mcdPivotErr.Set(0)

	defer func() {
		// Operations performed by rpm-ostree on the booted system are available
		// as staged deployment. It gets applied only when we reboot the system.
		// In case of an error during any rpm-ostree transaction, removing pending deployment
		// should be sufficient to discard any applied changes.
		if retErr != nil {
			// Print out the error now so that if we fail to cleanup -p, we don't lose it.
			glog.Infof("Rolling back applied changes to OS due to error: %v", retErr)
			if err := removePendingDeployment(); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error removing staged deployment: %w", errs)
				return
			}
		}
	}()

	// Apply kargs
	if mcDiff.kargs {
		if err := dn.updateKernelArguments(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments); err != nil {
			return err
		}
	}

	// Switch to real time kernel
	if err := dn.switchKernel(oldConfig, newConfig); err != nil {
		return err
	}

	// Apply extensions
	if err := dn.applyExtensions(oldConfig, newConfig); err != nil {
		return err
	}

	return nil
}
