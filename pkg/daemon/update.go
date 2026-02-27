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
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/clarketm/json"
	"github.com/coreos/go-semver/semver"
	systemddbus "github.com/coreos/go-systemd/v22/dbus"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeErrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	opv1 "github.com/openshift/api/operator/v1"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	pivottypes "github.com/openshift/machine-config-operator/pkg/daemon/pivot/types"
	pivotutils "github.com/openshift/machine-config-operator/pkg/daemon/pivot/utils"
	"github.com/openshift/machine-config-operator/pkg/daemon/runtimeassets"
	"github.com/openshift/machine-config-operator/pkg/helpers"
	"github.com/openshift/machine-config-operator/pkg/upgrademonitor"
)

const (
	// defaultDirectoryPermissions houses the default mode to use when no directory permissions are provided
	defaultDirectoryPermissions os.FileMode = 0o755
	// defaultFilePermissions houses the default mode to use when no file permissions are provided
	defaultFilePermissions os.FileMode = 0o644
	// fipsFile is the file to check if FIPS is enabled
	fipsFile                   = "/proc/sys/crypto/fips_enabled"
	extensionsRepo             = "/etc/yum.repos.d/coreos-extensions.repo"
	osExtensionsContentBaseDir = "/run/mco-extensions/"

	// These are the actions for a node to take after applying config changes. (e.g. a new machineconfig is applied)
	// "None" means no special action needs to be taken
	// This happens for example when ssh keys or the pull secret (/var/lib/kubelet/config.json) is changed
	postConfigChangeActionNone = "none"
	// The "reload crio" action will run "systemctl reload crio"
	postConfigChangeActionReloadCrio = "reload crio"
	// The "restart crio" action will run "systemctl restart crio"
	postConfigChangeActionRestartCrio = "restart crio"
	// Rebooting is still the default scenario for any other change
	postConfigChangeActionReboot = "reboot"
)

// releaseKernelPackages contains the list of packages per kernel type a given OS release uses
type releaseKernelPackages struct {
	defaultKernel   []string
	realtimeKernel  []string
	hugePagesKernel []string
}

func getNodeRef(node *corev1.Node) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		Kind: "Node",
		Name: node.GetName(),
		UID:  node.GetUID(),
	}
}

func restartService(name string) error {
	return runCmdSync("systemctl", "restart", name)
}

func reloadService(name string) error {
	return runCmdSync("systemctl", "reload", name)
}

func reloadDaemon() error {
	return runCmdSync("systemctl", constants.DaemonReloadCommand)
}

func (dn *Daemon) finishRebootlessUpdate() error {
	// Get current state of node, in case of an error reboot
	state, err := dn.getStateAndConfigs()
	if err != nil {
		return fmt.Errorf("could not apply update: error processing state and configs. Error: %w", err)
	}

	var inDesiredConfig bool
	var missingODC bool
	if missingODC, inDesiredConfig, err = dn.updateConfigAndState(state); err != nil {
		return fmt.Errorf("could not apply update: setting node's state to Done failed. Error: %w", err)
	}

	if missingODC {
		return fmt.Errorf("error updating state.currentconfig from on-disk currentconfig")
	}

	if inDesiredConfig {
		// (re)start the config drift monitor since rebooting isn't needed.
		dn.startConfigDriftMonitor()
		return nil
	}

	// currentConfig != desiredConfig, kick off an update
	return dn.triggerUpdateWithMachineConfig(state.currentConfig, state.desiredConfig, true)
}

func (dn *Daemon) executeReloadServiceNodeDisruptionAction(serviceName string, reloadErr error) error {
	if reloadErr != nil {
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedServiceReload", fmt.Sprintf("Reloading service %s failed. Error: %v", serviceName, reloadErr))
		}
		return fmt.Errorf("could not apply update: reloading %s configuration failed. Error: %w", serviceName, reloadErr)
	}

	// Get MCP associated with node
	pool, err := helpers.GetPrimaryPoolNameForMCN(dn.mcpLister, dn.node)
	if err != nil {
		return err
	}

	err = upgrademonitor.GenerateAndApplyMachineConfigNodes(
		&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdatePostActionComplete, Reason: string(mcfgv1.MachineConfigNodeUpdatePostActionComplete), Message: fmt.Sprintf("Node has reloaded service %s", serviceName)},
		nil,
		metav1.ConditionTrue,
		metav1.ConditionFalse,
		dn.node,
		dn.mcfgClient,
		dn.fgHandler,
		pool,
	)
	if err != nil {
		klog.Errorf("Error making MCN for Reloading success: %v", err)
	}

	if dn.nodeWriter != nil {
		dn.nodeWriter.Eventf(corev1.EventTypeNormal, "ServiceReload", "Config changes do not require reboot. Service %s was reloaded.", serviceName)
	}
	logSystem("%s service reloaded successfully!", serviceName)
	return nil
}

// performPostConfigChangeNodeDisruptionAction takes action based on the cluster's Node disruption policies.
// For non-reboot action, it applies configuration, updates node's config and state.
// In the end uncordon node to schedule workload.
// If at any point an error occurs, we reboot the node so that node has correct configuration.
func (dn *Daemon) performPostConfigChangeNodeDisruptionAction(postConfigChangeActions []opv1.NodeDisruptionPolicyStatusAction, configName string) error {
	for _, action := range postConfigChangeActions {

		// Drain is already completed at this stage and essentially a no-op for this loop, so no need to log that.
		if action.Type == opv1.DrainStatusAction {
			continue
		}

		logSystem("Performing post config change action: %v for config %s", action.Type, configName)

		// Get MCP associated with node
		pool, err := helpers.GetPrimaryPoolNameForMCN(dn.mcpLister, dn.node)
		if err != nil {
			return err
		}

		switch action.Type {
		case opv1.RebootStatusAction:
			err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
				&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdateRebooted, Reason: string(mcfgv1.MachineConfigNodeUpdateRebooted), Message: "Upgrade requires a reboot."},
				nil,
				metav1.ConditionUnknown,
				metav1.ConditionFalse,
				dn.node,
				dn.mcfgClient,
				dn.fgHandler,
				pool,
			)
			if err != nil {
				klog.Errorf("Error making MCN for rebooting: %v", err)
			}
			logSystem("Rebooting node")
			return dn.reboot(fmt.Sprintf("Node will reboot into config %s", configName))

		case opv1.NoneStatusAction:
			if dn.nodeWriter != nil {
				dn.nodeWriter.Eventf(corev1.EventTypeNormal, "SkipReboot", "Config changes do not require reboot.")
			}
			err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
				&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdatePostActionComplete, Reason: "None", Message: "Changes do not require a reboot"},
				nil,
				metav1.ConditionTrue,
				metav1.ConditionFalse,
				dn.node,
				dn.mcfgClient,
				dn.fgHandler,
				pool,
			)
			if err != nil {
				klog.Errorf("Error making MCN for no post config change action: %v", err)
			}
			logSystem("Node has Desired Config %s, skipping reboot", configName)

		case opv1.RestartStatusAction:
			serviceName := string(action.Restart.ServiceName)

			if err := restartService(serviceName); err != nil {
				// On RHEL nodes, this service is not available and will error out.
				// In those cases, directly run the command instead of using the service
				if serviceName == constants.UpdateCATrustServiceName {
					logSystem("Error executing %s unit, falling back to command", serviceName)
					cmd := exec.Command(constants.UpdateCATrustCommand)
					var stderr bytes.Buffer
					cmd.Stdout = os.Stdout
					cmd.Stderr = &stderr
					if err := cmd.Run(); err != nil {
						if dn.nodeWriter != nil {
							dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedServiceRestart", fmt.Sprintf("Restarting %s service failed. Error: %v", serviceName, err))
						}
						return fmt.Errorf("error running %s: %s: %w", constants.UpdateCATrustCommand, stderr.String(), err)
					}
				} else {
					if dn.nodeWriter != nil {
						dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedServiceRestart", fmt.Sprintf("Restarting %s service failed. Error: %v", serviceName, err))
					}
					return fmt.Errorf("could not apply update: restarting %s service failed. Error: %w", serviceName, err)
				}
			}
			// TODO: Add a new MCN Condition to the API for service restarts?
			if dn.nodeWriter != nil {
				dn.nodeWriter.Eventf(corev1.EventTypeNormal, "ServiceRestart", "Config changes do not require reboot. Service %s was restarted.", serviceName)
			}
			logSystem("%s service restarted successfully!", serviceName)

		case opv1.ReloadStatusAction:
			// Execute a generic service reload defined by the action object
			serviceName := string(action.Reload.ServiceName)
			if err := dn.executeReloadServiceNodeDisruptionAction(serviceName, reloadService(serviceName)); err != nil {
				return err
			}

		case opv1.SpecialStatusAction:
			// The special action type requires a CRIO reload
			if err := dn.executeReloadServiceNodeDisruptionAction(constants.CRIOServiceName, reloadService(constants.CRIOServiceName)); err != nil {
				return err
			}

		case opv1.DaemonReloadStatusAction:
			// Execute daemon-reload
			if err := dn.executeReloadServiceNodeDisruptionAction(constants.DaemonReloadCommand, reloadDaemon()); err != nil {
				return err
			}
		}
	}

	// We are here, which means a reboot was not needed to apply the configuration.
	return dn.finishRebootlessUpdate()
}

// performPostConfigChangeAction takes action based on what postConfigChangeAction has been asked.
// For non-reboot action, it applies configuration, updates node's config and state.
// In the end uncordon node to schedule workload.
// If at any point an error occurs, we reboot the node so that node has correct configuration.
func (dn *Daemon) performPostConfigChangeAction(postConfigChangeActions []string, configName string) error {
	// Get MCP associated with node
	pool, err := helpers.GetPrimaryPoolNameForMCN(dn.mcpLister, dn.node)
	if err != nil {
		return err
	}

	if ctrlcommon.InSlice(postConfigChangeActionReboot, postConfigChangeActions) {
		err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
			&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdateRebooted, Reason: string(mcfgv1.MachineConfigNodeUpdateRebooted), Message: "Upgrade requires a reboot."},
			nil,
			metav1.ConditionUnknown,
			metav1.ConditionFalse,
			dn.node,
			dn.mcfgClient,
			dn.fgHandler,
			pool,
		)
		if err != nil {
			klog.Errorf("Error making MCN for rebooting: %v", err)
		}
		logSystem("Rebooting node")
		return dn.reboot(fmt.Sprintf("Node will reboot into config %s", configName))
	}

	if ctrlcommon.InSlice(postConfigChangeActionNone, postConfigChangeActions) {
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "SkipReboot", "Config changes do not require reboot.")
		}
		err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
			&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdatePostActionComplete, Reason: "None", Message: fmt.Sprintf("Changes do not require a reboot")},
			nil,
			metav1.ConditionTrue,
			metav1.ConditionFalse,
			dn.node,
			dn.mcfgClient,
			dn.fgHandler,
			pool,
		)
		if err != nil {
			klog.Errorf("Error making MCN for no post config change action: %v", err)
		}
		logSystem("Node has Desired Config %s, skipping reboot", configName)
	}

	if ctrlcommon.InSlice(postConfigChangeActionReloadCrio, postConfigChangeActions) {
		serviceName := constants.CRIOServiceName

		if err := reloadService(serviceName); err != nil {
			if dn.nodeWriter != nil {
				dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedServiceReload", fmt.Sprintf("Reloading %s service failed. Error: %v", serviceName, err))
			}
			return fmt.Errorf("could not apply update: reloading %s configuration failed. Error: %w", serviceName, err)
		}

		err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
			&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdatePostActionComplete, Reason: string(mcfgv1.MachineConfigNodeUpdatePostActionComplete), Message: "Node has reloaded CRIO"},
			nil,
			metav1.ConditionTrue,
			metav1.ConditionFalse,
			dn.node,
			dn.mcfgClient,
			dn.fgHandler,
			pool,
		)
		if err != nil {
			klog.Errorf("Error making MCN for Reloading success: %v", err)
		}

		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "SkipReboot", "Config changes do not require reboot. Service %s was reloaded.", serviceName)
		}
		logSystem("%s config reloaded successfully! Desired config %s has been applied, skipping reboot", serviceName, configName)
	}

	if ctrlcommon.InSlice(postConfigChangeActionRestartCrio, postConfigChangeActions) {
		cmd := exec.Command(constants.UpdateCATrustCommand)
		var stderr bytes.Buffer
		cmd.Stdout = os.Stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("error running %s: %s: %w", constants.UpdateCATrustCommand, string(stderr.Bytes()), err)
		}

		serviceName := constants.CRIOServiceName

		if err := restartService(serviceName); err != nil {
			if dn.nodeWriter != nil {
				dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedServiceReload", fmt.Sprintf("Reloading %s service failed. Error: %v", serviceName, err))
			}
			return fmt.Errorf("could not apply update: reloading %s configuration failed. Error: %w", serviceName, err)
		}
		logSystem("%s config restarted successfully! Desired config %s has been applied, skipping reboot", serviceName, configName)

	}

	// We are here, which means a reboot was not needed to apply the configuration.
	return dn.finishRebootlessUpdate()
}

func setRunningKargsWithCmdline(config *mcfgv1.MachineConfig, requestedKargs []string, cmdline []byte) error {
	splits := splitKernelArguments(strings.TrimSpace(string(cmdline)))
	config.Spec.KernelArguments = nil
	for _, split := range splits {
		for _, reqKarg := range requestedKargs {
			if reqKarg == split {
				config.Spec.KernelArguments = append(config.Spec.KernelArguments, reqKarg)
				break
			}
		}
	}
	return nil
}

func (dn *Daemon) setRunningKargs(config *mcfgv1.MachineConfig, requestedKargs []string) error {
	rpmostreeKargsBytes, err := dn.cmdRunner.RunGetOut("rpm-ostree", "kargs")
	if err != nil {
		return err
	}

	return setRunningKargsWithCmdline(config, requestedKargs, rpmostreeKargsBytes)
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
		logSystem("No changes from %s to %s", oldConfigName, newConfigName)
		return false, nil
	}
	logSystem("Changes detected from %s to %s: %+v", oldConfigName, newConfigName, mcDiff)
	return true, nil
}

// addExtensionsRepo adds a repo into /etc/yum.repos.d/ which we use later to
// install extensions (additional packages).
func addExtensionsRepo(extensionsImageContentDir string) error {
	repoContent := "[coreos-extensions]\nenabled=1\nmetadata_expire=1m\nbaseurl=" + extensionsImageContentDir + "/usr/share/rpm-ostree/extensions/\ngpgcheck=0\nskip_if_unavailable=False\n"
	return writeFileAtomicallyWithDefaults(extensionsRepo, []byte(repoContent))
}

// podmanRemove kills and removes a container
func podmanRemove(cid string) {
	// Ignore errors here
	exec.Command("podman", "kill", cid).Run()
	exec.Command("podman", "rm", "-f", cid).Run()
}

func pullExtensionsImage(podmanInterface PodmanInterface, imgURL string) error {
	// Check if the image is present
	podmanImageInfo, err := podmanInterface.GetPodmanImageInfoByReference(imgURL)
	if err != nil || podmanImageInfo != nil {
		// The image exists (ok) or an error happened
		return err
	}

	// The image is not present, pull it
	var authArgs []string
	if _, err := os.Stat(kubeletAuthFile); err == nil {
		authArgs = append(authArgs, "--authfile", kubeletAuthFile)
	}
	args := []string{"pull", "-q"}
	args = append(args, authArgs...)
	args = append(args, imgURL)
	_, err = pivotutils.RunExtBackground(numRetriesNetCommands, "podman", args...)
	return err
}

func podmanCopy(podmanInterface PodmanInterface, imgURL, osImageContentDir string) (err error) {
	// make sure that osImageContentDir doesn't exist
	os.RemoveAll(osImageContentDir)

	// create a container
	var cidBuf []byte
	containerName := pivottypes.PivotNamePrefix + string(uuid.NewUUID())
	additionalArgs := []string{"--net=none", "--annotation=org.openshift.machineconfigoperator.pivot=true"}
	cidBuf, err = podmanInterface.CreatePodmanContainer(additionalArgs, containerName, imgURL)
	if err != nil {
		return
	}

	// only delete created container, we will delete container image later as we may need it for podmanInspect()
	defer podmanRemove(containerName)

	// copy the content from create container locally into a temp directory under /run/
	cid := strings.TrimSpace(string(cidBuf))
	args := []string{"cp", fmt.Sprintf("%s:/", cid), osImageContentDir}
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

// ExtractExtensionsImage extracts the OS extensions content in a temporary directory under /run/machine-os-extensions
// and returns the path on successful extraction
func (dn *CoreOSDaemon) ExtractExtensionsImage(imgURL string) (osExtensionsImageContentDir string, err error) {
	if err = os.MkdirAll(osExtensionsContentBaseDir, 0o755); err != nil {
		err = fmt.Errorf("error creating directory %s: %w", osExtensionsContentBaseDir, err)
		return
	}

	if osExtensionsImageContentDir, err = os.MkdirTemp(osExtensionsContentBaseDir, "os-extensions-content-"); err != nil {
		return
	}
	if err := pullExtensionsImage(dn.podmanInterface, imgURL); err != nil {
		return osExtensionsImageContentDir, fmt.Errorf("error pulling extensions image %s: %w", imgURL, err)
	}
	// Extract the image using `podman cp`
	return osExtensionsImageContentDir, podmanCopy(dn.podmanInterface, imgURL, osExtensionsImageContentDir)
}

// Remove pending deployment on OSTree based system
func removePendingDeployment() error {
	return runRpmOstree("cleanup", "-p")
}

// applyOSChanges extracts the OS image and adds coreos-extensions repo if we have either OS update or package layering to perform
func (dn *CoreOSDaemon) applyOSChanges(mcDiff machineConfigDiff, oldConfig, newConfig *mcfgv1.MachineConfig) (retErr error) {
	// We previously did not emit this event when kargs changed, so we still don't
	if mcDiff.osUpdate || mcDiff.extensions || mcDiff.kernelType || mcDiff.oclEnabled {
		// We emitted this event before, so keep it
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "InClusterUpgrade", fmt.Sprintf("Updating from oscontainer %s", newConfig.Spec.OSImageURL))
		}
	}

	// Only check the image type and execute OS changes if:
	// - machineconfig changed
	// - we're staying on a realtime kernel ( need to run rpm-ostree update )
	// - we have extensions ( need to run rpm-ostree update )
	// - OCL is enabled ( need to run rpm-ostree update )
	// We have at least one customer that removes the pull secret from the cluster to "shrinkwrap" it for distribution and we want
	// to make sure we don't break that use case, but realtime kernel update and extensions update always ran
	// if they were in use, so we also need to preserve that behavior.
	// https://issues.redhat.com/browse/OCPBUGS-4049
	if mcDiff.osUpdate || mcDiff.extensions || mcDiff.kernelType || mcDiff.kargs || mcDiff.oclEnabled ||
		helpers.CanonicalizeKernelType(newConfig.Spec.KernelType) == ctrlcommon.KernelTypeRealtime ||
		helpers.CanonicalizeKernelType(newConfig.Spec.KernelType) == ctrlcommon.KernelType64kPages {

		// Throw started/staged events only if there is any update required for the OS
		if dn.nodeWriter != nil {
			reason := mcDiff.osChangesString()
			if reason == "" {
				// osChangesString() can return empty in cases where the above diffs are false,
				// but the node uses a non standard kernel, so let's make it a bit more
				// informative in such cases
				reason = fmt.Sprintf("Updating to a target config with %s kernel", helpers.CanonicalizeKernelType(newConfig.Spec.KernelType))
			}
			dn.nodeWriter.Eventf(corev1.EventTypeNormal, "OSUpdateStarted", reason)
		}

		if err := dn.applyLayeredOSChanges(mcDiff, oldConfig, newConfig); err != nil {
			return err
		}

		// TODO: add here validation to check if the extension installed correctly?
		installedSet, err := dn.getCurrentlyInstalledPackages()
		if err != nil {
			return err
		}
		klog.Errorf("installedSet: %v", installedSet)

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
				klog.Errorf("Failed to create event with reason 'OSUpdateStaged': %v", err)
			}
		}
	}

	return nil
}

func calculatePostConfigChangeActionFromMCDiffs(diffFileSet []string) (actions []string) {
	filesPostConfigChangeActionNone := []string{
		caBundleFilePath,
		constants.KubeletAuthFile,
	}
	directoriesPostConfigChangeActionNone := []string{
		constants.OpenShiftNMStateConfigDir,
	}
	filesPostConfigChangeActionReloadCrio := []string{
		constants.ContainerRegistryConfPath,
		constants.GPGNoRebootPath,
		constants.ContainerRegistryPolicyPath,
	}
	filesPostConfigChangeActionRestartCrio := []string{
		constants.UserCABundlePath,
	}
	dirsPostConfigChangeActionReloadCrio := []string{
		constants.SigstoreRegistriesConfigDir,
	}

	actions = []string{postConfigChangeActionNone}
	for _, path := range diffFileSet {
		switch {
		case ctrlcommon.InSlice(path, filesPostConfigChangeActionNone):
			continue

		case ctrlcommon.InSlice(path, filesPostConfigChangeActionReloadCrio),
			ctrlcommon.InSlice(filepath.Dir(path), dirsPostConfigChangeActionReloadCrio):
			// Don't override a restart CRIO action
			if !ctrlcommon.InSlice(postConfigChangeActionRestartCrio, actions) {
				actions = []string{postConfigChangeActionReloadCrio}
			}

		case ctrlcommon.InSlice(path, filesPostConfigChangeActionRestartCrio):
			actions = []string{postConfigChangeActionRestartCrio}

		case ctrlcommon.InSlice(filepath.Dir(path), directoriesPostConfigChangeActionNone):
			continue

		default:
			actions = []string{postConfigChangeActionReboot}
			return actions
		}
	}
	return actions
}

// calculatePostConfigChangeNodeDisruptionActionFromMCDiffs takes action based on the cluster's Node disruption policies.
func calculatePostConfigChangeNodeDisruptionActionFromMCDiffs(diffSSH bool, diffFileSet, diffUnitSet []string, clusterPolicies opv1.NodeDisruptionPolicyClusterStatus) []opv1.NodeDisruptionPolicyStatusAction {
	actions := []opv1.NodeDisruptionPolicyStatusAction{}

	// Step through all file based policies, and build out the actions object
	for _, diffPath := range diffFileSet {
		pathFound, actionsFound := ctrlcommon.FindClosestFilePolicyPathMatch(diffPath, clusterPolicies.Files)
		if pathFound {
			klog.Infof("NodeDisruptionPolicy %v found for diff file %s", actionsFound, diffPath)
			actions = append(actions, actionsFound...)
		} else {
			// If this file path has no policy defined, default to reboot
			klog.V(4).Infof("no policy found for diff path %s", diffPath)
			return []opv1.NodeDisruptionPolicyStatusAction{{
				Type: opv1.RebootStatusAction,
			}}
		}
	}

	// Step through all unit based policies, and build out the actions object
	for _, diffUnit := range diffUnitSet {
		unitFound := false
		for _, policyUnit := range clusterPolicies.Units {
			klog.V(4).Infof("comparing policy unit name %s to diff unit name %s", string(policyUnit.Name), diffUnit)
			if string(policyUnit.Name) == diffUnit {
				klog.Infof("NodeDisruptionPolicy %v found for diff unit %s!", policyUnit.Actions, diffUnit)
				actions = append(actions, policyUnit.Actions...)
				unitFound = true
				break
			}
		}
		if !unitFound {
			// If this unit has no policy defined, default to reboot
			klog.V(4).Infof("no policy found for diff unit %s", diffUnit)
			return []opv1.NodeDisruptionPolicyStatusAction{{
				Type: opv1.RebootStatusAction,
			}}
		}
	}

	// SSH only has one possible policy(and there is a default), so blindly add that if there is an SSH diff
	if diffSSH {
		klog.Infof("SSH diff detected, applying SSH policy %v", clusterPolicies.SSHKey.Actions)
		actions = append(actions, clusterPolicies.SSHKey.Actions...)
	}

	// If any of the actions need a reboot, then just return a single Reboot action
	if apihelpers.CheckNodeDisruptionActionsForTargetActions(actions, opv1.RebootStatusAction) {
		return []opv1.NodeDisruptionPolicyStatusAction{{
			Type: opv1.RebootStatusAction,
		}}
	}

	// If there is a "None" action in conjunction with other kinds of actions, strip out the "None" action elements as it is redundant
	if apihelpers.CheckNodeDisruptionActionsForTargetActions(actions, opv1.NoneStatusAction) {
		if apihelpers.CheckNodeDisruptionActionsForTargetActions(actions, opv1.DrainStatusAction, opv1.ReloadStatusAction, opv1.RestartStatusAction, opv1.DaemonReloadStatusAction, opv1.SpecialStatusAction) {
			finalActions := []opv1.NodeDisruptionPolicyStatusAction{}
			for _, action := range actions {
				if action.Type != opv1.NoneStatusAction {
					finalActions = append(finalActions, action)
				}
			}
			return finalActions
		}
		// If we're here, this means that the action list has only "None" actions; return a single "None" Action
		return []opv1.NodeDisruptionPolicyStatusAction{{
			Type: opv1.NoneStatusAction,
		}}
	}

	// If we're here, return as is - this means action list had zero "None" actions in the list
	return actions
}

func calculatePostConfigChangeAction(diff *machineConfigDiff, diffFileSet []string) ([]string, error) {
	// If a machine-config-daemon-force file is present, it means the user wants to
	// move to desired state without additional validation. We will reboot the node in
	// this case regardless of what MachineConfig diff is.
	if _, err := os.Stat(constants.MachineConfigDaemonForceFile); err == nil {
		if err := os.Remove(constants.MachineConfigDaemonForceFile); err != nil {
			return []string{}, fmt.Errorf("failed to remove force validation file: %w", err)
		}
		klog.Infof("Setting post config change action to postConfigChangeActionReboot; %s present", constants.MachineConfigDaemonForceFile)
		return []string{postConfigChangeActionReboot}, nil
	}

	if diff.osUpdate || diff.kargs || diff.fips || diff.units || diff.kernelType || diff.extensions {
		// must reboot
		return []string{postConfigChangeActionReboot}, nil
	}

	// Calculate actions based on file, unit and ssh diffs
	return calculatePostConfigChangeActionFromMCDiffs(diffFileSet), nil
}

// calculatePostConfigChangeNodeDisruptionAction takes action based on the cluster's Node disruption policies.
func (dn *Daemon) calculatePostConfigChangeNodeDisruptionAction(diff *machineConfigDiff, diffFileSet, diffUnitSet []string) ([]opv1.NodeDisruptionPolicyStatusAction, error) {

	var mcop *opv1.MachineConfiguration
	var pollErr error
	// Wait for mcop.Status.NodeDisruptionPolicyStatus to populate, otherwise error out. This shouldn't take very long
	// as this is done by the operator sync loop, but may be extended if transitioning to TechPreview as the operator restarts,
	if err := wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, 2*time.Minute, true, func(_ context.Context) (bool, error) {
		mcop, pollErr = dn.mcopClient.OperatorV1().MachineConfigurations().Get(context.TODO(), ctrlcommon.MCOOperatorKnobsObjectName, metav1.GetOptions{})
		if pollErr != nil {
			klog.Errorf("calculating NodeDisruptionPolicies: MachineConfiguration/cluster has not been created yet")
			pollErr = fmt.Errorf("MachineConfiguration/cluster has not been created yet")
			return false, nil
		}

		// Ensure status.ObservedGeneration matches the last generation of MachineConfiguration
		if mcop.Generation != mcop.Status.ObservedGeneration {
			klog.Errorf("calculating NodeDisruptionPolicies: NodeDisruptionPolicyStatus is not up to date.")
			pollErr = fmt.Errorf("NodeDisruptionPolicyStatus is not up to date")
			return false, nil
		}
		return true, nil
	}); err != nil {
		klog.Errorf("NodeDisruptionPolicyStatus was not ready: %v", pollErr)
		return nil, fmt.Errorf("NodeDisruptionPolicyStatus was not ready: %v", pollErr)
	}

	// Continue policy calculation if no errors were encountered in fetching the policy.
	// If a machine-config-daemon-force file is present, it means the user wants to
	// move to desired state without additional validation. We will reboot the node in
	// this case regardless of what MachineConfig diff is.
	klog.Infof("Calculating node disruption actions")
	if _, err := os.Stat(constants.MachineConfigDaemonForceFile); err == nil {
		if err = os.Remove(constants.MachineConfigDaemonForceFile); err != nil {
			return []opv1.NodeDisruptionPolicyStatusAction{}, fmt.Errorf("failed to remove force validation file: %w", err)
		}
		klog.Infof("Setting post config change node disruption action to Reboot; %s present", constants.MachineConfigDaemonForceFile)
		return []opv1.NodeDisruptionPolicyStatusAction{{
			Type: opv1.RebootStatusAction,
		}}, nil
	}

	if diff.osUpdate || diff.kargs || diff.fips || diff.kernelType || diff.extensions {
		// must reboot
		return []opv1.NodeDisruptionPolicyStatusAction{{
			Type: opv1.RebootStatusAction,
		}}, nil
	}
	if !diff.files && !diff.units && !diff.passwd {
		// This is a diff which requires no actions
		klog.Infof("No changes in files, units or SSH keys, no NodeDisruptionPolicies are in effect")
		return []opv1.NodeDisruptionPolicyStatusAction{{
			Type: opv1.NoneStatusAction,
		}}, nil
	}

	// Calculate actions based on file, unit and ssh diffs
	nodeDisruptionActions := calculatePostConfigChangeNodeDisruptionActionFromMCDiffs(diff.passwd, diffFileSet, diffUnitSet, mcop.Status.NodeDisruptionPolicyStatus.ClusterPolicies)

	// Print out node disruption actions for debug purposes
	klog.Infof("Calculated node disruption actions:")
	for _, action := range nodeDisruptionActions {
		switch action.Type {
		case opv1.ReloadStatusAction:
			klog.Infof("%v - %v", action.Type, action.Reload.ServiceName)
		case opv1.RestartStatusAction:
			klog.Infof("%v - %v", action.Type, action.Restart.ServiceName)
		default:
			klog.Infof("%v", action.Type)
		}
	}

	return nodeDisruptionActions, nil

}

// Finalizes the revert process by enabling a special systemd unit prior to
// rebooting the node.
//
// After we write the original factory image to the node, none of the files
// specified in the MachineConfig will be written due to how rpm-ostree handles
// file writes. Because those files are owned by the layered container image,
// they are not present after reboot; even if we were to write them to the node
// before rebooting. Consequently, after reverting back to the original image,
// the node will lose contact with the control plane and the easiest way to
// reestablish contact is to rebootstrap the node.
//
// By comparison, if we write a file that is _not_ owned by the layered
// container image, this file will persist after reboot. So what we do is write
// a special systemd unit that will rebootstrap the node upon reboot.
// Unfortunately, this will incur a second reboot during the rollback process,
// so there is room for improvement here.
func (dn *Daemon) finalizeRevertToNonLayering(newConfig *mcfgv1.MachineConfig) (retErr error) {
	// First, we write the new MachineConfig to a file. This is both the signal
	// that the revert systemd unit should fire as well as the desired source of
	// truth.
	outBytes, err := json.Marshal(newConfig)
	if err != nil {
		return fmt.Errorf("could not marshal MachineConfig %q to JSON: %w", newConfig.Name, err)
	}

	if err := writeFileAtomicallyWithDefaults(runtimeassets.RevertServiceMachineConfigFile, outBytes); err != nil {
		return fmt.Errorf("could not write MachineConfig %q to %q: %w", newConfig.Name, runtimeassets.RevertServiceMachineConfigFile, err)
	}

	klog.Infof("Wrote MachineConfig %q to %q", newConfig.Name, runtimeassets.RevertServiceMachineConfigFile)

	defer func() {
		if retErr != nil {
			if err := os.RemoveAll(runtimeassets.RevertServiceMachineConfigFile); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back %q on disk: %q", runtimeassets.RevertServiceMachineConfigFile, errs)
				return
			}
		}
	}()

	// Next, we enable the revert systemd unit. This renders and writes the
	// machine-config-daemon-revert.service systemd unit, clones it, and writes
	// it to disk. The reason for doing it this way is because it will persist
	// after the reboot since it was not written or mutated by the rpm-ostree
	// process.
	if err := dn.enableRevertSystemdUnit(newConfig); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.disableRevertSystemdUnit(); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back systemd unit %q on disk: %e", runtimeassets.RevertServiceName, errs)
				return
			}
		}
	}()

	return nil
}

// Update the node to the provided node configuration.
// This function should be de-duped with dn.updateHypershift() and
// dn.updateOnClusterLayering(). See: https://issues.redhat.com/browse/MCO-810 for
// discussion.
//
//nolint:gocyclo
func (dn *Daemon) update(oldConfig, newConfig *mcfgv1.MachineConfig, skipCertificateWrite, firstBoot bool) (retErr error) {
	oldConfig = canonicalizeEmptyMC(oldConfig)

	mcDiff, err := newMachineConfigDiff(oldConfig, newConfig)
	if err != nil {
		return fmt.Errorf("could not calculate config diff: %w", err)
	}

	if mcDiff.oclEnabled {
		// Check if any OCL-specific changes are present
		oclChange := ctrlcommon.RequiresRebuild(oldConfig, newConfig)
		if !oclChange {
			klog.Info("OCL enabled but no OCL-specific changes detected - applying non-OCL update")
			mcDiff.oclEnabled = false
		}
	}

	if mcDiff.revertFromOCL {
		klog.Info("Initiating OCL revert process")
		if err := dn.finalizeRevertToNonLayering(newConfig); err != nil {
			return fmt.Errorf("failed to prepare OCL revert: %w", err)
		}

		// Cleanup revert artifacts if update fails before completion
		defer func() {
			if retErr != nil {
				klog.Info("Rolling back OCL revert setup due to error")
				if err := os.Remove(runtimeassets.RevertServiceMachineConfigFile); err != nil && !os.IsNotExist(err) {
					klog.Warningf("Error cleaning revert config: %v", err)
				}
				if err := dn.disableRevertSystemdUnit(); err != nil {
					klog.Warningf("Error disabling revert service: %v", err)
				}
			}
		}()
	}

	if dn.nodeWriter != nil {
		// Refetch node from lister to get fresh state before checking guard.
		// This prevents overwriting Degraded/Unreconcilable states that were just set.
		freshNode, err := dn.nodeLister.Get(dn.name)
		if err != nil {
			return fmt.Errorf("error fetching fresh node state: %w", err)
		}
		state, err := getNodeAnnotationExt(freshNode, constants.MachineConfigDaemonStateAnnotationKey, true)
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
		dn.CancelSIGTERM()
	}()

	// Get MCP associated with node
	pool, err := helpers.GetPrimaryPoolNameForMCN(dn.mcpLister, dn.node)
	if err != nil {
		return err
	}

	// Update the MCN's NodeNodeDegraded condition with the update result
	defer func() {
		dn.reportMachineNodeDegradeStatus(retErr, pool)
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

	klog.Infof("Checking Reconcilable for config %v to %v", oldConfigName, newConfigName)
	// checking for reconcilability
	// make sure we can actually reconcile this state

	var mcop opv1.MachineConfiguration
	if !firstBoot && dn.fgHandler != nil && dn.fgHandler.Enabled(features.FeatureGateIrreconcilableMachineConfig) {
		mcopPtr, err := ctrlcommon.GetIrreconcilableOverrides(dn.mcopLister)
		if err != nil {
			return err
		}
		mcop = *mcopPtr
	}

	diff, reconcilableError := reconcilable(oldConfig, newConfig, &mcop.Spec.IrreconcilableValidationOverrides)

	if reconcilableError != nil {
		Nerr := upgrademonitor.GenerateAndApplyMachineConfigNodes(
			&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdatePrepared, Reason: string(mcfgv1.MachineConfigNodeUpdatePrepared), Message: fmt.Sprintf("Update Failed compatibility validation. MachineConfigs %v and %v are not compatible. Err: %s", oldConfigName, newConfigName, reconcilableError.Error())},
			nil,
			metav1.ConditionUnknown,
			metav1.ConditionFalse,
			dn.node,
			dn.mcfgClient,
			dn.fgHandler,
			pool,
		)
		if Nerr != nil {
			klog.Errorf("Error making MCN for Preparing update failed: %v", err)
		}
		wrappedErr := fmt.Errorf("can't reconcile config %s with %s: %w", oldConfigName, newConfigName, reconcilableError)
		if dn.nodeWriter != nil {
			dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedToReconcile", wrappedErr.Error())
		}
		return &unreconcilableErr{wrappedErr}
	}

	logSystem("Starting update from %s to %s: %+v", oldConfigName, newConfigName, diff)

	diffFileSet := ctrlcommon.CalculateConfigFileDiffs(&oldIgnConfig, &newIgnConfig)
	// Get the added and updated units
	unitDiff := ctrlcommon.GetChangedConfigUnitsByType(&oldIgnConfig, &newIgnConfig)
	addedOrChangedUnits := slices.Concat(unitDiff.Added, unitDiff.Updated)
	// Get the names of all units changed in some way (added, removed, or updated)
	var allChangedUnitNames []string
	for _, unit := range append(addedOrChangedUnits, unitDiff.Removed...) {
		allChangedUnitNames = append(allChangedUnitNames, unit.Name)
	}

	var nodeDisruptionActions []opv1.NodeDisruptionPolicyStatusAction
	var actions []string
	// Check for forcefile before calculatePostConfigChange* functions delete it.
	// This is needed for updateFiles to know whether to write all units (OCPBUGS-74692).
	forceFilePresent := forceFileExists()
	// Node Disruption Policies cannot be used during firstboot as API is not accessible.
	if !firstBoot {
		nodeDisruptionActions, err = dn.calculatePostConfigChangeNodeDisruptionAction(diff, diffFileSet, allChangedUnitNames)
	} else {
		actions, err = calculatePostConfigChangeAction(diff, diffFileSet)
		klog.Infof("Skipping node disruption polciies as node is executing first boot.")
	}

	if err != nil {
		Nerr := upgrademonitor.GenerateAndApplyMachineConfigNodes(
			&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdatePrepared, Reason: string(mcfgv1.MachineConfigNodeUpdatePrepared), Message: fmt.Sprintf("Update Failed compatibility validation. MachineConfigs %v and %v are not available for update. Error calculating post config change actions: %s", oldConfigName, newConfigName, err.Error())},
			nil,
			metav1.ConditionUnknown,
			metav1.ConditionFalse,
			dn.node,
			dn.mcfgClient,
			dn.fgHandler,
			pool,
		)
		if Nerr != nil {
			klog.Errorf("Error making MCN for Preparing update failed: %v", err)
		}
		return err
	}

	var drain bool
	// Node Disruption Policies cannot be used during firstboot as API is not accessible.
	if !firstBoot {
		// Check actions list and perform node drain if required
		drain, err = isDrainRequiredForNodeDisruptionActions(nodeDisruptionActions, oldIgnConfig, newIgnConfig)
		if err != nil {
			return err
		}
		klog.Infof("Drain calculated for node disruption: %v for config %s", drain, newConfigName)
	} else {
		// Check and perform node drain if required
		drain, err = isDrainRequired(actions, diffFileSet, oldIgnConfig, newIgnConfig)
		if err != nil {
			return err
		}
	}
	err = upgrademonitor.GenerateAndApplyMachineConfigNodes(
		&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdatePrepared, Reason: string(mcfgv1.MachineConfigNodeUpdatePrepared), Message: fmt.Sprintf("Update Compatible. Post Cfg Actions: %v Drain Required: %t", actions, drain)},
		nil,
		metav1.ConditionTrue,
		metav1.ConditionFalse,
		dn.node,
		dn.mcfgClient,
		dn.fgHandler,
		pool,
	)
	if err != nil {
		klog.Errorf("Error making MCN for Update Compatible: %v", err)
	}

	err = upgrademonitor.GenerateAndApplyMachineConfigNodeSpec(dn.fgHandler, pool, dn.node, dn.mcfgClient)
	if err != nil {
		klog.Errorf("Error making MCN spec for Update Compatible: %v", err)
	}
	if drain {
		if err := dn.performDrain(); err != nil {
			return err
		}
	} else {
		klog.Info("Changes do not require drain, skipping.")
		err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
			&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdateExecuted, Reason: string(mcfgv1.MachineConfigNodeUpdateDrained), Message: "Node Drain Not required for this update."},
			&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdateDrained, Reason: fmt.Sprintf("%s%s", string(mcfgv1.MachineConfigNodeUpdateExecuted), string(mcfgv1.MachineConfigNodeUpdateDrained)), Message: "Node Drain Not required for this update."},
			metav1.ConditionUnknown,
			metav1.ConditionFalse,
			dn.node,
			dn.mcfgClient,
			dn.fgHandler,
			pool,
		)
		if err != nil {
			klog.Errorf("Error making MCN for Drain not required: %v", err)
		}
	}

	files := ""
	for _, f := range newIgnConfig.Storage.Files {
		files += f.Path + " "
	}

	updatesNeeded := []string{"not", "not"}
	if diff.passwd {
		updatesNeeded[1] = ""
	}
	if diff.osUpdate || diff.extensions || diff.kernelType {
		updatesNeeded[0] = ""
	}

	err = upgrademonitor.GenerateAndApplyMachineConfigNodes(
		&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdateExecuted, Reason: string(mcfgv1.MachineConfigNodeUpdateFilesAndOS), Message: fmt.Sprintf("Updating the Files and OS on disk as a part of the in progress phase")},
		&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdateFilesAndOS, Reason: fmt.Sprintf("%s%s", string(mcfgv1.MachineConfigNodeUpdateExecuted), string(mcfgv1.MachineConfigNodeUpdateFilesAndOS)), Message: fmt.Sprintf("Applying files and new OS config to node. OS will %s need an update. SSH Keys will %s need an update", updatesNeeded[0], updatesNeeded[1])},
		metav1.ConditionUnknown,
		metav1.ConditionUnknown,
		dn.node,
		dn.mcfgClient,
		dn.fgHandler,
		pool,
	)
	if err != nil {
		klog.Errorf("Error making MCN for Updating Files and OS: %v", err)
	}

	// update files on disk that need updating
	if err := dn.updateFiles(oldIgnConfig, newIgnConfig, addedOrChangedUnits, skipCertificateWrite, forceFilePresent); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.updateFiles(newIgnConfig, oldIgnConfig, addedOrChangedUnits, skipCertificateWrite, false); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back files writes: %w", errs)
				return
			}
		}
	}()

	// update file permissions
	if err := dn.updateKubeConfigPermission(); err != nil {
		return err
	}

	// only update passwd if it has changed (do not nullify)
	// we do not need to include SetPasswordHash in this, since only updateSSHKeys has issues on firstboot.
	if diff.passwd {
		klog.Info("setting passwd")
		if err := dn.updateSSHKeys(newIgnConfig.Passwd.Users, oldIgnConfig.Passwd.Users); err != nil {
			return err
		}

		defer func() {
			if retErr != nil {
				if err := dn.updateSSHKeys(newIgnConfig.Passwd.Users, oldIgnConfig.Passwd.Users); err != nil {
					errs := kubeErrs.NewAggregate([]error{err, retErr})
					retErr = fmt.Errorf("error rolling back SSH keys updates: %w", errs)
					return
				}
			}
		}()
	}

	// Set password hash
	if err := dn.SetPasswordHash(newIgnConfig.Passwd.Users, oldIgnConfig.Passwd.Users); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.SetPasswordHash(newIgnConfig.Passwd.Users, oldIgnConfig.Passwd.Users); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back password hash updates: %w", errs)
				return
			}
		}
	}()

	if dn.os.IsCoreOSVariant() {
		coreOSDaemon := CoreOSDaemon{dn}

		if mcDiff.revertFromOCL {
			klog.Info("Applying OCL revert-specific OS changes")
		}

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
		klog.Info("updating the OS on non-CoreOS nodes is not supported")
	}

	// Ideally we would want to update kernelArguments only via MachineConfigs.
	// We are keeping this to maintain compatibility and OKD requirement.
	if err := UpdateTuningArgs(KernelTuningFile, CmdLineFile); err != nil {
		return err
	}

	// At this point, we write the now expected to be "current" config to /etc.
	// When we reboot, we'll find this file and validate that we're in this state,
	// and that completes an update.
	newOdc := newOnDiskConfigFromMachineConfig(newConfig)

	klog.Infof("update: about to store CURRENT on-disk config: MC=%q, Spec.OSImageURL=%q, extracted-image=%q",
		newConfig.GetName(), newConfig.Spec.OSImageURL, newOdc.currentImage)
	if err := dn.storeCurrentConfigOnDisk(newOnDiskConfigFromMachineConfig(newConfig)); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			oldOdc := newOnDiskConfigFromMachineConfig(oldConfig)
			klog.Infof("update: rollback — restoring CURRENT on-disk config: MC=%q, Spec.OSImageURL=%q, extracted-image=%q",
				oldConfig.GetName(), oldConfig.Spec.OSImageURL, oldOdc.currentImage)
			if err := dn.storeCurrentConfigOnDisk(newOnDiskConfigFromMachineConfig(oldConfig)); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back current config on disk: %w", errs)
				return
			}
		}
	}()

	err = upgrademonitor.GenerateAndApplyMachineConfigNodes(
		&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdateExecuted, Reason: string(mcfgv1.MachineConfigNodeUpdateFilesAndOS), Message: fmt.Sprintf("Updated the Files and OS on disk as a part of the in progress phase")},
		&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeUpdateFilesAndOS, Reason: fmt.Sprintf("%s%s", string(mcfgv1.MachineConfigNodeUpdateExecuted), string(mcfgv1.MachineConfigNodeUpdateFilesAndOS)), Message: fmt.Sprintf("Applied files and new OS config to node. OS did %s need an update. SSH Keys did %s need an update", updatesNeeded[0], updatesNeeded[1])},
		metav1.ConditionTrue,
		metav1.ConditionTrue,
		dn.node,
		dn.mcfgClient,
		dn.fgHandler,
		pool,
	)
	if err != nil {
		klog.Errorf("Error making MCN for Updated Files and OS: %v", err)
	}
	// Node Disruption Policies cannot be used during firstboot as API is not accessible.
	if !firstBoot {
		return dn.performPostConfigChangeNodeDisruptionAction(nodeDisruptionActions, newConfig.GetName())
	}
	// If we're here, node disruption policies can't be used, so perform legacy action
	return dn.performPostConfigChangeAction(actions, newConfig.GetName())
}

// This is currently a subsection copied over from update() since we need to be more nuanced. Should eventually
// de-dupe the functions.
// See: https://issues.redhat.com/browse/MCO-810
func (dn *Daemon) updateHypershift(oldConfig, newConfig *mcfgv1.MachineConfig, diff *machineConfigDiff) (retErr error) {
	oldIgnConfig, err := ctrlcommon.ParseAndConvertConfig(oldConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing old Ignition config failed: %w", err)
	}
	newIgnConfig, err := ctrlcommon.ParseAndConvertConfig(newConfig.Spec.Config.Raw)
	if err != nil {
		return fmt.Errorf("parsing new Ignition config failed: %w", err)
	}

	unitDiff := ctrlcommon.GetChangedConfigUnitsByType(&oldIgnConfig, &newIgnConfig)
	addedOrChangedUnits := slices.Concat(unitDiff.Added, unitDiff.Updated)

	// Check for forcefile to support config drift recovery (OCPBUGS-74692)
	forceFilePresent := forceFileExists()

	// update files on disk that need updating
	// We should't skip the certificate write in HyperShift since it does not run the extra daemon process
	if err := dn.updateFiles(oldIgnConfig, newIgnConfig, addedOrChangedUnits, false, forceFilePresent); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.updateFiles(newIgnConfig, oldIgnConfig, addedOrChangedUnits, false, false); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error rolling back files writes: %w", errs)
				return
			}
		}
	}()

	if err := dn.updateSSHKeys(newIgnConfig.Passwd.Users, oldIgnConfig.Passwd.Users); err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			if err := dn.updateSSHKeys(newIgnConfig.Passwd.Users, oldIgnConfig.Passwd.Users); err != nil {
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
		klog.Info("updating the OS on non-CoreOS nodes is not supported")
	}

	if err := UpdateTuningArgs(KernelTuningFile, CmdLineFile); err != nil {
		return err
	}

	klog.Info("Successfully completed Hypershift config update")
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
	osUpdate      bool
	kargs         bool
	fips          bool
	passwd        bool
	files         bool
	units         bool
	kernelType    bool
	extensions    bool
	oclEnabled    bool
	revertFromOCL bool
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

	force := forceFileExists()

	_, oldOCLImage := extractOCLImageFromMachineConfig(oldConfig)
	_, newOCLImage := extractOCLImageFromMachineConfig(newConfig)

	diff := &machineConfigDiff{
		osUpdate:   oldConfig.Spec.OSImageURL != newConfig.Spec.OSImageURL || force,
		kargs:      !(kargsEmpty || reflect.DeepEqual(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments)),
		fips:       oldConfig.Spec.FIPS != newConfig.Spec.FIPS,
		passwd:     !reflect.DeepEqual(oldIgn.Passwd, newIgn.Passwd),
		files:      !reflect.DeepEqual(oldIgn.Storage.Files, newIgn.Storage.Files),
		units:      !reflect.DeepEqual(oldIgn.Systemd.Units, newIgn.Systemd.Units),
		kernelType: helpers.CanonicalizeKernelType(oldConfig.Spec.KernelType) != helpers.CanonicalizeKernelType(newConfig.Spec.KernelType),
		extensions: !(extensionsEmpty || reflect.DeepEqual(oldConfig.Spec.Extensions, newConfig.Spec.Extensions)),
		oclEnabled: (oldOCLImage != "" || newOCLImage != "") || (oldOCLImage != "" && newOCLImage != ""),
	}

	if !diff.oclEnabled {
		return diff, nil
	}

	// If OCL is enabled, compute OS update and revertFromOCL separately.
	diff.osUpdate = oldOCLImage != newOCLImage || force
	diff.revertFromOCL = newOCLImage == ""
	return diff, nil
}

// reconcilable checks the configs to make sure that the only changes requested
// are ones we know how to do in-place. If we can reconcile, (nil, nil) is returned.
// Otherwise, if we can't do it in place, the node is marked as degraded;
// the returned string value includes the rationale.
//
// we can only update machine configs that have changes to the files,
// directories, links, and systemd units sections of the included ignition
// config currently.
//
// NOTE: The RenderController is also checking the configs to ensure that they
// are reconcilable. By the time this code is reached, we can be reasonably
// confident that the configs are reconcilable however, this check remains here
// out of an abundance of caution. Additionally, this code has access to the
// underlying node filesystem and can inspect the FIPS file
// (/proc/sys/crypto/fips_enabled) and can determine if there is a mismatch
// between the MachineConfig and the actual on-disk state.
func reconcilable(oldConfig, newConfig *mcfgv1.MachineConfig, overrides *opv1.IrreconcilableValidationOverrides) (*machineConfigDiff, error) {
	if err := ctrlcommon.IsRenderedConfigReconcilable(oldConfig, newConfig, overrides); err != nil {
		return nil, fmt.Errorf("configs %s, %s are not reconcilable: %w", oldConfig.Name, newConfig.Name, err)
	}

	// FIPS section
	// We do not allow update to FIPS for a running cluster, so any changes here will be an error
	if err := checkFIPS(oldConfig, newConfig); err != nil {
		return nil, err
	}

	// we made it through all the checks. reconcile away!
	klog.V(2).Info("Configs are reconcilable")
	mcDiff, err := newMachineConfigDiff(oldConfig, newConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating machineConfigDiff: %w", err)
	}
	return mcDiff, nil
}

func processFips(handler func(bool) error) error {
	content, err := os.ReadFile(fipsFile)
	if err != nil {
		if os.IsNotExist(err) {
			// we just exit cleanly if we're not even on linux
			klog.Infof("no %s on this system, skipping FIPS check", fipsFile)
			return nil
		}
		return fmt.Errorf("error reading FIPS file at %s: %s: %w", fipsFile, string(content), err)
	}
	nodeFIPS, err := strconv.ParseBool(strings.TrimSuffix(string(content), "\n"))
	if err != nil {
		return fmt.Errorf("error parsing FIPS file at %s: %w", fipsFile, err)
	}
	return handler(nodeFIPS)
}

// checkFIPS verifies the state of FIPS on the system before an update.
// Our new thought around this is that really FIPS should be a "day 1"
// operation, and we don't want to make it editable after the fact.
// See also https://github.com/openshift/installer/pull/2594
// Anyone who wants to force this can change the MC flag, then
// `oc debug node` and run the disable command by hand, then reboot.
// If we detect that FIPS has been changed, we reject the update.
func checkFIPS(current, desired *mcfgv1.MachineConfig) error {
	return processFips(func(nodeFIPS bool) error {
		if desired.Spec.FIPS == nodeFIPS {
			if desired.Spec.FIPS {
				klog.Infof("FIPS is configured and enabled")
			}
			// Check if FIPS on the system is at the desired setting
			current.Spec.FIPS = nodeFIPS
			return nil
		}
		return fmt.Errorf("detected change to FIPS flag; refusing to modify FIPS on a running cluster")
	})
}

// Set the value of the running node fips to the provided machine config
// The purpose is to set the fips value to the machine config representing the
// current setting.  It is used when comparing the running configuration
// to the desired configuration in order to decide if reboot is necessary during
// firstboot
func setNodeFipsIntoMC(mc *mcfgv1.MachineConfig) error {
	return processFips(func(nodeFIPS bool) error {
		mc.Spec.FIPS = nodeFIPS
		return nil
	})
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
	for start = begin; start < len(args) && isSpace(args[start]); {
		start++
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
	logSystem("Running rpm-ostree %v", args)
	return runRpmOstree(args...)
}

// getCurrentlyInstalledPackages returns the list of currently installed extension packages
func (dn *Daemon) getCurrentlyInstalledPackages() (sets.Set[string], error) {
	status, err := dn.NodeUpdaterClient.Peel().QueryStatus()
	if err != nil {
		return nil, fmt.Errorf("failed to query rpm-ostree status: %w", err)
	}

	bootedDeployment, err := status.GetBootedDeployment()
	if err != nil {
		return nil, fmt.Errorf("failed to get booted deployment: %w", err)
	}

	return sets.New(bootedDeployment.RequestedPackages...), nil
}

// generateExtensionsArgs generates extension arguments for rpm-ostree, based on the target config
// and currently installed extension packages.
func generateExtensionsArgs(installedSet sets.Set[string], newConfig *mcfgv1.MachineConfig) []string {

	// Get packages that should be installed based on new config
	supportedExtensions := ctrlcommon.SupportedExtensions()
	requiredSet := sets.New[string]()

	for _, ext := range newConfig.Spec.Extensions {
		if packages, exists := supportedExtensions[ext]; exists {
			requiredSet.Insert(packages...)
		}
	}

	var extArgs []string

	// Find packages to install: required but not currently installed
	toInstall := requiredSet.Difference(installedSet)
	for _, pkg := range sets.List(toInstall) {
		extArgs = append(extArgs, constants.RPMOSTreeInstallArg, pkg)
	}

	// Build set of all extension packages that can be managed, since
	// we only want to remove packages that the MCO manages.
	allExtensionSet := sets.New[string]()
	for _, packages := range supportedExtensions {
		allExtensionSet.Insert(packages...)
	}

	// Find packages to uninstall: currently installed extension packages that are not required
	managedInstalled := installedSet.Intersection(allExtensionSet)
	toUninstall := managedInstalled.Difference(requiredSet)
	for _, pkg := range sets.List(toUninstall) {
		extArgs = append(extArgs, constants.RPMOSTreeUninstallArg, pkg)
	}

	return extArgs
}

func (dn *CoreOSDaemon) applyExtensions(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// Take no action if we're not an RHCOS node
	if !dn.os.IsEL() {
		return nil
	}

	extensionsEmpty := len(oldConfig.Spec.Extensions) == 0 && len(newConfig.Spec.Extensions) == 0
	if (extensionsEmpty) ||
		(reflect.DeepEqual(oldConfig.Spec.Extensions, newConfig.Spec.Extensions) && oldConfig.Spec.OSImageURL == newConfig.Spec.OSImageURL) {
		return nil
	}

	// Validate extensions allowlist on RHCOS nodes
	if err := ctrlcommon.ValidateMachineConfigExtensions(newConfig.Spec); err != nil {
		return err
	}

	// Get currently installed packages from rpm-ostree status
	installedSet, err := dn.getCurrentlyInstalledPackages()
	if err != nil {
		return err
	}

	// Generate arguments based on current extension packages and the target config's extensions
	args := generateExtensionsArgs(installedSet, newConfig)
	if len(args) == 0 {
		logSystem("No extension updates required")
		return nil
	}

	// Add "update" to the start of argument list
	args = append([]string{constants.RPMOSTreeUpdateArg}, args...)
	logSystem("Applying extensions : %+q", args)
	return runRpmOstree(args...)
}

// validateExtensions checks that all extension packages specified in the MachineConfig
// are actually installed on the node. This is called after reboot to verify that
// extensions were installed correctly. If any required packages are missing, an error
// is returned which will cause the node (and subsequently the MachineConfigPool) to degrade.
func (dn *CoreOSDaemon) validateExtensions(currentConfig *mcfgv1.MachineConfig) error {
	// Skip validation on non-RHCOS nodes since extensions are not supported
	if !dn.os.IsEL() {
		return nil
	}

	// Skip validation if no extensions are specified
	if len(currentConfig.Spec.Extensions) == 0 {
		return nil
	}

	// Get currently installed packages from rpm-ostree status
	installedSet, err := dn.getCurrentlyInstalledPackages()
	if err != nil {
		return fmt.Errorf("failed to get installed packages for extension validation: %w", err)
	}

	// Validate that all required extension packages are installed
	supportedExtensions := ctrlcommon.SupportedExtensions()
	var missingPackages []string

	for _, ext := range currentConfig.Spec.Extensions {
		packages, exists := supportedExtensions[ext]
		if !exists {
			klog.Warningf("Extension %q is not in the supported extensions list, skipping validation", ext)
			continue
		}

		for _, pkg := range packages {
			if !installedSet.Has(pkg) {
				missingPackages = append(missingPackages, fmt.Sprintf("%s (from extension %s)", pkg, ext))
			}
		}
	}

	if len(missingPackages) > 0 {
		return fmt.Errorf("extension packages not installed correctly: %v", missingPackages)
	}

	klog.V(4).Infof("Extension validation passed: all %d extension(s) installed correctly", len(currentConfig.Spec.Extensions))
	return nil
}

// switchKernel updates kernel on host with the kernelType specified in MachineConfig.
// Right now it supports default (traditional), realtime kernel and 64k pages kernel
func (dn *CoreOSDaemon) switchKernel(oldConfig, newConfig *mcfgv1.MachineConfig) error {
	// We support Kernel update only on RHCOS and SCOS nodes
	if !dn.os.IsEL() {
		klog.Info("updating kernel on non-RHCOS nodes is not supported")
		klog.Infof("Detected osrelease \n %+q", dn.os)
		return nil
	}

	oldKtype := helpers.CanonicalizeKernelType(oldConfig.Spec.KernelType)
	newKtype := helpers.CanonicalizeKernelType(newConfig.Spec.KernelType)

	// In the OS update path, we removed overrides for kernel-rt.  So if the target (new) config
	// is also default (i.e. throughput) then we have nothing to do.
	if newKtype == ctrlcommon.KernelTypeDefault {
		return nil
	}

	// 64K memory pages kernel is only supported for aarch64
	if newKtype == ctrlcommon.KernelType64kPages && goruntime.GOARCH != "arm64" {
		return fmt.Errorf("64k-pages is only supported for aarch64 architecture")
	}

	if oldKtype != newKtype {
		logSystem("Initiating switch to kernel %s", newKtype)
	} else {
		logSystem("Re-applying kernel type %s", newKtype)
	}

	kernelPackages := dn.getKernelPackagesForTargetRelease()
	if newKtype == ctrlcommon.KernelTypeRealtime {
		// Switch to RT kernel
		args := []string{"override", "remove"}
		args = append(args, kernelPackages.defaultKernel...)
		for _, pkg := range kernelPackages.realtimeKernel {
			args = append(args, constants.RPMOSTreeInstallArg, pkg)
		}

		return runRpmOstree(args...)
	} else if newKtype == ctrlcommon.KernelType64kPages {
		// Switch to 64k pages kernel
		args := []string{"override", "remove"}
		args = append(args, kernelPackages.defaultKernel...)
		for _, pkg := range kernelPackages.hugePagesKernel {
			args = append(args, constants.RPMOSTreeInstallArg, pkg)
		}

		return runRpmOstree(args...)
	}
	return fmt.Errorf("unhandled kernel type %s", newKtype)
}

// getKernelPackagesForTargetRelease returns the list of kernel packaged for the running OS release.
func (dn *CoreOSDaemon) getKernelPackagesForTargetRelease() releaseKernelPackages {
	// TODO: Drop this code and use https://github.com/coreos/rpm-ostree/issues/2542 instead

	kernelPackages := releaseKernelPackages{
		defaultKernel:   []string{"kernel", "kernel-core", "kernel-modules", "kernel-modules-core", "kernel-modules-extra"},
		hugePagesKernel: []string{"kernel-64k-core", "kernel-64k-modules", "kernel-64k-modules-core", "kernel-64k-modules-extra"},
		// Note this list explicitly does *not* include kernel-rt as that is a meta-package that tries to pull in a lot
		// of other dependencies we don't want for historical reasons.
		realtimeKernel: []string{"kernel-rt-core", "kernel-rt-modules", "kernel-rt-modules-extra"},
	}
	return kernelPackages
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
func (dn *Daemon) updateFiles(oldIgnConfig, newIgnConfig ign3types.Config, addedOrChangedUnits []ign3types.Unit, skipCertificateWrite, forceFilePresent bool) error {
	klog.Info("Updating files")
	if err := dn.writeFiles(newIgnConfig.Storage.Files, skipCertificateWrite); err != nil {
		return err
	}

	// With OCPBUGS-58023, we updated this flow to only write units that were either added or
	// updated. As can be seen in OCPBUGS-74692, this impacted the traditional method to recover
	// from config drifts with systemd units. It makes the `touch /run/machine-config-daemon-force`
	// command useless since the new flow does not rewrite all files, only the ones that have been
	// added or changed with the latest MC. To keep the fix for OCPBUGS-58023 and allow continue
	// supporting the traditional config drift recovery for systemd units, all units should be
	// written when a forcefile exists.
	unitsToWrite := addedOrChangedUnits
	if forceFilePresent {
		klog.Info("Forcefile exists, writing all units")
		unitsToWrite = newIgnConfig.Systemd.Units
	}

	if err := dn.writeUnits(unitsToWrite); err != nil {
		return err
	}
	return dn.deleteStaleData(oldIgnConfig, newIgnConfig)
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
	klog.Info("Deleting stale data")

	newFileSet := make(map[string]struct{})
	for _, f := range newIgnConfig.Storage.Files {
		newFileSet[f.Path] = struct{}{}
	}

	// need to skip these on upgrade if they are in a MC, or else we will remove all certs!
	certsToSkip := []string{
		userCABundleFilePath,
		caBundleFilePath,
		cloudCABundleFilePath,
	}
	for _, f := range oldIgnConfig.Storage.Files {
		if _, ok := newFileSet[f.Path]; ok {
			continue
		}
		skipBecauseCert := false
		for _, cert := range certsToSkip {
			if cert == f.Path {
				skipBecauseCert = true
				break
			}
		}
		if strings.Contains(filepath.Dir(f.Path), imageCAFilePath) {
			skipBecauseCert = true
		}
		if skipBecauseCert {
			continue
		}
		if _, err := os.Stat(noOrigFileStampName(f.Path)); err == nil {
			if delErr := os.Remove(noOrigFileStampName(f.Path)); delErr != nil {
				return fmt.Errorf("deleting noorig file stamp %q: %w", noOrigFileStampName(f.Path), delErr)
			}
			klog.V(2).Infof("Removing file %q completely", f.Path)
		} else if _, err := os.Stat(origFileName(f.Path)); err == nil {
			// Add a check for backwards compatibility: basically if the file doesn't exist in /usr/etc (on FCOS/RHCOS)
			// and no rpm is claiming it, we assume that the orig file came from a wrongful backup of a MachineConfig
			// file instead of a file originally on disk. See https://bugzilla.redhat.com/show_bug.cgi?id=1814397
			restore := false
			rpmNotFound, isOwned, err := isFileOwnedByRPMPkg(f.Path)
			switch {
			case isOwned:
				// File is owned by an rpm
				restore = true
			case !isOwned && err == nil:
				// Run on Fedora/RHEL - check whether the file exists in /usr/etc (on FCOS/RHCOS)
				if strings.HasPrefix(f.Path, "/etc") {
					if _, err := os.Stat(withUsrPath(f.Path)); err != nil {
						if !os.IsNotExist(err) {
							return err
						}
					} else {
						restore = true
					}
				}
			case rpmNotFound:
				// Run on non-Fedora/RHEL machine
				klog.Infof("Running on non-Fedora/RHEL machine, skip file restoration.")
			default:
				return err
			}

			if restore {
				if err := restorePath(f.Path); err != nil {
					return err
				}
				klog.V(2).Infof("Restored file %q", f.Path)
				continue
			}

			if delErr := os.Remove(origFileName(f.Path)); delErr != nil {
				return fmt.Errorf("deleting orig file %q: %w", origFileName(f.Path), delErr)
			}
		}

		// Check Systemd.Units.Dropins - don't remove the file if configuration has been converted into a dropin
		if dn.isPathInDropins(f.Path, &newIgnConfig.Systemd) {
			klog.Infof("Not removing file %q: replaced with systemd dropin", f.Path)
			continue
		}

		klog.V(2).Infof("Deleting stale config file: %s", f.Path)
		if err := os.Remove(f.Path); err != nil {
			newErr := fmt.Errorf("unable to delete %s: %w", f.Path, err)
			if !os.IsNotExist(err) {
				return newErr
			}
			// otherwise, just warn
			klog.Warningf("%v", newErr)
		}
		klog.Infof("Removed stale file %q", f.Path)
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
					klog.V(2).Infof("Removing file %q completely", path)
				} else if _, err := os.Stat(origFileName(path)); err == nil {
					if err := restorePath(path); err != nil {
						return err
					}
					klog.V(2).Infof("Restored file %q", path)
					continue
				}
				klog.V(2).Infof("Deleting stale systemd dropin file: %s", path)
				if err := os.Remove(path); err != nil {
					newErr := fmt.Errorf("unable to delete %s: %w", path, err)
					if !os.IsNotExist(err) {
						return newErr
					}
					// otherwise, just warn
					klog.Warningf("%v", newErr)
				}
				klog.Infof("Removed stale systemd dropin %q", path)
			}
		}
		path := filepath.Join(pathSystemd, u.Name)
		if _, ok := newUnitSet[path]; !ok {
			// since the unit doesn't exist anymore within the MachineConfig,
			// look to restore defaults here, so that symlinks are removed first
			// if the system has the service disabled
			// writeUnits() will catch units that still have references in other MCs
			if err := dn.presetUnit(u); err != nil {
				klog.Infof("Did not restore preset for %s (may not exist): %s", u.Name, err)
			}
			if _, err := os.Stat(noOrigFileStampName(path)); err == nil {
				if delErr := os.Remove(noOrigFileStampName(path)); delErr != nil {
					return fmt.Errorf("deleting noorig file stamp %q: %w", noOrigFileStampName(path), delErr)
				}
				klog.V(2).Infof("Removing file %q completely", path)
			} else if _, err := os.Stat(origFileName(path)); err == nil {
				if err := restorePath(path); err != nil {
					return err
				}
				klog.V(2).Infof("Restored file %q", path)
				continue
			}
			klog.V(2).Infof("Deleting stale systemd unit file: %s", path)
			if err := os.Remove(path); err != nil {
				newErr := fmt.Errorf("unable to delete %s: %w", path, err)
				if !os.IsNotExist(err) {
					return newErr
				}
				// otherwise, just warn
				klog.Warningf("%v", newErr)
			}
			klog.Infof("Removed stale systemd unit %q", path)
		}
	}

	// nolint:revive // because i disagree that returning this directly would be cleaner
	if err := dn.workaroundOcpBugs33694(); err != nil {
		return err
	}

	return nil
}

// Previous versions of the MCD leaked some enablement symlinks. We clean a
// known problematic subset of those here. See also:
// https://issues.redhat.com/browse/OCPBUGS-33694?focusedId=24917003#comment-24917003
func (dn *Daemon) workaroundOcpBugs33694() error {
	stalePaths := []string{
		"/etc/systemd/system/network-online.target.requires/node-valid-hostname.service",
		"/etc/systemd/system/network-online.target.wants/ovs-configuration.service",
	}
	for _, path := range stalePaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("error deleting %q: %w", path, err)
		} else if err == nil {
			klog.Infof("Removed stale symlink %q", path)
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
	klog.Infof("Enabled systemd units: %v", units)
	return nil
}

// disableUnits disables a set of systemd units via systemctl, if any fail all fails.
func (dn *Daemon) disableUnits(units []string) error {
	args := append([]string{"disable"}, units...)
	stdouterr, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("error disabling unit: %s", stdouterr)
	}
	klog.Infof("Disabled systemd units %v", units)
	return nil
}

// presetUnit resets a systemd unit to its preset via systemctl
func (dn *Daemon) presetUnit(unit ign3types.Unit) error {
	args := []string{"preset", unit.Name}
	stdouterr, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("error running preset on unit: %s", stdouterr)
	}
	klog.Infof("Preset systemd unit %q", unit.Name)
	return nil
}

// writeUnits writes the systemd units to disk
func (dn *Daemon) writeUnits(units []ign3types.Unit) error {
	var enabledUnits []string
	var disabledUnits []string

	isCoreOSVariant := dn.os.IsCoreOSVariant()
	systemdUnits, err := dn.listSystemdUnits()
	if err != nil {
		return err
	}

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
			// Only when a unit has contents should we attempt to enable or disable it.
			// See: https://issues.redhat.com/browse/OCPBUGS-56648
			_, unitExists := systemdUnits[u.Name]
			if unitHasContent(u) || unitExists {
				if *u.Enabled {
					enabledUnits = append(enabledUnits, u.Name)
				} else {
					disabledUnits = append(disabledUnits, u.Name)
				}
			} else {
				action := "disable"
				if *u.Enabled {
					action = "enable"
				}
				klog.Infof("Could not %s unit %q, because it has no contents, skipping", action, u.Name)
			}
		} else {
			if err := dn.presetUnit(u); err != nil {
				// Don't fail here, since a unit may have a dropin referencing a nonexisting actual unit
				klog.Infof("Could not reset unit preset for %s, skipping. (Error msg: %v)", u.Name, err)
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

func (dn *Daemon) listSystemdUnits() (result map[string]systemddbus.UnitStatus, err error) {
	conn, err := systemddbus.NewSystemdConnectionContext(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to system bus to list units: %w", err)
	}
	defer func() {
		conn.Close()
	}()

	units, err := conn.ListUnitsContext(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to list systemd units: %w", err)
	}

	result = make(map[string]systemddbus.UnitStatus)
	for _, unit := range units {
		result[unit.Name] = unit
	}
	return result, nil

}

// writeFiles writes the given files to disk.
// it doesn't fetch remote files and expects a flattened config file.
func (dn *Daemon) writeFiles(files []ign3types.File, skipCertificateWrite bool) error {
	return writeFiles(files, skipCertificateWrite)
}

// Ensures that both the SSH root directory (/home/core/.ssh) as well as any
// subdirectories are created with the correct (0700) permissions.
func createSSHKeyDir(authKeyDir string) error {
	klog.Infof("Creating missing SSH key dir at %q", authKeyDir)

	mkdir := func(dir string) error {
		return exec.Command("runuser", "-u", constants.CoreUserName, "--", "mkdir", "-m", "0700", "-p", dir).Run()
	}

	// Create the root SSH key directory (/home/core/.ssh) first (if there does not exist one).
	if _, err := os.Stat(constants.CoreUserSSHPath); os.IsNotExist(err) {
		if err := mkdir(filepath.Dir(constants.RHCOS8SSHKeyPath)); err != nil {
			return err
		}
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
	klog.Infof("Writing SSH keys to %q", authKeyPath)

	// Check the existence of the /home/core/.ssh dir before creating a new one
	// via runuser core by hand. Delete if the dir is created under the wrong
	// user (root), and let the MCD recreate it.
	// Serve as a workaround for https://issues.redhat.com/browse/OCPBUGS-11832
	if dirInfo, err := os.Stat(constants.CoreUserSSHPath); err == nil {
		uid := dirInfo.Sys().(*syscall.Stat_t).Uid
		if userInfo, err := user.LookupId(fmt.Sprint(uid)); err == nil {
			if userInfo.Username != constants.CoreUserName {
				if err := os.RemoveAll(constants.CoreUserSSHPath); err != nil {
					return fmt.Errorf("Failed to remove existing root user owned .ssh path %s:%w", constants.CoreUserSSHPath, err)
				}
			}
		} else {
			return fmt.Errorf("Failed to look up the user of the .ssh path %s:%w", constants.CoreUserSSHPath, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

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

	klog.V(2).Infof("Wrote SSH keys to %q", authKeyPath)

	return nil
}

// Set a given PasswdUser's Password Hash
func (dn *Daemon) SetPasswordHash(newUsers, oldUsers []ign3types.PasswdUser) error {
	// confirm that user exits
	klog.Info("Checking if absent users need to be disconfigured")

	// checking if old users need to be deconfigured
	deconfigureAbsentUsers(newUsers, oldUsers)

	var uErr user.UnknownUserError
	switch _, err := user.Lookup(constants.CoreUserName); {
	case err == nil:
	case errors.As(err, &uErr):
		klog.Info("core user does not exist, and creating users is not supported, so ignoring configuration specified for core user")
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
			return fmt.Errorf("Failed to reset password for %s: %s:%w", u.Name, out, err)
		}
		klog.Info("Password has been configured")
	}

	return nil
}

// Update the permission of the kubeconfig file located in /etc/kubenetes/kubeconfig
// Requested in https://issues.redhat.com/browse/OCPBUGS-15367
func (dn *Daemon) updateKubeConfigPermission() error {
	klog.Info("updating the permission of the kubeconfig to: 0o600")

	kubeConfigPath := "/etc/kubernetes/kubeconfig"
	// Checking if kubeconfig is existed in the expected path:
	if _, err := os.Stat(kubeConfigPath); err == nil {
		if err := os.Chmod(kubeConfigPath, 0o600); err != nil {
			return fmt.Errorf("Failed to reset permission for %s:%w", kubeConfigPath, err)
		}
	} else {
		return fmt.Errorf("Cannot stat %s: %w", kubeConfigPath, err)
	}
	return nil
}

// Determines if we should use the new SSH key path
// (/home/core/.ssh/authorized_keys.d/ignition) or the old SSH key path
// (/home/core/.ssh/authorized_keys)
func (dn *Daemon) useNewSSHKeyPath() bool {
	return dn.os.IsEL9() || dn.os.IsEL10() || dn.os.IsFCOS() || dn.os.IsSCOS()
}

// Update a given PasswdUser's SSHKey
func (dn *Daemon) updateSSHKeys(newUsers, oldUsers []ign3types.PasswdUser) error {
	klog.Info("updating SSH keys")

	// Checking to see if absent users need to be deconfigured
	deconfigureAbsentUsers(newUsers, oldUsers)

	var uErr user.UnknownUserError
	switch _, err := user.Lookup(constants.CoreUserName); {
	case err == nil:
	case errors.As(err, &uErr):
		klog.Info("core user does not exist, and creating users is not supported, so ignoring configuration specified for core user")
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

func deconfigureAbsentUsers(newUsers, oldUsers []ign3types.PasswdUser) {
	for _, oldUser := range oldUsers {
		if !isUserPresent(oldUser, newUsers) {
			klog.Infof("Absent user detected, deconfiguring the password for user %s\n", oldUser.Name)
			deconfigureUser(oldUser)
		}
	}
}

func isUserPresent(user ign3types.PasswdUser, userList []ign3types.PasswdUser) bool {
	for _, u := range userList {
		if u.Name == user.Name {
			return true
		}
	}
	return false
}

func deconfigureUser(user ign3types.PasswdUser) error {
	// clear out password
	pwhash := ""
	user.PasswordHash = &pwhash

	if out, err := exec.Command("usermod", "-p", *user.PasswordHash, user.Name).CombinedOutput(); err != nil {
		return fmt.Errorf("Failed to change password for %s: %s:%w", user.Name, out, err)
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
		klog.Info("SELinux is not enforcing")
	}

	systemdPodmanArgs := []string{"--unit", "machine-config-daemon-update-rpmostree-via-container", "-p", "EnvironmentFile=-/etc/mco/proxy.env", "--collect", "--wait", "--", "podman"}
	pullArgs := append([]string{}, systemdPodmanArgs...)
	pullArgs = append(pullArgs, "pull", "--authfile", "/var/lib/kubelet/config.json")
	if !podmanSupportsSigstore() {
		pullArgs = append(pullArgs, "--signature-policy", "/etc/machine-config-daemon/policy-for-old-podman.json")
	}
	pullArgs = append(pullArgs, target)
	err = runCmdSync("systemd-run", pullArgs...)
	if err != nil {
		return err
	}

	runArgs := append([]string{}, systemdPodmanArgs...)
	runArgs = append(runArgs, "run", "--env-file", "/etc/mco/proxy.env", "--privileged", "--pid=host", "--net=host", "--rm", "-v", "/:/run/host", target, "rpm-ostree", "ex", "deploy-from-self", "/run/host")
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

// queueRevertKernelSwap undoes the layering of the RT kernel or kernel-64k hugepages
func (dn *Daemon) queueRevertKernelSwap() error {
	booted, _, err := dn.NodeUpdaterClient.GetBootedAndStagedDeployment()
	if err != nil {
		return err
	}

	// Before we attempt to do an OS update, we must remove the kernel-rt or kernel-64k switch
	// because in the case of updating from RHEL8 to RHEL9, the kernel packages are
	// OS version dependent.  See also https://github.com/coreos/rpm-ostree/issues/2542
	// (Now really what we want to do here is something more like rpm-ostree override reset --kernel
	//  i.e. the inverse of https://github.com/coreos/rpm-ostree/pull/4322 so that
	//  we're again not hardcoding even the prefix of kernel packages)
	kernelOverrides := []string{}
	kernelExtLayers := []string{}
	for _, removal := range booted.RequestedBaseRemovals {
		if removal == "kernel" || strings.HasPrefix(removal, "kernel-") {
			kernelOverrides = append(kernelOverrides, removal)
		}
	}
	for _, pkg := range booted.RequestedPackages {
		if strings.HasPrefix(pkg, "kernel-rt-") || strings.HasPrefix(pkg, "kernel-64k-") {
			kernelExtLayers = append(kernelExtLayers, pkg)
		}
	}
	// We *only* do this switch if the node has done a switch from kernel -> kernel-rt or kernel-64k.
	// We don't want to override any machine-local hotfixes for the kernel package.
	// Implicitly in this we don't really support machine-local hotfixes for kernel-rt or kernel-64k.
	// The only sane way to handle that is declarative drop-ins, but really we want to
	// just go to deploying pre-built images and not doing per-node mutation with rpm-ostree
	// at all.
	switch {
	case len(kernelOverrides) > 0 && len(kernelExtLayers) > 0:
		args := []string{"override", "reset"}
		args = append(args, kernelOverrides...)
		for _, pkg := range kernelExtLayers {
			args = append(args, constants.RPMOSTreeUninstallArg, pkg)
		}
		if err := runRpmOstree(args...); err != nil {
			return err
		}
	case len(kernelOverrides) > 0 || len(kernelExtLayers) > 0:
		klog.Infof("notice: detected %d kernel overrides and %d kernel-rt or kernel-64k layers", len(kernelOverrides), len(kernelExtLayers))
	default:
		klog.Infof("No kernel overrides or replacement detected")
	}

	return nil
}

// updateLayeredOS updates the system OS to the one specified in newConfig
func (dn *Daemon) updateLayeredOS(config *mcfgv1.MachineConfig) error {
	newURL := config.Spec.OSImageURL
	klog.Infof("Updating OS to layered image %q", newURL)

	newEnough, err := dn.NodeUpdaterClient.IsNewEnoughForLayering()
	if err != nil {
		return err
	}
	// If the host isn't new enough to understand the new container model natively, run as a privileged container.
	// See https://github.com/coreos/rpm-ostree/pull/3961 and https://issues.redhat.com/browse/MCO-356
	// This currently will incur a double reboot; see https://github.com/coreos/rpm-ostree/issues/4018
	if !newEnough {
		logSystem("rpm-ostree is not new enough for layering; forcing an update via container")
		return dn.InplaceUpdateViaNewContainer(newURL)
	}

	isPisConfigured, err := dn.isPinnedImageSetConfigured()
	if err != nil {
		// Ignore the error and default to remote pull
		klog.Errorf("Failed to determine if pinned image set is configured: %v", err)
	}

	// If PIS is configured check if the image is locally present. If so, rebase using
	// the local image
	var podmanImageInfo *PodmanImageInfo
	if isPisConfigured {
		if podmanImageInfo, err = dn.podmanInterface.GetPodmanImageInfoByReference(newURL); err != nil {
			return err
		}
	}

	// For image mode status reporting we need the node's MCP association to populate its MCN
	imageModeStatusReportingEnabled := dn.fgHandler != nil && dn.fgHandler.Enabled(features.FeatureGateImageModeStatusReporting)
	pool := ""
	if imageModeStatusReportingEnabled {
		pool, err = helpers.GetPrimaryPoolNameForMCN(dn.mcpLister, dn.node)
		if err != nil {
			return err
		}
	}

	if podmanImageInfo != nil {
		if err := dn.NodeUpdaterClient.RebaseLayeredFromContainerStorage(podmanImageInfo); err != nil {
			return fmt.Errorf("failed to update OS from local storage: %s: %w", newURL, err)
		}
	} else {
		// Report ImagePulledFromRegistry condition as unknown (pulling)
		if imageModeStatusReportingEnabled {
			err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
				&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeImagePulledFromRegistry, Reason: string(mcfgv1.MachineConfigNodeImagePulledFromRegistry), Message: fmt.Sprintf("Pulling OS image %s from registry", newURL)},
				nil,
				metav1.ConditionUnknown,
				metav1.ConditionFalse,
				dn.node,
				dn.mcfgClient,
				dn.fgHandler,
				pool,
			)
			if err != nil {
				klog.Errorf("Error setting ImagePulledFromRegistry condition to unknown: %v", err)
			}
		}

		// Workaround for OCPBUGS-43406, retry the remote rebase with backoff,
		// such that if we happen to update while the CoreDNS pod is being restarted,
		// the next retry should succeed if no other issues are present.
		backoff := wait.Backoff{
			Duration: 5 * time.Second,
			Factor:   2,
			Steps:    5,
		}

		if err := wait.ExponentialBackoff(backoff, func() (bool, error) {
			if err := dn.NodeUpdaterClient.RebaseLayered(newURL); err != nil {
				klog.Warningf("Failed to update OS to %s (will retry): %v", newURL, err)
				return false, nil
			}
			return true, nil
		}); err != nil {
			// Report ImagePulledFromRegistry condition as false (failed)
			if imageModeStatusReportingEnabled {
				mcnErr := upgrademonitor.GenerateAndApplyMachineConfigNodes(
					&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeImagePulledFromRegistry, Reason: string(mcfgv1.MachineConfigNodeImagePulledFromRegistry), Message: fmt.Sprintf("Failed to pull OS image %s from registry: %v", newURL, err)},
					nil,
					metav1.ConditionFalse,
					metav1.ConditionFalse,
					dn.node,
					dn.mcfgClient,
					dn.fgHandler,
					pool,
				)
				if mcnErr != nil {
					klog.Errorf("Error setting ImagePulledFromRegistry condition to false: %v", mcnErr)
				}
			}
			return fmt.Errorf("Failed to update OS to %s after retries: %w", newURL, err)
		}

		// Report ImagePulledFromRegistry condition as true (success)
		if imageModeStatusReportingEnabled {
			err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
				&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeImagePulledFromRegistry, Reason: string(mcfgv1.MachineConfigNodeImagePulledFromRegistry), Message: fmt.Sprintf("Successfully pulled OS image %s from registry", newURL)},
				nil,
				metav1.ConditionTrue,
				metav1.ConditionFalse,
				dn.node,
				dn.mcfgClient,
				dn.fgHandler,
				pool,
			)
			if err != nil {
				klog.Errorf("Error setting ImagePulledFromRegistry condition to true: %v", err)
			}
		}
	}

	return nil
}

func (dn *Daemon) isPinnedImageSetConfigured() (bool, error) {
	if dn.fgHandler == nil || !dn.fgHandler.Enabled(features.FeatureGatePinnedImages) || dn.node == nil || dn.mcpLister == nil {
		// Two options:
		// - PIS is not enabled
		// - MCD first boot run: No connection to the cluster and node not populated -> Cannot check PIS config
		return false, nil
	}

	// PIS is enabled. Check if it's configured in any of its pools
	pools, _, err := helpers.GetPoolsForNode(dn.mcpLister, dn.node)
	if err != nil {
		return false, fmt.Errorf("failed to get pools for node %q: %w", dn.node.Name, err)
	}

	for _, pool := range pools {
		if pool.Spec.PinnedImageSets != nil && len(pool.Spec.PinnedImageSets) > 0 {
			return true, nil
		}
	}
	// No pools with PIS configured
	return false, nil
}

// TODO: Delete this function to always consume the CommandRunner interface instance
// Tracking story: https://issues.redhat.com/browse/MCO-1925
//
//	Conserved the old signature to avoid a big footprint bugfix with this change
//
// Synchronously invoke a command, writing its stdout to our stdout,
// and gathering stderr into a buffer which will be returned in err
// in case of error.
func runCmdSync(cmdName string, args ...string) error {
	klog.Infof("Running: %s %s", cmdName, strings.Join(args, " "))
	cmd := exec.Command(cmdName, args...)
	var stderr bytes.Buffer
	cmd.Stdout = os.Stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("error running %s %s: %s: %w", cmdName, strings.Join(args, " "), string(stderr.Bytes()), err)
	}

	return nil
}

var (
	podmanSigstoreSupported      sync.Once
	podmanSigstoreSupportedValue bool
)

func podmanSupportsSigstore() bool {
	podmanSigstoreSupported.Do(func() {
		// https://issues.redhat.com/browse/OCPBUGS-38809 failed for base image 4.11 or older, OCP 4.12 is with podman 4.4.1
		// returns false if podman version is less than 4.4.1
		cmd := exec.Command("podman", "version", "-f", "{{.APIVersion}}")
		out, err := cmd.CombinedOutput()
		if err != nil {
			klog.Errorf("failed to run podman version: %v", err)
			podmanSigstoreSupportedValue = false
			return
		}
		sigstorePodman := "4.4.1"
		// Example version format: 5.3.0-rc1
		imgPodmanVersion, err := semver.NewVersion(strings.TrimSpace(string(out)))
		if err != nil {
			klog.Errorf("failed to parse podman version: %v", err)
			podmanSigstoreSupportedValue = false
			return
		}
		podmanSigstoreSupportedValue = imgPodmanVersion.Compare(*semver.New(sigstorePodman)) >= 0
	})
	return podmanSigstoreSupportedValue
}

// Log a message to the systemd journal as well as our stdout
func logSystem(format string, a ...interface{}) {
	message := fmt.Sprintf(format, a...)
	message = fmt.Sprintf("%q", message)
	klog.Info(message)
	// Since we're chrooted into the host rootfs with /run mounted,
	// we can just talk to the journald socket.  Doing this as a
	// subprocess rather than talking to journald in process since
	// I worry about the golang library having a connection pre-chroot.
	logger := exec.Command("logger")

	var log bytes.Buffer
	log.WriteString(fmt.Sprintf("machine-config-daemon[%d]: %s", os.Getpid(), message))

	logger.Stdin = &log
	if err := logger.Run(); err != nil {
		klog.Errorf("failed to invoke logger: %v", err)
	}
}

func (dn *Daemon) catchIgnoreSIGTERM() {
	dn.updateActiveLock.Lock()
	defer dn.updateActiveLock.Unlock()
	if dn.updateActive {
		return
	}
	klog.Info("Adding SIGTERM protection")
	dn.updateActive = true
	dn.maybeEventf(corev1.EventTypeNormal, "AddSigtermProtection", "Adding SIGTERM protection")
}

func (dn *Daemon) CancelSIGTERM() {
	dn.updateActiveLock.Lock()
	defer dn.updateActiveLock.Unlock()
	if dn.updateActive {
		klog.Info("Removing SIGTERM protection")
		dn.maybeEventf(corev1.EventTypeNormal, "RemoveSigtermProtection", "Removing SIGTERM protection")
		dn.updateActive = false
	}
}

// reboot is the final step. it tells systemd-logind to reboot the machine,
// cleans up the agent's connections
// on failure to reboot, it throws an error and waits for the operator to try again
func (dn *Daemon) reboot(rationale string) error {
	// Now that everything is done, avoid delaying shutdown.
	dn.CancelSIGTERM()
	dn.Close()

	if dn.skipReboot {
		return nil
	}

	// We'll only have a recorder if we're cluster driven
	if dn.nodeWriter != nil {
		dn.nodeWriter.Eventf(corev1.EventTypeNormal, "Reboot", rationale)
	}
	logSystem("initiating reboot: %s", rationale)

	if dn.node != nil {
		Rebooting := make(map[string]string)
		Rebooting[constants.MachineConfigDaemonPostConfigAction] = constants.MachineConfigDaemonStateRebooting
		_, err := dn.nodeWriter.SetAnnotations(Rebooting)
		if err != nil {
			klog.Errorf("Error setting post config action annotation %v", err)
		}
	}

	// reboot, executed async via systemd-run so that the reboot command is executed
	// in the context of the host asynchronously from us
	// We're not returning the error from the reboot command as it can be terminated by
	// the system itself with signal: terminated. We can't catch the subprocess termination signal
	// either, we just have one for the MCD itself.
	rebootCmd := rebootCommand(rationale, dn.os.IsCoreOSVariant())
	if err := rebootCmd.Run(); err != nil {
		logSystem("failed to run reboot: %v", err)
		mcdRebootErr.Inc()
		return fmt.Errorf("reboot command failed, something is seriously wrong")
	}
	// if we're here, reboot went through successfully, so we set rebootQueued
	// and we wait for GracefulNodeShutdown
	dn.rebootQueued = true
	logSystem("reboot successful")

	return nil
}

//nolint:gocyclo
func (dn *CoreOSDaemon) applyLayeredOSChanges(mcDiff machineConfigDiff, oldConfig, newConfig *mcfgv1.MachineConfig) (retErr error) {
	// Override the computed diff if the booted state differs from the oldConfig
	// https://issues.redhat.com/browse/OCPBUGS-2757
	if mcDiff.osUpdate && dn.bootedOSImageURL == newConfig.Spec.OSImageURL {
		klog.Infof("Already in desired image %s", newConfig.Spec.OSImageURL)
		mcDiff.osUpdate = false
	}

	var osExtensionsContentDir string
	var err error

	// For image mode status reporting we need the node's MCP association to populate its MCN
	imageModeStatusReportingEnabled := dn.fgHandler != nil && dn.fgHandler.Enabled(features.FeatureGateImageModeStatusReporting)
	pool := ""
	if imageModeStatusReportingEnabled {
		pool, err = helpers.GetPrimaryPoolNameForMCN(dn.mcpLister, dn.node)
		if err != nil {
			return err
		}
	}

	if newConfig.Spec.BaseOSExtensionsContainerImage != "" && (mcDiff.osUpdate || mcDiff.extensions || mcDiff.kernelType) && !mcDiff.oclEnabled {
		// TODO(jkyros): the original intent was that we use the extensions container as a service, but that currently results
		// in a lot of complexity due to boostrap and firstboot where the service isn't easily available, so for now we are going
		// to extract them to disk like we did previously.

		// Report ImagePulledFromRegistry condition as unknown (pulling)
		if imageModeStatusReportingEnabled {
			err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
				&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeImagePulledFromRegistry, Reason: string(mcfgv1.MachineConfigNodeImagePulledFromRegistry), Message: fmt.Sprintf("Pulling extensions image %s from registry", newConfig.Spec.BaseOSExtensionsContainerImage)},
				nil,
				metav1.ConditionUnknown,
				metav1.ConditionFalse,
				dn.node,
				dn.mcfgClient,
				dn.fgHandler,
				pool,
			)
			if err != nil {
				klog.Errorf("Error setting ImagePulledFromRegistry condition to unknown: %v", err)
			}
		}

		if osExtensionsContentDir, err = dn.ExtractExtensionsImage(newConfig.Spec.BaseOSExtensionsContainerImage); err != nil {
			// Report ImagePulledFromRegistry condition as false (failed)
			if imageModeStatusReportingEnabled {
				err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
					&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeImagePulledFromRegistry, Reason: string(mcfgv1.MachineConfigNodeImagePulledFromRegistry), Message: fmt.Sprintf("Failed to pull extensions image %s from registry: %v", newConfig.Spec.BaseOSExtensionsContainerImage, err)},
					nil,
					metav1.ConditionFalse,
					metav1.ConditionFalse,
					dn.node,
					dn.mcfgClient,
					dn.fgHandler,
					pool,
				)
				if err != nil {
					klog.Errorf("Error setting ImagePulledFromRegistry condition to false: %v", err)
				}
			}
			return err
		}

		// Report ImagePulledFromRegistry condition as true (success)
		if imageModeStatusReportingEnabled {
			err := upgrademonitor.GenerateAndApplyMachineConfigNodes(
				&upgrademonitor.Condition{State: mcfgv1.MachineConfigNodeImagePulledFromRegistry, Reason: string(mcfgv1.MachineConfigNodeImagePulledFromRegistry), Message: fmt.Sprintf("Successfully pulled extensions image %s from registry", newConfig.Spec.BaseOSExtensionsContainerImage)},
				nil,
				metav1.ConditionTrue,
				metav1.ConditionFalse,
				dn.node,
				dn.mcfgClient,
				dn.fgHandler,
				pool,
			)
			if err != nil {
				klog.Errorf("Error setting ImagePulledFromRegistry condition to true: %v", err)
			}
		}

		// Delete extracted OS image once we are done.
		defer os.RemoveAll(osExtensionsContentDir)

		if err := addExtensionsRepo(osExtensionsContentDir); err != nil {
			return err
		}
		defer os.Remove(extensionsRepo)
	}

	// Always clean up pending, because the RT kernel switch logic below operates on booted,
	// not pending.
	if err := removePendingDeployment(); err != nil {
		return fmt.Errorf("failed to remove pending deployment: %w", err)
	}

	defer func() {
		// Operations performed by rpm-ostree on the booted system are available
		// as staged deployment. It gets applied only when we reboot the system.
		// In case of an error during any rpm-ostree transaction, removing pending deployment
		// should be sufficient to discard any applied changes.
		if retErr != nil {
			// Print out the error now so that if we fail to cleanup -p, we don't lose it.
			klog.Infof("Rolling back applied changes to OS due to error: %v", retErr)
			if err := removePendingDeployment(); err != nil {
				errs := kubeErrs.NewAggregate([]error{err, retErr})
				retErr = fmt.Errorf("error removing staged deployment: %w", errs)
				return
			}
		}
	}()

	if !mcDiff.oclEnabled {
		// If we have an OS update *or* a kernel type change, then we must undo the kernel swap
		// enablement.
		if mcDiff.osUpdate || mcDiff.kernelType {
			if err := dn.queueRevertKernelSwap(); err != nil {
				mcdPivotErr.Inc()
				return err
			}
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

	if mcDiff.kargs {
		if err := dn.updateKernelArguments(oldConfig.Spec.KernelArguments, newConfig.Spec.KernelArguments); err != nil {
			return err
		}
	}

	// If on-cluster layering is enabled, we can skip the rest of this process.
	if mcDiff.oclEnabled {
		return nil
	}

	// Switch to real time kernel
	if mcDiff.osUpdate || mcDiff.kernelType {
		if err := dn.switchKernel(oldConfig, newConfig); err != nil {
			return err
		}
	}

	// Apply extensions
	return dn.applyExtensions(oldConfig, newConfig)
}

// Enables the revert layering systemd unit.
//
// To enable the unit, we perform the following operations:
// 1. Retrieve the ControllerConfig.
// 2. Generate the Ignition config from the ControllerConfig and the supplied MachineConfig.
// 3. Writes the new systemd unit and its needed files to disk and enable it.
func (dn *Daemon) enableRevertSystemdUnit(newConfig *mcfgv1.MachineConfig) error {
	ctrlcfg, err := dn.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get controllerconfig %s: %w", ctrlcommon.ControllerConfigName, err)
	}

	rs, err := runtimeassets.NewRevertService(ctrlcfg, newConfig)
	if err != nil {
		return err
	}

	revertIgn, err := rs.Ignition()
	if err != nil {
		return fmt.Errorf("could not create %s: %w", runtimeassets.RevertServiceName, err)
	}

	if err := dn.writeFiles(revertIgn.Storage.Files, false); err != nil {
		return fmt.Errorf("could not write files for %s: %w", runtimeassets.RevertServiceName, err)
	}

	if err := dn.writeUnits(revertIgn.Systemd.Units); err != nil {
		return fmt.Errorf("could not write %s: %w", runtimeassets.RevertServiceName, err)
	}

	return nil
}

// Disables the revert layering systemd unit, if it is present.
//
// To disable the unit, it performs the following operations:
// 1. Checks for the presence of the systemd unit file. If not present, it will
// no-op.
// 2. If the unit file is present, it will disable the unit using the default
// MCD code paths for that purpose.
// 3. It will ensure that the unit file is removed as well as the file that the
// Ignition config was written to.
func (dn *Daemon) disableRevertSystemdUnit() error {
	unitPath := filepath.Join(pathSystemd, runtimeassets.RevertServiceName)

	unitPathExists, err := fileExists(unitPath)
	if err != nil {
		return fmt.Errorf("could not determine if service %q exists: %w", runtimeassets.RevertServiceName, err)
	}

	// If the unit path does not exist, there is nothing to do.
	if !unitPathExists {
		return nil
	}

	// If we've reached this point, we know that the unit file is still present,
	// which means that the unit may still be enabled.
	if err := dn.disableUnits([]string{runtimeassets.RevertServiceName}); err != nil {
		return err
	}

	filesToRemove := []string{
		unitPath,
		runtimeassets.RevertServiceMachineConfigFile,
		runtimeassets.RevertServiceProxyFile,
	}

	// systemd removes the unit file, but there is no harm in calling
	// os.RemoveAll() since it will return nil if the file does not exist.
	for _, fileToRemove := range filesToRemove {
		if err := os.RemoveAll(fileToRemove); err != nil {
			return err
		}
	}

	return nil
}

// If the provided image is empty, then the OSImageURL value on the
// MachineConfig should take precedence. Otherwise, if the provided image is
// set, then it should take precedence over the OSImageURL value. This is only
// used for OCL OS updates and should not be used for anything else.
func canonicalizeMachineConfigImage(img string, mc *mcfgv1.MachineConfig) *mcfgv1.MachineConfig {
	copied := mc.DeepCopy()

	if img == "" {
		return copied
	}

	copied.Spec.OSImageURL = img

	return copied
}

// reportMachineNodeDegradeStatus Given the final error, and the used pool, of a node update the
// method sets the [mcfgv1.MachineConfigNodeNodeDegraded] condition status in the status of the MCN.
// If the error is not nil the condition status is set to [metav1.ConditionTrue] and the condition
// message is formatted accordingly to include the error message. The condition is otherwise set to
// [metav1.ConditionFalse].
func (dn *Daemon) reportMachineNodeDegradeStatus(err error, pool string) {
	if dn.node == nil {
		return
	}
	condition := &upgrademonitor.Condition{
		State:  mcfgv1.MachineConfigNodeNodeDegraded,
		Reason: string(mcfgv1.MachineConfigNodeNodeDegraded),
	}
	status := metav1.ConditionFalse
	if err == nil {
		condition.Message = fmt.Sprintf("Node %s upgrade succeeded", dn.node.GetName())
	} else {
		condition.Message = fmt.Sprintf("Node %s upgrade failure. %v", dn.node.GetName(), err)
		status = metav1.ConditionTrue
	}

	if applyErr := upgrademonitor.GenerateAndApplyMachineConfigNodes(
		condition,
		nil,
		status,
		metav1.ConditionFalse,
		dn.node,
		dn.mcfgClient,
		dn.fgHandler,
		pool,
	); applyErr != nil {
		klog.Errorf("Error updating MCN degraded status condition %v", applyErr)
	}
}
