package extended

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	expect "github.com/google/goexpect"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"k8s.io/apimachinery/pkg/util/sets"
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

// GetUnitProperties executes `systemctl show $unitname`, can be used to checkout service dependency
func (n *Node) GetUnitProperties(unitName string, args ...string) (string, error) {
	cmd := append([]string{"systemctl", "show", unitName}, args...)
	stdout, _, err := n.DebugNodeWithChrootStd(cmd...)
	return stdout, err
}

// GetRpmOstreeStatus returns the rpm-ostree status in json format
func (n *Node) GetRpmOstreeStatus(asJSON bool) (string, error) {
	args := []string{"rpm-ostree", "status"}
	if asJSON {
		args = append(args, "--json")
	}
	stringStatus, _, err := n.DebugNodeWithChrootStd(args...)
	logger.Debugf("json rpm-ostree status:\n%s", stringStatus)
	return stringStatus, err
}

// GetBootedOsTreeDeployment returns the ostree deployment currently booted. In json format
func (n *Node) GetBootedOsTreeDeployment(asJSON bool) (string, error) {
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
func (n *Node) GetCurrentBootOSImage() (string, error) {
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

// RestoreDesiredConfig changes the value of the desiredConfig annotation to equal the value of currentConfig. desiredConfig=currentConfig.
func (n *Node) RestoreDesiredConfig() error {
	currentConfig, err := n.GetCurrentMachineConfig()
	if err != nil {
		return err
	}
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
func (n *Node) GetCurrentMachineConfig() (string, error) {
	return n.Get(`{.metadata.annotations.machineconfiguration\.openshift\.io/currentConfig}`)
}

// GetCurrentImage returns the current image used in this node
func (n *Node) GetCurrentImage() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/currentImage}`)
}

// GetDesiredMachineConfig returns the ID of the machine config that we want the node to use
func (n *Node) GetDesiredMachineConfig() (string, error) {
	return n.Get(`{.metadata.annotations.machineconfiguration\.openshift\.io/desiredConfig}`)
}

// GetMachineConfigState returns the State of machineconfiguration process
func (n *Node) GetMachineConfigState() (string, error) {
	return n.Get(`{.metadata.annotations.machineconfiguration\.openshift\.io/state}`)
}

// GetMachineConfigReason returns the Reason of machineconfiguration on this node
func (n *Node) GetMachineConfigReason() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/reason}`)
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
func (n *Node) GetDesiredDrain() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/desiredDrain}`)
}

// GetLastAppliedDrain returns the last applied drain in this node
func (n *Node) GetLastAppliedDrain() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/lastAppliedDrain}`)
}

// HasBeenDrained returns a true if the desired and the last applied drain annotations have the same value
func (n *Node) HasBeenDrained() bool {
	return n.GetLastAppliedDrain() == n.GetDesiredDrain()
}

// IsUpdated returns if the node is pending for machineconfig configuration or it is up to date
func (n *Node) IsUpdated() (bool, error) {
	currentConfig, err := n.GetCurrentMachineConfig()
	if err != nil {
		return false, err
	}

	desiredConfig, err := n.GetDesiredMachineConfig()
	if err != nil {
		return false, err
	}

	state, err := n.GetMachineConfigState()
	if err != nil {
		return false, err
	}

	mcdLogs, err := n.GetMCDaemonLogs("")
	if err != nil {
		return false, err
	}

	// Workaroud. After setting the status to Done, MCO actually executes more actions.
	// To make sure that no more actions will be executed in the node, we need to wait for this string in the logs
	driftMonitorStarted := regexp.MustCompile(`(?i)config.*drift.*monitor.*started`).MatchString(mcdLogs)

	return (currentConfig == desiredConfig) && (state == "Done") && (driftMonitorStarted), nil
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

// IsReady returns boolean 'true' if the node is ready. Else it retruns 'false'.
func (n *Node) IsReady() bool {
	return n.IsConditionStatusTrue("Ready")
}

// GetMCDaemonLogs returns the logs of the MachineConfig daemonset pod for this node. The logs will be grepped using the 'filter' parameter
func (n *Node) GetMCDaemonLogs(filter string) (string, error) {
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

// GetDateOrFail executes `date`command and returns the current time in the node and fails the test case if there is any error
func (n *Node) GetDateOrFail() time.Time {
	date, err := n.GetDate()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Could not get the current date in %s", n)

	return date
}

// GetDate executes `date`command and returns the current time in the node
func (n *Node) GetDate() (time.Time, error) {

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
func (n *Node) GetUptime() (time.Time, error) {

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
func (n *Node) GetEventsByReasonSince(since time.Time, reason string) ([]*Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`reason=` + reason + `,involvedObject.name=` + n.GetName())

	return eventList.GetAllSince(since)
}

// GetAllEventsSince returns a list of all the events related to this node since the provided date
func (n *Node) GetAllEventsSince(since time.Time) ([]*Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`involvedObject.name=` + n.GetName())

	return eventList.GetAllSince(since)
}

// GetAllEventsSinceEvent returns a list of all the events related to this node that occurred after the provided event
func (n *Node) GetAllEventsSinceEvent(since *Event) ([]*Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`involvedObject.name=` + n.GetName())

	return eventList.GetAllEventsSinceEvent(since)
}

// GetLatestEvent returns the latest event occurred in the node
func (n *Node) GetLatestEvent() (*Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`involvedObject.name=` + n.GetName())

	return eventList.GetLatest()
}

// GetEvents retunrs all the events that happened in this node since IgnoreEventsBeforeNow()() method was called.
//
//	If IgnoreEventsBeforeNow() is not called, it returns all existing events for this node.
func (n *Node) GetEvents() ([]*Event, error) {
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
	logger.Infof(output)

	return err
}

// ExecuteExpectBatch run a command and executes an interactive batch sequence using expect
func (n *Node) ExecuteDebugExpectBatch(timeout time.Duration, batch []expect.Batcher) ([]expect.BatchRes, error) {
	debug := false
	if _, enabled := os.LookupEnv(logger.EnableDebugLog); enabled {
		debug = true
	}

	setErr := quietSetNamespacePrivileged(n.oc, n.oc.Namespace())
	if setErr != nil {
		return nil, setErr
	}

	debugCommand := fmt.Sprintf("oc --kubeconfig=%s -n %s debug node/%s",
		exutil.KubeConfigPath(), n.oc.Namespace(), n.GetName())

	logger.Infof("Expect spawning command: %s", debugCommand)
	e, _, err := expect.Spawn(debugCommand, -1, expect.Verbose(debug))
	defer func() { _ = e.Close() }()
	if err != nil {
		logger.Errorf("Error spawning process %s. Error: %s", debugCommand, err)
		return nil, err
	}

	br, err := e.ExpectBatch(batch, timeout)
	if err != nil {
		logger.Errorf("Error running expect batch process for node %s: %s. Batch result: %v", n.GetName(), err, br)
	}

	recErr := quietRecoverNamespaceRestricted(n.oc, n.oc.Namespace())
	if recErr != nil {
		return br, recErr
	}

	return br, err
}

// GetRHELVersion returns the RHEL version of the  node
func (n *Node) GetRHELVersion() (string, error) {
	vContent, err := n.DebugNodeWithChroot("cat", "/etc/os-release")
	if err != nil {
		return "", err
	}

	r := regexp.MustCompile(`VERSION_ID="?(?P<rhel_version>[^"]+)"?`)
	match := r.FindStringSubmatch(vContent)
	if len(match) == 0 {
		msg := fmt.Sprintf("No RHEL_VERSION available in /etc/os-release file: %s", vContent)
		logger.Errorf("%s", msg)
		return "", fmt.Errorf("Error: %s", msg)
	}

	rhelvIndex := r.SubexpIndex("rhel_version")
	rhelVersion := match[rhelvIndex]

	logger.Infof("Node %s RHEL_VERSION %s", n.GetName(), rhelVersion)
	return rhelVersion, nil
}

// GetPool returns the only pool owning this node
func (n *Node) GetPrimaryPool() (*MachineConfigPool, error) {
	allMCPs, err := NewMachineConfigPoolList(n.oc.AsAdmin()).GetAll()
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
				primaryPool = pool
			} else if pool.IsCustom() && primaryPool != nil && primaryPool.IsCustom() {
				// Error condition: the node belongs to 2 custom pools
				return nil, fmt.Errorf("Forbidden configuration. The node %s belongs to 2 custom pools: %s and %s",
					node.GetName(), primaryPool.GetName(), pool.GetName())
			}
		}
	}

	if primaryPool == nil {
		return nil, fmt.Errorf("Could not find the primary pool for %s", n)
	}

	return primaryPool, nil
}

// GetPool returns the only pool owning this node and fails the test if any error happened
func (n *Node) GetPrimaryPoolOrFail() *MachineConfigPool {
	pool, err := n.GetPrimaryPool()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the primary pool of node %s", n.GetName())

	return pool
}

// GetArchitecture get the architecture used in the node
func (n *Node) GetArchitecture() (architecture.Architecture, error) {
	arch, err := n.Get(`{.status.nodeInfo.architecture}`)
	if err != nil {
		return architecture.UNKNOWN, err
	}
	return architecture.FromString(arch), nil
}

// GetArchitectureOrFail get the architecture used in the node and fail the test if any error happens while doing it
func (n *Node) GetArchitectureOrFail() architecture.Architecture {
	arch, err := n.GetArchitecture()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the architecture of node %s", n.GetName())

	return arch
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

// GetAll returns a []*Node list with all existing nodes
func (nl *NodeList) GetAll() ([]*Node, error) {
	allNodeResources, err := nl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allNodes := make([]*Node, 0, len(allNodeResources))

	for _, nodeRes := range allNodeResources {
		allNodes = append(allNodes, NewNode(nl.oc, nodeRes.name))
	}

	return allNodes, nil
}

// GetAllLinux resturns a list with all linux nodes in the cluster
func (nl NodeList) GetAllLinux() ([]*Node, error) {
	nl.ByLabel("kubernetes.io/os=linux")

	return nl.GetAll()
}

// GetAllWorkerNodes returns a list of worker Nodes
func (nl NodeList) GetAllWorkerNodes() ([]*Node, error) {
	nl.ByLabel("node-role.kubernetes.io/worker=")

	return nl.GetAll()
}

// GetAllLinuxWorkerNodes returns a list of linux worker Nodes
func (nl NodeList) GetAllLinuxWorkerNodes() ([]*Node, error) {
	nl.ByLabel("node-role.kubernetes.io/worker=,kubernetes.io/os=linux")

	return nl.GetAll()
}

// GetAllLinuxWorkerNodesOrFail returns a list of linux worker Nodes. Fail the test case if an error happens.
func (nl NodeList) GetAllLinuxWorkerNodesOrFail() []*Node {
	workers, err := nl.GetAllLinuxWorkerNodes()
	o.Expect(err).NotTo(o.HaveOccurred())
	return workers
}

// GetAllReady returns all nodes that are in Ready status
func (nl NodeList) GetAllReady() ([]*Node, error) {
	allNodes, err := nl.GetAll()
	if err != nil {
		return nil, err
	}

	readyNodes := []*Node{}
	for _, node := range allNodes {
		if node.IsReady() {
			readyNodes = append(readyNodes, node)
		}
	}
	return readyNodes, nil
}

// quietSetNamespacePrivileged invokes compat_otp.SetNamespacePrivileged but disable the logs output to avoid noise in the logs
func quietSetNamespacePrivileged(oc *exutil.CLI, namespace string) error {
	oc.NotShowInfo()
	defer oc.SetShowInfo()

	logger.Debugf("Setting namespace %s as privileged", namespace)
	return exutil.SetNamespacePrivileged(oc, namespace)
}

// quietRecoverNamespaceRestricted invokes compat_otp.RecoverNamespaceRestricted but disable the logs output to avoid noise in the logs
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

func getReadyNodes(oc *exutil.CLI) (sets.Set[string], error) {
	nodeList := NewResourceList(oc.AsAdmin(), "nodes")
	nodes, err := nodeList.GetAll()
	if err != nil {
		return nil, err
	}

	nodeSet := sets.New[string]()
	for _, node := range nodes {
		node.oc.NotShowInfo()
		isReady, err := node.Get(`{.status.conditions[?(@.type=="Ready")].status}`)
		if err == nil && isReady == TrueString {
			nodeSet.Insert(node.name)
		}
		node.oc.SetShowInfo()
	}
	return nodeSet, nil
}
