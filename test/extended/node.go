package extended

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	"github.com/openshift/machine-config-operator/test/extended/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"

	expect "github.com/google/goexpect"
	o "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Node is used to handle node OCP resources
type Node struct {
	Resource
	eventCheckpoint time.Time
}

// NodeList handles list of nodes
type NodeList struct {
	ResourceList
}

// Struct that stores data usage information in bytes
type SpaceUsage struct {
	Used  int64
	Avail int64
}

// NewNode construct a new node struct
func NewNode(oc *exutil.CLI, name string) *Node {
	return &Node{*NewResource(oc, "node", name), time.Time{}}
}

// NewNodeList construct a new node list struct to handle all existing nodes
func NewNodeList(oc *exutil.CLI) *NodeList {
	return &NodeList{*NewResourceList(oc, "node")}
}

// String implements the Stringer interface
func (n Node) String() string {
	return n.GetName()
}

// DebugNodeWithChroot creates a debugging session of the node with chroot
func (n *Node) DebugNodeWithChroot(cmd ...string) (string, error) {
	var (
		out        string
		err        error
		numRetries = 3
	)
	n.oc.NotShowInfo()
	defer n.oc.SetShowInfo()

	for i := 0; i < numRetries; i++ {
		if i > 0 {
			logger.Infof("Error happened: %s.\nRetrying command. Num retries: %d", err, i)
		}
		out, err = exutil.DebugNodeWithChroot(n.oc, n.name, cmd...)
		if err == nil {
			return out, nil
		}
	}

	return out, err
}

// DebugNodeWithChrootStd creates a debugging session of the node with chroot and only returns separated stdout and stderr
func (n *Node) DebugNodeWithChrootStd(cmd ...string) (string, string, error) {
	var (
		stdout     string
		stderr     string
		err        error
		numRetries = 3
	)

	setErr := quietSetNamespacePrivileged(n.oc, n.oc.Namespace())
	if setErr != nil {
		return "", "", setErr
	}

	cargs := []string{"node/" + n.GetName(), "--", "chroot", "/host"}
	cargs = append(cargs, cmd...)

	for i := 0; i < numRetries; i++ {
		if i > 0 {
			logger.Infof("Error happened: %s.\nRetrying command. Num retries: %d", err, i)
		}
		stdout, stderr, err = n.oc.Run("debug").Args(cargs...).Outputs()
		if err == nil {
			return stdout, stderr, nil
		}
	}

	recErr := quietRecoverNamespaceRestricted(n.oc, n.oc.Namespace())
	if recErr != nil {
		return "", "", recErr
	}

	return stdout, stderr, err
}

// DebugNodeWithOptions launch debug container with options e.g. --image
func (n *Node) DebugNodeWithOptions(options []string, cmd ...string) (string, error) {
	var (
		out        string
		err        error
		numRetries = 3
	)

	for i := 0; i < numRetries; i++ {
		if i > 0 {
			logger.Infof("Error happened: %s.\nRetrying command. Num retries: %d", err, i)
		}
		out, err = exutil.DebugNodeWithOptions(n.oc, n.name, options, cmd...)
		if err == nil {
			return out, nil
		}
	}

	return out, err
}

// DebugNode creates a debugging session of the node
func (n *Node) DebugNode(cmd ...string) (string, error) {
	return exutil.DebugNode(n.oc, n.name, cmd...)
}

// DeleteLabel removes the given label from the node
func (n *Node) DeleteLabel(label string) (string, error) {
	logger.Infof("Delete label %s from node %s", label, n.GetName())
	return exutil.DeleteLabelFromNode(n.oc, n.name, label)
}

// WaitForLabelRemoved waits until the given label is not present in the node.
func (n *Node) WaitForLabelRemoved(label string) error {
	logger.Infof("Waiting for label %s to be removed from node %s", label, n.GetName())

	immediate := true
	waitErr := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, 10*time.Minute, immediate, func(_ context.Context) (bool, error) {
		labels, err := n.Get(`{.metadata.labels}`)
		if err != nil {
			logger.Infof("Error waiting for labels to be removed:%v, and try next round", err)
			return false, nil
		}
		labelsMap := JSON(labels)
		label, err := labelsMap.GetSafe(label)
		if err == nil && !label.Exists() {
			logger.Infof("Label %s has been removed from node %s", label, n.GetName())
			return true, nil
		}
		return false, nil
	})

	if waitErr != nil {
		logger.Errorf("Timeout while waiting for label %s to be delete from node %s. Error: %s",
			label,
			n.GetName(),
			waitErr)
	}

	return waitErr
}

// GetMachineConfigDaemon returns the name of the ConfigDaemon pod for this node
func (n *Node) GetMachineConfigDaemon() string {
	machineConfigDaemon, err := exutil.GetPodName(n.oc, "openshift-machine-config-operator", "k8s-app=machine-config-daemon", n.name)
	o.Expect(err).NotTo(o.HaveOccurred())
	return machineConfigDaemon
}

// GetNodeHostname returns the cluster node hostname
func (n *Node) GetNodeHostname() (string, error) {
	return exutil.GetNodeHostname(n.oc, n.name)
}

// ForceReapplyConfiguration create the file `/run/machine-config-daemon-force` in the node
// in order to force MCO to reapply the current configuration
func (n *Node) ForceReapplyConfiguration() error {
	logger.Infof("Forcing reapply configuration in node %s", n.GetName())
	_, err := n.DebugNodeWithChroot("touch", "/run/machine-config-daemon-force")

	return err
}

// GetUnitStatus executes `systemctl status` command on the node and returns the output
func (n *Node) GetUnitStatus(unitName string) (string, error) {
	return n.DebugNodeWithChroot("systemctl", "status", unitName)
}

// UnmaskService executes `systemctl unmask` command on the node and returns the output
func (n *Node) UnmaskService(svcName string) (string, error) {
	return n.DebugNodeWithChroot("systemctl", "unmask", svcName)
}

// GetUnitProperties executes `systemctl show $unitname`, can be used to checkout service dependency
func (n *Node) GetUnitProperties(unitName string, args ...string) (string, error) {
	cmd := append([]string{"systemctl", "show", unitName}, args...)
	stdout, _, err := n.DebugNodeWithChrootStd(cmd...)
	return stdout, err
}

// GetUnitActiveEnterTime returns the last time when the unit entered in active status
func (n *Node) GetUnitActiveEnterTime(unitName string) (time.Time, error) {
	cmdOut, err := n.GetUnitProperties(unitName, "--timestamp=unix", "-P", "ActiveEnterTimestamp")
	if err != nil {
		return time.Time{}, err
	}

	logger.Infof("Active enter time output: [%s]", cmdOut)

	// The output should have this format
	// sh-5.1# systemctl show crio.service --timestamp=unix -P ActiveEnterTimestamp
	// @1709918801
	r := regexp.MustCompile(`^\@(?P<unix_timestamp>[0-9]+)$`)
	match := r.FindStringSubmatch(cmdOut)
	if len(match) == 0 {
		msg := fmt.Sprintf("Wrong property format. Expected a format like '@1709918801', but got '%s'", cmdOut)
		logger.Info(msg)
		return time.Time{}, errors.New(msg)
	}
	unixTimeIndex := r.SubexpIndex("unix_timestamp")
	unixTime := match[unixTimeIndex]

	iUnixTime, err := strconv.ParseInt(unixTime, 10, 64)
	if err != nil {
		return time.Time{}, err
	}

	activeEnterTime := time.Unix(iUnixTime, 0)

	logger.Infof("Unit %s ActiveEnterTimestamp %s", unitName, activeEnterTime)

	return activeEnterTime, nil
}

// GetUnitActiveEnterTime returns the last time when the unit entered in active status
// Parse ExecReload={ path=/bin/kill ; argv[]=/bin/kill -s HUP $MAINPID ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }
// If the service was never reloaded, then we return an empty time.Time{} and no error.
func (n *Node) GetUnitExecReloadStartTime(unitName string) (time.Time, error) {
	cmdOut, err := n.GetUnitProperties(unitName, "--timestamp=unix", "-P", "ExecReload")
	if err != nil {
		return time.Time{}, err
	}

	logger.Infof("Reload start time output: [%s]", cmdOut)

	// The output should have this format
	// sh-5.1# systemctl show crio.service --timestamp=unix -P ExecReload
	// @1709918801
	r := regexp.MustCompile(`start_time=\[(?P<unix_timestamp>@[0-9]+|n\/a)\]`)
	match := r.FindStringSubmatch(cmdOut)
	if len(match) == 0 {
		msg := fmt.Sprintf("Wrong property format. Expected a format like 'start_time=[@1709918801]', but got '%s'", cmdOut)
		logger.Info(msg)
		return time.Time{}, errors.New(msg)
	}
	unixTimeIndex := r.SubexpIndex("unix_timestamp")
	unixTime := match[unixTimeIndex]

	if unixTime == "n/a" {
		logger.Infof("Crio was never reloaded.  Reload Start Time = %s", unixTime)
		return time.Time{}, nil
	}

	iUnixTime, err := strconv.ParseInt(strings.Replace(unixTime, "@", "", 1), 10, 64)
	if err != nil {
		return time.Time{}, err
	}

	activeEnterTime := time.Unix(iUnixTime, 0)

	logger.Infof("Unit %s ExecReload start time %s", unitName, activeEnterTime)

	return activeEnterTime, nil
}

// GetUnitDependencies executes `systemctl list-dependencies` with arguments like --before --after
func (n *Node) GetUnitDependencies(unitName string, opts ...string) (string, error) {
	options := []string{"systemctl", "list-dependencies", unitName}
	if len(opts) > 0 {
		options = append(options, opts...)
	}
	return n.DebugNodeWithChroot(options...)
}

// IsUnitActive check unit is active or inactive
func (n *Node) IsUnitActive(unitName string) bool {
	output, _, err := n.DebugNodeWithChrootStd("systemctl", "is-active", unitName)
	if err != nil {
		logger.Errorf("Get unit state for %s failed: %v", unitName, err)
		return false
	}
	logger.Infof("Unit %s state is: %s", unitName, output)
	return output == "active"
}

// IsUnitEnabled check unit enablement state is enabled/enabled-runtime or others e.g. disabled
func (n *Node) IsUnitEnabled(unitName string) bool {
	output, _, err := n.DebugNodeWithChrootStd("systemctl", "is-enabled", unitName)
	if err != nil {
		logger.Errorf("Get unit enablement state for %s failed: %v", unitName, err)
		return false
	}
	logger.Infof("Unit %s enablement state is: %s ", unitName, output)
	return strings.HasPrefix(output, "enabled")
}

// GetRpmOstreeStatus returns the rpm-ostree status in json format
func (n Node) GetRpmOstreeStatus(asJSON bool) (string, error) {
	args := []string{"rpm-ostree", "status"}
	if asJSON {
		args = append(args, "--json")
	}
	stringStatus, _, err := n.DebugNodeWithChrootStd(args...)
	logger.Debugf("json rpm-ostree status:\n%s", stringStatus)
	return stringStatus, err
}

// GetBootedOsTreeDeployment returns the ostree deployment currently booted. In json format
func (n Node) GetBootedOsTreeDeployment(asJSON bool) (string, error) {
	if asJSON {
		stringStatus, err := n.GetRpmOstreeStatus(true)
		if err != nil {
			return "", err
		}

		deployments := JSON(stringStatus).Get("deployments")
		for _, item := range deployments.Items() {
			booted := item.Get("booted").ToBool()
			if booted {
				return item.AsJSONString()
			}
		}
	} else {

		stringStatus, err := n.GetRpmOstreeStatus(false)
		if err != nil {
			return "", err
		}
		deployments := strings.Split(stringStatus, "\n\n")
		for _, deployment := range deployments {
			if strings.Contains(deployment, "*") {
				return deployment, nil
			}
		}
	}

	logger.Infof("WARNING! No booted deployment found in node %s", n.GetName())
	return "", nil

}

// GetCurrentBootOSImage returns the osImage currently used to boot the node
func (n Node) GetCurrentBootOSImage() (string, error) {
	deployment, err := n.GetBootedOsTreeDeployment(true)
	if err != nil {
		return "", fmt.Errorf("Error getting the rpm-ostree status value.\n%s", err)
	}

	containerRef, jerr := JSON(deployment).GetSafe("container-image-reference")
	if jerr != nil {
		return "", fmt.Errorf("We cant get 'container-image-reference' from the deployment status. Wrong rpm-ostree status!.\n%s\n%s", jerr, deployment)
	}

	logger.Infof("Current booted container-image-reference: %s", containerRef)

	imageSplit := strings.Split(containerRef.ToString(), ":")
	lenImageSplit := len(imageSplit)
	if lenImageSplit < 2 {
		return "", fmt.Errorf("Wrong container-image-reference in deployment:\n%s\n%s", err, deployment)
	}

	// remove the "ostree-unverified-registry:" part of the image
	// remove the "containers-storage:" part of the image
	// it can have these modifiers: ostree-unverified-image:containers-storage:quay.io/openshift-.....
	// we need to take into account this kind of images too ->  ostree-unverified-registry:image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/ocb-worker-image@sha256:da29d9033c...
	image := imageSplit[lenImageSplit-2] + ":" + imageSplit[lenImageSplit-1]
	// we need to check if the image includes the port too
	if lenImageSplit > 2 {
		_, err := strconv.Atoi(strings.Split(image, "/")[0])
		// the image url includes the port. It is in the format my.doamin:port/my/path
		if err == nil {
			image = imageSplit[lenImageSplit-3] + ":" + image
		}
	}

	image = strings.TrimSpace(image)
	logger.Infof("Booted image: %s", image)

	return image, nil
}

// Cordon cordons the node by running the "oc adm cordon" command
func (n *Node) Cordon() error {
	return n.oc.Run("adm").Args("cordon", n.GetName()).Execute()
}

// Uncordon uncordons the node by running the "oc adm uncordon" command
func (n *Node) Uncordon() error {
	return n.oc.Run("adm").Args("uncordon", n.GetName()).Execute()
}

// IsCordoned returns true if the node is cordoned
func (n *Node) IsCordoned() (bool, error) {
	key, err := n.Get(`{.spec.taints[?(@.key=="node.kubernetes.io/unschedulable")].key}`)
	if err != nil {
		return false, err
	}

	return key != "", nil
}

// IsCordonedOrFail returns true if the node is cordoned. It fails the test is there is any error
func (n *Node) IsCordonedOrFail() bool {
	isCordoned, err := n.IsCordoned()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the tains in node %s", n.GetName())

	return isCordoned
}

// RestoreDesiredConfig changes the value of the desiredConfig annotation to equal the value of currentConfig. desiredConfig=currentConfig.
func (n *Node) RestoreDesiredConfig() error {
	currentConfig := n.GetCurrentMachineConfig()
	if currentConfig == "" {
		return fmt.Errorf("currentConfig annotation has an empty value in node %s", n.GetName())
	}
	logger.Infof("Node: %s. Restoring desiredConfig value to match currentConfig value: %s", n.GetName(), currentConfig)

	currentImage := n.GetCurrentImage()
	if currentImage == "" {
		return n.PatchDesiredConfig(currentConfig)
	}
	logger.Infof("Node: %s. Restoring desiredImage value to match currentImage value: %s", n.GetName(), currentImage)
	return n.PatchDesiredConfigAndDesiredImage(currentConfig, currentImage)
}

// GetCurrentMachineConfig returns the ID of the current machine config used in the node
func (n Node) GetCurrentMachineConfig() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/currentConfig}`)
}

// GetCurrentImage returns the current image used in this node
func (n Node) GetCurrentImage() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/currentImage}`)
}

// GetDesiredMachineConfig returns the ID of the machine config that we want the node to use
func (n Node) GetDesiredMachineConfig() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/desiredConfig}`)
}

// GetMachineConfigState returns the State of machineconfiguration process
func (n Node) GetMachineConfigState() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/state}`)
}

// GetMachineConfigReason returns the Reason of machineconfiguration on this node
func (n Node) GetMachineConfigReason() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/reason}`)
}

// GetDesiredConfig returns the desired machine config for this node
func (n Node) GetDesiredConfig() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/desiredConfig}`)
}

// PatchDesiredConfig patches the desiredConfig annotation with the provided value
func (n *Node) PatchDesiredConfig(desiredConfig string) error {
	return n.Patch("merge", `{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"`+desiredConfig+`"}}}`)
}

// PatchDesiredConfigAndImage patches the desiredConfig annotation and the desiredImage annotation with the provided values
func (n *Node) PatchDesiredConfigAndDesiredImage(desiredConfig, desiredImage string) error {
	return n.Patch("merge", `{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"`+desiredConfig+`", "machineconfiguration.openshift.io/desiredImage":"`+desiredImage+`"}}}`)
}

// GetDesiredDrain returns the last desired machine config that needed a drain operation in this node
func (n Node) GetDesiredDrain() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/desiredDrain}`)
}

// GetLastAppliedDrain returns the last applied drain in this node
func (n Node) GetLastAppliedDrain() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/lastAppliedDrain}`)
}

// HasBeenDrained returns a true if the desired and the last applied drain annotations have the same value
func (n Node) HasBeenDrained() bool {
	return n.GetLastAppliedDrain() == n.GetDesiredDrain()
}

// IsUpdated returns if the node is pending for machineconfig configuration or it is up to date
func (n *Node) IsUpdated() bool {
	return (n.GetCurrentMachineConfig() == n.GetDesiredMachineConfig()) && (n.GetMachineConfigState() == "Done")
}

// IsTainted returns if the node hast taints or not
func (n *Node) IsTainted() bool {
	taint, err := n.Get("{.spec.taints}")
	return err == nil && taint != ""
}

// Returns true if the node is schedulable
func (n *Node) IsSchedulable() (bool, error) {
	unschedulable, err := n.Get(`{.spec.unschedulable}`)
	if err != nil {
		return false, err
	}

	return !IsTrue(unschedulable), nil
}

// Returns true if the node is schedulable and fails the test if there is an error
func (n *Node) IsSchedulableOrFail() bool {
	schedulable, err := n.IsSchedulable()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error while getting the taints in node %s", n.GetName())

	return schedulable
}

// HasTaintEffect Returns true if the node has any taint with the given effect
func (n *Node) HasTaintEffect(taintEffect string) (bool, error) {
	taint, err := n.Get(`{.spec.taints[?(@.effect=="` + taintEffect + `")]}`)
	if err != nil {
		return false, err
	}

	return taint != "", nil
}

// HasTaintEffectOrFail Returns true if the node has any taint with the given effect and fails the test if any error happened
func (n *Node) HasTaintEffectOrFail(taintEffect string) bool {
	hasTaintEffect, err := n.HasTaintEffect(taintEffect)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error while getting the taints effects in node %s", n.GetName())

	return hasTaintEffect
}

// IsEdge Returns true if th node is an edge node
func (n *Node) IsEdge() (bool, error) {
	_, err := n.GetLabel(`node-role.kubernetes.io/edge`)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// IsEdgeOrFail Returns true if th node is an edge node and fails the test if any error happens
func (n *Node) IsEdgeOrFail() bool {
	isEdge, err := n.IsEdge()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error finding out if node %s is an edge node", n)
	return isEdge
}

// IsUpdating returns if the node is currently updating the machine configuration
func (n *Node) IsUpdating() bool {
	return n.GetMachineConfigState() == "Working"
}

// IsReady returns boolean 'true' if the node is ready. Else it retruns 'false'.
func (n Node) IsReady() bool {
	return n.IsConditionStatusTrue("Ready")
}

// GetMCDaemonLogs returns the logs of the MachineConfig daemonset pod for this node. The logs will be grepped using the 'filter' parameter
func (n Node) GetMCDaemonLogs(filter string) (string, error) {
	var (
		mcdLogs = ""
		err     error
	)
	err = Retry(5, 5*time.Second, func() error {
		mcdLogs, err = exutil.GetSpecificPodLogs(n.oc, MachineConfigNamespace, "machine-config-daemon", n.GetMachineConfigDaemon(), filter)
		return err
	})

	return mcdLogs, err
}

// PollMCDaemonLogs returns a function that can be used by gomega Eventually/Consistently functions to poll logs results
// If there is an error, it will return empty string, new need to take that into account building our Eventually/Consistently statement
func (n Node) PollMCDaemonLogs(filter string) func() string {
	return func() string {
		logs, err := n.GetMCDaemonLogs(filter)
		if err != nil {
			return ""
		}
		return logs
	}
}

// CaptureMCDaemonLogsUntilRestartWithTimeout captures all the logs in the MachineConfig daemon pod for this node until the daemon pod is restarted
func (n *Node) CaptureMCDaemonLogsUntilRestartWithTimeout(timeout string) (string, error) {
	var (
		logs                = ""
		err                 error
		machineConfigDaemon = n.GetMachineConfigDaemon()
	)

	duration, err := time.ParseDuration(timeout)
	if err != nil {
		return "", err
	}

	c := make(chan string, 1)

	go func() {
		err = Retry(5, 5*time.Second, func() error {
			var err error
			logs, err = n.oc.WithoutNamespace().Run("logs").Args("-n", MachineConfigNamespace, machineConfigDaemon, "-c", "machine-config-daemon", "-f").Output()
			if err != nil {
				logger.Errorf("Retrying because: Error getting %s logs. Error: %s\nOutput: %s", machineConfigDaemon, err, logs)
			}
			return err
		})

		if err != nil {
			logger.Errorf("Error getting %s logs. Error: %s", machineConfigDaemon, err)
		}
		c <- logs
	}()

	select {
	case logs := <-c:
		return logs, nil
	case <-time.After(duration):
		errMsg := fmt.Sprintf(`Node "%s". Timeout while waiting for the daemon pod "%s" -n  "%s" to be restarted`,
			n.GetName(), machineConfigDaemon, MachineConfigNamespace)
		logger.Info(errMsg)
		return "", errors.New(errMsg)
	}

}

// GetDateOrFail executes `date`command and returns the current time in the node and fails the test case if there is any error
func (n Node) GetDateOrFail() time.Time {
	date, err := n.GetDate()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Could not get the current date in %s", n)

	return date
}

// GetDate executes `date`command and returns the current time in the node
func (n Node) GetDate() (time.Time, error) {

	date, _, err := n.DebugNodeWithChrootStd(`date`, `+%Y-%m-%dT%H:%M:%SZ`)

	logger.Infof("node %s. DATE: %s", n.GetName(), date)
	if err != nil {
		logger.Errorf("Error trying to get date in node %s: %s", n.GetName(), err)
		return time.Time{}, err
	}
	layout := "2006-01-02T15:04:05Z"
	returnTime, perr := time.Parse(layout, date)
	if perr != nil {
		logger.Errorf("Error trying to parsing date %s in node %s: %s", date, n.GetName(), perr)
		return time.Time{}, perr
	}

	return returnTime, nil
}

// GetUptime executes `uptime -s` command and returns the time when the node was booted
func (n Node) GetUptime() (time.Time, error) {

	uptime, _, err := n.DebugNodeWithChrootStd(`uptime`, `-s`)

	logger.Infof("node %s. UPTIME: %s", n.GetName(), uptime)
	if err != nil {
		logger.Errorf("Error trying to get uptime in node %s: %s", n.GetName(), err)
		return time.Time{}, err
	}
	layout := "2006-01-02 15:04:05"
	returnTime, perr := time.Parse(layout, uptime)
	if perr != nil {
		logger.Errorf("Error trying to parsing uptime %s in node %s: %s", uptime, n.GetName(), perr)
		return time.Time{}, perr
	}

	return returnTime, nil
}

// GetEventsByReasonSince returns a list of all the events with the given reason that are related to this node since the provided date
func (n Node) GetEventsByReasonSince(since time.Time, reason string) ([]Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`reason=` + reason + `,involvedObject.name=` + n.GetName())

	return eventList.GetAllSince(since)
}

// GetAllEventsSince returns a list of all the events related to this node since the provided date
func (n Node) GetAllEventsSince(since time.Time) ([]Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`involvedObject.name=` + n.GetName())

	return eventList.GetAllSince(since)
}

// GetAllEventsSinceEvent returns a list of all the events related to this node that occurred after the provided event
func (n Node) GetAllEventsSinceEvent(since *Event) ([]Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`involvedObject.name=` + n.GetName())

	return eventList.GetAllEventsSinceEvent(since)
}

// GetLatestEvent returns the latest event occurred in the node
func (n Node) GetLatestEvent() (*Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`involvedObject.name=` + n.GetName())

	return eventList.GetLatest()
}

// GetEvents retunrs all the events that happened in this node since IgnoreEventsBeforeNow()() method was called.
//
//	If IgnoreEventsBeforeNow() is not called, it returns all existing events for this node.
func (n *Node) GetEvents() ([]Event, error) {
	return n.GetAllEventsSince(n.eventCheckpoint)
}

func (n *Node) IgnoreEventsBeforeNow() error {
	var err error
	latestEvent, lerr := n.GetLatestEvent()
	if lerr != nil {
		return lerr
	}

	logger.Infof("Latest event in node %s was: %s", n.GetName(), latestEvent)

	if latestEvent == nil {
		logger.Infof("Since no event was found for node %s, we will not ignore any event", n.GetName())
		n.eventCheckpoint = time.Time{}
		return nil
	}

	logger.Infof("Ignoring all previous events!")
	n.eventCheckpoint, err = latestEvent.GetLastTimestamp()
	return err
}

// GetDateWithDelta returns the date in the node +delta
func (n Node) GetDateWithDelta(delta string) (time.Time, error) {
	date, err := n.GetDate()
	if err != nil {
		return time.Time{}, err
	}

	timeDuration, terr := time.ParseDuration(delta)
	if terr != nil {
		logger.Errorf("Error getting delta time %s", terr)
		return time.Time{}, terr
	}

	return date.Add(timeDuration), nil
}

// IsFIPSEnabled check whether fips is enabled on node
func (n *Node) IsFIPSEnabled() (bool, error) {
	output, err := n.DebugNodeWithChroot("fips-mode-setup", "--check")
	if err != nil {
		logger.Errorf("Error checking fips mode %s", err)
	}

	return strings.Contains(output, "FIPS mode is enabled"), err
}

// IsKernelArgEnabled check whether kernel arg is enabled on node
func (n *Node) IsKernelArgEnabled(karg string) (bool, error) {
	unameOut, unameErr := n.DebugNodeWithChroot("bash", "-c", "uname -a")
	if unameErr != nil {
		logger.Errorf("Error checking kernel arg via uname -a: %v", unameErr)
		return false, unameErr
	}

	cliOut, cliErr := n.DebugNodeWithChroot("cat", "/proc/cmdline")
	if cliErr != nil {
		logger.Errorf("Err checking kernel arg via /proc/cmdline: %v", cliErr)
		return false, cliErr
	}

	return (strings.Contains(unameOut, karg) || strings.Contains(cliOut, karg)), nil
}

// IsRealTimeKernel returns true if the node is using a realtime kernel
func (n *Node) IsRealTimeKernel() (bool, error) {
	// we can use the IsKernelArgEnabled to check the realtime kernel
	return n.IsKernelArgEnabled("PREEMPT_RT")
}

// Is64kPagesKernel returns true if the node is using a 64k-pages kernel
func (n *Node) Is64kPagesKernel() (bool, error) {
	// we can use the IsKernelArgEnabled to check the 64k-pages kernel
	return n.IsKernelArgEnabled("+64k")
}

// InstallRpm installs the rpm in the node using rpm-ostree command
func (n *Node) InstallRpm(rpmName string) (string, error) {
	logger.Infof("Installing rpm '%s' in node  %s", rpmName, n.GetName())
	out, err := n.DebugNodeWithChroot("rpm-ostree", "install", rpmName)

	return out, err
}

// UninstallRpm installs the rpm in the node using rpm-ostree command
func (n *Node) UninstallRpm(rpmName string) (string, error) {
	logger.Infof("Uninstalling rpm '%s' in node  %s", rpmName, n.GetName())
	out, err := n.DebugNodeWithChroot("rpm-ostree", "uninstall", rpmName)

	return out, err
}

// Reboot reboots the node after waiting 10 seconds. To know why look https://issues.redhat.com/browse/OCPBUGS-1306
func (n *Node) Reboot() error {
	afterSeconds := 1
	logger.Infof("REBOOTING NODE %s  after %d seconds!!", n.GetName(), afterSeconds)

	// In SNO we cannot trigger the reboot directly using a debug command (like: "sleep 10 && reboot"), because the debug pod will return a failure
	// because we lose connectivity with the cluster when we reboot the only node in the cluster
	// The solution is to schedule a reboot 1 second after the Reboot method is called, and wait 5 seconds to make sure that the reboot happened
	out, err := n.DebugNodeWithChroot("sh", "-c", fmt.Sprintf("systemd-run --on-active=%d --timer-property=AccuracySec=10ms reboot", afterSeconds))
	if err != nil {
		logger.Errorf("Error rebooting node %s:\n%s", n, out)
	}

	time.Sleep(time.Duration(afterSeconds) * time.Second) // we don't return the control of the program until we make sure that the timer for the reboot has expired
	return err
}

// IsRpmOsTreeIdle returns true if `rpm-ostree status` reports iddle state
func (n *Node) IsRpmOsTreeIdle() (bool, error) {
	status, err := n.GetRpmOstreeStatus(false)

	if strings.Contains(status, "State: idle") {
		return true, err
	}

	return false, err
}

// WaitUntilRpmOsTreeIsIdle waits until rpm-ostree reports an idle state. Returns an error if times out
func (n *Node) WaitUntilRpmOsTreeIsIdle() error {
	logger.Infof("Waiting for rpm-ostree state to be idle in node %s", n.GetName())

	immediate := false
	waitErr := wait.PollUntilContextTimeout(context.TODO(), 10*time.Second, 10*time.Minute, immediate, func(_ context.Context) (bool, error) {
		isIddle, err := n.IsRpmOsTreeIdle()
		if err == nil {
			if isIddle {
				return true, nil
			}
			return false, nil
		}

		logger.Infof("Error waiting for rpm-ostree status to report idle state: %s.\nTry next round", err)
		return false, nil
	})

	if waitErr != nil {
		logger.Errorf("Timeout while waiting for rpm-ostree status to report idle state in node %s. Error: %s",
			n.GetName(),
			waitErr)
	}

	return waitErr

}

// CancelRpmOsTreeTransactions cancels rpm-ostree transactions
func (n *Node) CancelRpmOsTreeTransactions() (string, error) {
	return n.DebugNodeWithChroot("rpm-ostree", "cancel")
}

// CopyFromLocal Copy a local file or directory to the node
func (n *Node) CopyFromLocal(from, to string) error {
	immediate := true
	waitErr := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, 5*time.Minute, immediate, func(_ context.Context) (bool, error) {
		kubeletReady := n.IsReady()
		if kubeletReady {
			return true, nil
		}
		logger.Warnf("Kubelet is not ready in %s. To copy the file to the node we need to wait for kubelet to be ready. Waiting...", n)
		return false, nil
	})

	if waitErr != nil {
		logger.Errorf("Cannot copy file %s to %s in node %s because Kubelet is not ready in this node", from, to, n)
		return waitErr
	}

	return n.oc.Run("adm").Args("copy-to-node", "node/"+n.GetName(), fmt.Sprintf("--copy=%s=%s", from, to)).Execute()
}

// CopyToLocal Copy a file or directory in the node to a local path
func (n *Node) CopyToLocal(from, to string) error {
	logger.Infof("Node: %s. Copying file %s to local path %s",
		n.GetName(), from, to)
	mcDaemonName := n.GetMachineConfigDaemon()
	fromDaemon := filepath.Join("/rootfs", from)

	return n.oc.Run("cp").Args("-n", MachineConfigNamespace, mcDaemonName+":"+fromDaemon, to, "-c", MachineConfigDaemon).Execute()
}

// RemoveFile removes a file from the node
func (n *Node) RemoveFile(filePathToRemove string) error {
	logger.Infof("Removing file %s from node %s", filePathToRemove, n.GetName())
	output, err := n.DebugNodeWithChroot("rm", "-f", filePathToRemove)
	logger.Info(output)
	return err
}

// RpmIsInstalled returns true if the package is installed
func (n *Node) RpmIsInstalled(rpmNames ...string) bool {
	rpmOutput, err := n.DebugNodeWithChroot(append([]string{"rpm", "-q"}, rpmNames...)...)
	logger.Info(rpmOutput)
	return err == nil
}

// ExecuteExpectBatch run a command and executes an interactive batch sequence using expect
func (n *Node) ExecuteDebugExpectBatch(timeout time.Duration, batch []expect.Batcher) ([]expect.BatchRes, error) {

	setErr := quietSetNamespacePrivileged(n.oc, n.oc.Namespace())
	if setErr != nil {
		return nil, setErr
	}

	debugCommand := fmt.Sprintf("oc --kubeconfig=%s -n %s debug node/%s",
		exutil.KubeConfigPath(), n.oc.Namespace(), n.GetName())

	logger.Infof("Expect spawning command: %s", debugCommand)
	e, _, err := expect.Spawn(debugCommand, -1, expect.Verbose(true))
	defer func() { _ = e.Close() }()
	if err != nil {
		logger.Errorf("Error spawning process %s. Error: %s", debugCommand, err)
		return nil, err
	}

	bresps, err := e.ExpectBatch(batch, timeout)
	if err != nil {
		logger.Errorf("Error executing batch: %s", err)
	}

	recErr := quietRecoverNamespaceRestricted(n.oc, n.oc.Namespace())
	if recErr != nil {
		return nil, err
	}

	return bresps, err
}

// UserAdd creates a user in the node
func (n *Node) UserAdd(userName string) error {
	logger.Infof("Create user %s in node %s", userName, n.GetName())
	_, err := n.DebugNodeWithChroot("useradd", userName)
	return err
}

// UserDel deletes a user in the node
func (n *Node) UserDel(userName string) error {
	logger.Infof("Delete user %s in node %s", userName, n.GetName())
	_, err := n.DebugNodeWithChroot("userdel", "-f", userName)
	return err
}

// UserExists returns true if the user exists in the node
func (n *Node) UserExists(userName string) bool {
	_, err := n.DebugNodeWithChroot("grep", "-E", fmt.Sprintf("^%s:", userName), "/etc/shadow")

	return err == nil
}

// GetRHELVersion returns the RHEL version of the  node
func (n *Node) GetRHELVersion() (string, error) {
	vContent, err := n.DebugNodeWithChroot("cat", "/etc/os-release")
	if err != nil {
		return "", err
	}

	r := regexp.MustCompile(`VERSION_ID="?(?P<rhel_version>.*)"?`)
	match := r.FindStringSubmatch(vContent)
	if len(match) == 0 {
		msg := fmt.Sprintf("No RHEL_VERSION available in /etc/os-release file: %s", vContent)
		logger.Info(msg)
		return "", errors.New(msg)
	}

	rhelvIndex := r.SubexpIndex("rhel_version")
	rhelVersion := match[rhelvIndex]

	logger.Infof("Node %s RHEL_VERSION %s", n.GetName(), rhelVersion)
	return rhelVersion, nil
}

// GetPool returns the only pool owning this node
func (n *Node) GetPrimaryPool() (*MachineConfigPool, error) {
	allMCPs, err := NewMachineConfigPoolList(n.oc).GetAll()
	if err != nil {
		return nil, err
	}

	var primaryPool *MachineConfigPool
	for _, item := range allMCPs {
		pool := item
		allNodes, err := pool.getSelectedNodes("")
		if err != nil {
			return nil, err
		}

		for _, node := range allNodes {
			if node.GetName() != n.GetName() {
				continue
			}

			// We use short circuit evaluation to set the primary pool:
			// - If the pool is master, it will be the primary pool;
			// - If the primary pool is nil (not set yet), we set the primary pool (either worker or custom);
			// - If the primary pool is not nil, we overwrite it only if the primary pool is a worker.
			if pool.IsMaster() || primaryPool == nil || primaryPool.IsWorker() {
				primaryPool = &pool
			} else if pool.IsCustom() && primaryPool != nil && primaryPool.IsCustom() {
				// Error condition: the node belongs to 2 custom pools
				return nil, fmt.Errorf("Forbidden configuration. The node %s belongs to 2 custom pools: %s and %s",
					node.GetName(), primaryPool.GetName(), pool.GetName())
			}
		}
	}

	return primaryPool, nil
}

// GetPool returns the only pool owning this node and fails the test if any error happened
func (n *Node) GetPrimaryPoolOrFail() *MachineConfigPool {
	pool, err := n.GetPrimaryPool()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(),
		"Error getting the pool that owns node %", n.GetName())
	return pool
}

// GetPools returns a list with all the MCPs matching this node's labels. An node can be listed by n more than one pool.
func (n *Node) GetPools() ([]MachineConfigPool, error) {
	allPools, err := NewMachineConfigPoolList(n.oc).GetAll()
	if err != nil {
		return nil, err
	}

	nodePools := []MachineConfigPool{}
	for _, mcp := range allPools {
		// Get all nodes labeled for this pool
		allNodes, err := mcp.getSelectedNodes("")
		if err != nil {
			return nil, err
		}
		for _, node := range allNodes {
			if n.GetName() == node.GetName() {
				nodePools = append(nodePools, mcp)
			}
		}
	}

	return nodePools, nil
}

// IsListedByPoolOrFail returns true if this node is listed by the MPC configured labels. If an error happens it fails the test.
func (n *Node) IsListedByPoolOrFail(mcp *MachineConfigPool) bool {
	isInPool, err := n.IsListedByPool(mcp)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(),
		"Cannot get the list of pools for node %s", n.GetName())
	return isInPool
}

// IsListedByPool returns true if this node is listed by the MCP configured labels.
func (n *Node) IsListedByPool(mcp *MachineConfigPool) (bool, error) {
	pools, err := n.GetPools()
	if err != nil {
		return false, err
	}

	for _, pool := range pools {
		if pool.GetName() == mcp.GetName() {
			return true, nil
		}
	}
	return false, nil
}

// RemoveIPTablesRulesByRegexp removes all the iptables rules printed by  `iptables -S`  that match the given regexp
func (n *Node) RemoveIPTablesRulesByRegexp(regx string) ([]string, error) {
	return n.removeIPTablesRulesByRegexp(false, regx)
}

// RemoveIP6TablesRulesByRegexp removes all the iptables rules printed by  `ip6tables -S`  that match the given regexp
func (n *Node) RemoveIP6TablesRulesByRegexp(regx string) ([]string, error) {
	return n.removeIPTablesRulesByRegexp(true, regx)
}

// removeIPTablesRulesByRegexp removes all the iptables rules printed by `iptables -S` or `ip6tables -S` that match the given regexp
func (n *Node) removeIPTablesRulesByRegexp(ipv6 bool, regx string) ([]string, error) {
	removedRules := []string{}
	command := "iptables"
	if ipv6 == true {
		command = "ip6tables"
	}

	allRulesString, stderr, err := n.DebugNodeWithChrootStd(command, "-S")
	if err != nil {
		logger.Errorf("Error running `%s -S`. Stderr: %s", command, stderr)
		return nil, err
	}

	allRules := strings.Split(allRulesString, "\n")
	for _, rule := range allRules {
		if !regexp.MustCompile(regx).MatchString(rule) {
			continue
		}
		logger.Infof("%s. Removing %s rule: %s", n.GetName(), command, rule)
		removeCommand := strings.Replace(rule, "-A", "-D", 1)
		output, err := n.DebugNodeWithChroot(append([]string{command}, splitCommandString(removeCommand)...)...)
		// OCPQE-20258 if the rule is removed already, retry will be failed as well. add this logic to catach this error
		// if the error message indicates the rule does not exist, i.e. it's already removed, we consider the retry is succeed
		alreadyRemoved := strings.Contains(output, "does a matching rule exist in that chain")
		if alreadyRemoved {
			logger.Warnf("iptable rule %s is already removed", rule)
		}

		if err != nil && !alreadyRemoved {
			logger.Errorf("Output: %s", output)
			return removedRules, err
		}
		removedRules = append(removedRules, rule)
	}
	return removedRules, err
}

// ExecIPTables executes the iptables commands in the node. The "rules" param is a list of iptables commands to be executed. Each string is a full command.
//
//	  for example:
//		[ "-A OPENSHIFT-BLOCK-OUTPUT -p tcp -m tcp --dport 22623 --tcp-flags FIN,SYN,RST,ACK SYN -j REJECT --reject-with icmp-port-unreachable",
//	       "-A OPENSHIFT-BLOCK-OUTPUT -p tcp -m tcp --dport 22624 --tcp-flags FIN,SYN,RST,ACK SYN -j REJECT --reject-with icmp-port-unreachable" ]
//
// This function can be used to restore the rules removed by "RemoveIPTablesRulesByRegexp"
func (n *Node) execIPTables(ipv6 bool, rules []string) error {
	command := "iptables"
	if ipv6 == true {
		command = "ip6tables"
	}

	for _, rule := range rules {
		logger.Infof("%s. Adding %s rule: %s", n.GetName(), command, rule)
		output, err := n.DebugNodeWithChroot(append([]string{command}, splitCommandString(rule)...)...)
		if err != nil {
			logger.Errorf("Output: %s", output)
			return err
		}

	}
	return nil
}

// ExecIPTables execute the iptables command in the node
func (n *Node) ExecIPTables(rules []string) error {
	return n.execIPTables(false, rules)
}

// ExecIPTables execute the ip6tables command in the node
func (n *Node) ExecIP6Tables(rules []string) error {
	return n.execIPTables(true, rules)
}

// GetArchitecture get the architecture used in the node
func (n Node) GetArchitecture() (architecture.Architecture, error) {
	arch, err := n.Get(`{.status.nodeInfo.architecture}`)
	if err != nil {
		return architecture.UNKNOWN, err
	}
	return architecture.FromString(arch), nil
}

// GetArchitectureOrFail get the architecture used in the node and fail the test if any error happens while doing it
func (n Node) GetArchitectureOrFail() architecture.Architecture {
	arch, err := n.GetArchitecture()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the architecture of node %s", n.GetName())

	return arch
}

// GetJournalLogs returns the journal logs
func (n *Node) GetJournalLogs(args ...string) (string, error) {
	cmd := []string{"journalctl", "-o", "with-unit"}
	return n.DebugNodeWithChroot(append(cmd, args...)...)
}

// GetMachineConfigNode returns the MachineConfigNode resource linked to this node
func (n *Node) GetMachineConfigNode() *MachineConfigNode {
	return NewMachineConfigNode(n.oc.AsAdmin(), n.GetName())
}

// GetFileSystemSpaceUsage returns the space usage in the node
// Parse command
// $ df -B1 --output=used,avail /
//
//	Used      Avail
//
// 38409719808 7045369856
func (n *Node) GetFileSystemSpaceUsage(path string) (*SpaceUsage, error) {
	var (
		parserRegex = `(?P<Used>\d+)\D+(?P<Avail>\d+)`
	)
	stdout, stderr, err := n.DebugNodeWithChrootStd("df", "-B1", "--output=used,avail", path)
	if err != nil {
		logger.Errorf("Error getting the disk usage in node %s:\nstdout:%s\nstderr:%s",
			n.GetName(), stdout, stderr)
		return nil, err
	}

	lines := strings.Split(stdout, "\n")
	if len(lines) != 2 {
		return nil, fmt.Errorf("Expected 2 lines, and got:\n%s", stdout)
	}

	logger.Debugf("parsing: %s", lines[1])
	re := regexp.MustCompile(parserRegex)
	match := re.FindStringSubmatch(strings.TrimSpace(lines[1]))
	logger.Infof("matched disk space stat info: %v", match)
	// check whether matched string found
	if len(match) == 0 {
		return nil, fmt.Errorf("no disk space info matched")
	}

	usedIndex := re.SubexpIndex("Used")
	if usedIndex < 0 {
		return nil, fmt.Errorf("Could not parse Used bytes from\n%s", stdout)
	}
	used, err := strconv.ParseInt(match[usedIndex], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("Could convert parsed Used data [%s] into float64 from\n%s", match[usedIndex], stdout)

	}

	availIndex := re.SubexpIndex("Avail")
	if usedIndex < 0 {
		return nil, fmt.Errorf("Could not parse Avail bytes from\n%s", stdout)
	}
	avail, err := strconv.ParseInt(match[availIndex], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("Could convert parsed Avail data [%s] into float64 from\n%s", match[availIndex], stdout)
	}

	return &SpaceUsage{Used: used, Avail: avail}, nil
}

// GetMachine returns the machine used to create this node
func (n Node) GetMachine() (*Machine, error) {
	machineLabel, err := n.GetAnnotation("machine.openshift.io/machine")
	if err != nil {
		return nil, err
	}
	machineLabelSplit := strings.Split(machineLabel, "/")

	if len(machineLabelSplit) != 2 {
		return nil, fmt.Errorf("Malformed machine label %s in node %s", machineLabel, n.GetName())
	}
	machineName := machineLabelSplit[1]
	return NewMachine(n.GetOC(), "openshift-machine-api", machineName), nil
}

// CanUseDnfDownload returns true if dnf can use the download subcommand
func (n Node) CanUseDnfDownload() (bool, error) {
	out, err := n.DebugNodeWithChroot("dnf", "download", "--help")
	if err != nil {
		if strings.Contains(out, "No such command:") || strings.Contains(out, "No such file or directory") {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// DnfDownload uses the "dnf download" command to download a rpm package. Returns the full name of the downloaded package
func (n Node) DnfDownload(pkg, dir string) (string, error) {
	out, err := n.DebugNodeWithChroot("dnf", "download", pkg, "--destdir", dir)
	logger.Infof("Download output: %s", out)
	if err != nil {
		return "", err
	}

	expr := `(?P<package>(?m)^` + pkg + `.*rpm)`
	r := regexp.MustCompile(expr)
	match := r.FindStringSubmatch(out)
	if len(match) == 0 {
		msg := fmt.Sprintf("Wrong download output. Cannot extract the name of the downloaded package using expresion %s:\n%s", expr, out)
		logger.Error(msg)
		return "", errors.New(msg)
	}
	pkgNameIndex := r.SubexpIndex("package")
	pkgName := match[pkgNameIndex]

	fullPkgName := path.Join(dir, pkgName)
	logger.Infof("Downloaded package: %s", fullPkgName)
	return fullPkgName, nil
}

// Reset returns the node's OS to its original state, removing any modification applied to it
func (n Node) OSReset() error {
	logger.Infof("Resetting the OS in node %s", n)
	_, err := n.DebugNodeWithChroot("rpm-ostree", "reset")
	return err
}

// GetAll returns a []Node list with all existing nodes
func (nl *NodeList) GetAll() ([]Node, error) {
	allNodeResources, err := nl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allNodes := make([]Node, 0, len(allNodeResources))

	for _, nodeRes := range allNodeResources {
		allNodes = append(allNodes, *NewNode(nl.oc, nodeRes.name))
	}

	return allNodes, nil
}

// GetAllLinux resturns a list with all linux nodes in the cluster
func (nl NodeList) GetAllLinux() ([]Node, error) {
	nl.ByLabel("kubernetes.io/os=linux")

	return nl.GetAll()
}

// GetAllMasterNodes returns a list of master Nodes
func (nl NodeList) GetAllMasterNodes() ([]Node, error) {
	nl.ByLabel("node-role.kubernetes.io/master=")

	return nl.GetAll()
}

// GetAllWorkerNodes returns a list of worker Nodes
func (nl NodeList) GetAllWorkerNodes() ([]Node, error) {
	nl.ByLabel("node-role.kubernetes.io/worker=")

	return nl.GetAll()
}

// GetAllMasterNodesOrFail returns a list of master Nodes
func (nl NodeList) GetAllMasterNodesOrFail() []Node {
	masters, err := nl.GetAllMasterNodes()
	o.Expect(err).NotTo(o.HaveOccurred())
	return masters
}

// GetAllWorkerNodesOrFail returns a list of worker Nodes. Fail the test case if an error happens.
func (nl NodeList) GetAllWorkerNodesOrFail() []Node {
	workers, err := nl.GetAllWorkerNodes()
	o.Expect(err).NotTo(o.HaveOccurred())
	return workers
}

// GetAllLinuxWorkerNodes returns a list of linux worker Nodes
func (nl NodeList) GetAllLinuxWorkerNodes() ([]Node, error) {
	nl.ByLabel("node-role.kubernetes.io/worker=,kubernetes.io/os=linux")

	return nl.GetAll()
}

// GetAllLinuxWorkerNodesOrFail returns a list of linux worker Nodes. Fail the test case if an error happens.
func (nl NodeList) GetAllLinuxWorkerNodesOrFail() []Node {
	workers, err := nl.GetAllLinuxWorkerNodes()
	o.Expect(err).NotTo(o.HaveOccurred())
	return workers
}

// GetAllCoreOsWokerNodesOrFail returns a list with all CoreOs nodes in the cluster. Fail the test case if an error happens.
func (nl NodeList) GetAllCoreOsWokerNodesOrFail() []Node {
	nl.ByLabel("node-role.kubernetes.io/worker=,node.openshift.io/os_id=rhel")

	workers, err := nl.GetAll()
	o.Expect(err).NotTo(o.HaveOccurred())
	return workers
}

// GetAllCoreOsNodesOrFail returns a list with all CoreOs nodes including master and workers. Fail the test case if an error happens
func (nl NodeList) GetAllCoreOsNodesOrFail() []Node {
	nl.ByLabel("node.openshift.io/os_id=rhel")

	allRhcos, err := nl.GetAll()
	o.Expect(err).NotTo(o.HaveOccurred())
	return allRhcos
}

// GetTaintedNodes returns a list with all tainted nodes in the cluster. Fail the test if an error happens.
func (nl *NodeList) GetTaintedNodes() []Node {
	allNodes, err := nl.GetAll()
	o.Expect(err).NotTo(o.HaveOccurred())

	taintedNodes := []Node{}
	for _, node := range allNodes {
		if node.IsTainted() {
			taintedNodes = append(taintedNodes, node)
		}
	}

	return taintedNodes
}

// GetAllDegraded returns a list will all the nodes reporting macineconfig degraded state
// metadata.annotations.machineconfiguration\.openshift\.io/state=="Degraded
func (nl NodeList) GetAllDegraded() ([]Node, error) {
	filter := `?(@.metadata.annotations.machineconfiguration\.openshift\.io/state=="Degraded")`
	nl.SetItemsFilter(filter)
	return nl.GetAll()
}

// McStateSnapshot get snapshot of machine config state for the nodes in this list
// the output is like `Working Done Done`
func (nl NodeList) McStateSnapshot() string {
	return nl.GetOrFail(`{.items[*].metadata.annotations.machineconfiguration\.openshift\.io/state}`)
}

// quietSetNamespacePrivileged invokes exutil.SetNamespacePrivileged but disable the logs output to avoid noise in the logs
func quietSetNamespacePrivileged(oc *exutil.CLI, namespace string) error {
	oc.NotShowInfo()
	defer oc.SetShowInfo()

	logger.Debugf("Setting namespace %s as privileged", namespace)
	return exutil.SetNamespacePrivileged(oc, namespace)
}

// quietRecoverNamespaceRestricted invokes exutil.RecoverNamespaceRestricted but disable the logs output to avoid noise in the logs
func quietRecoverNamespaceRestricted(oc *exutil.CLI, namespace string) error {
	oc.NotShowInfo()
	defer oc.SetShowInfo()

	logger.Debugf("Recovering namespace %s from privileged", namespace)
	return exutil.RecoverNamespaceRestricted(oc, namespace)
}

// GetOperatorNode returns the node running the MCO operator pod
func GetOperatorNode(oc *exutil.CLI) (*Node, error) {
	podsList := NewNamespacedResourceList(oc.AsAdmin(), "pods", MachineConfigNamespace)
	podsList.ByLabel("k8s-app=machine-config-operator")

	mcoPods, err := podsList.GetAll()
	if err != nil {
		return nil, err
	}

	if len(mcoPods) != 1 {
		return nil, fmt.Errorf("There should be 1 and only 1 MCO operator pod. Found operator pods: %s", mcoPods)
	}

	nodeName, err := mcoPods[0].Get(`{.spec.nodeName}`)
	if err != nil {
		return nil, err
	}

	return NewNode(oc, nodeName), nil
}
