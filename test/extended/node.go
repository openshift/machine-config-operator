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
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	o "github.com/onsi/gomega"
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
func (n *Node) GetCurrentMachineConfig() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/currentConfig}`)
}

// GetCurrentImage returns the current image used in this node
func (n *Node) GetCurrentImage() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/currentImage}`)
}

// GetDesiredMachineConfig returns the ID of the machine config that we want the node to use
func (n *Node) GetDesiredMachineConfig() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/desiredConfig}`)
}

// GetMachineConfigState returns the State of machineconfiguration process
func (n *Node) GetMachineConfigState() string {
	return n.GetOrFail(`{.metadata.annotations.machineconfiguration\.openshift\.io/state}`)
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
func (n *Node) IsUpdated() bool {
	return (n.GetCurrentMachineConfig() == n.GetDesiredMachineConfig()) && (n.GetMachineConfigState() == "Done")
}

// IsReady returns if the node is in Ready condition
func (n *Node) IsReady() bool {
	return n.IsConditionStatusTrue("Ready")
}

// IsTainted returns if the node hast taints or not
func (n *Node) IsTainted() bool {
	taint, err := n.Get("{.spec.taints}")
	return err == nil && taint != ""
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

// CopyFromLocal copies a local file to the node
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

// GetMachineConfigNode returns the MachineConfigNode resource linked to this node
func (n *Node) GetMachineConfigNode() *MachineConfigNode {
	return NewMachineConfigNode(n.oc.AsAdmin(), n.GetName())
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

// Returns the set of ready nodes in the cluster
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

// GetDateOrFail executes GetDate and fails the test if there is an error
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
func (n *Node) GetEventsByReasonSince(since time.Time, reason string) ([]Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`reason=` + reason + `,involvedObject.name=` + n.GetName())

	return eventList.GetAllSince(since)
}

// GetAllEventsSince returns a list of all the events related to this node since the provided date
func (n *Node) GetAllEventsSince(since time.Time) ([]Event, error) {
	eventList := NewEventList(n.oc, MachineConfigNamespace)
	eventList.ByFieldSelector(`involvedObject.name=` + n.GetName())

	return eventList.GetAllSince(since)
}

// GetAllEventsSinceEvent returns a list of all the events related to this node that occurred after the provided event
func (n *Node) GetAllEventsSinceEvent(since *Event) ([]Event, error) {
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

// GetEvents retunrs all the events that happened in this node since IgnoreEventsBeforeNow() method was called.
//
//	If IgnoreEventsBeforeNow() is not called, it returns all existing events for this node.
func (n *Node) GetEvents() ([]Event, error) {
	return n.GetAllEventsSince(n.eventCheckpoint)
}

// IgnoreEventsBeforeNow sets the event checkpoint to the latest event, so GetEvents will only return events after this point
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

// ExecuteDebugExpectBatch executes an interactive expect session in a debug pod for this node
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

// GetRHELVersion returns the RHEL version string from the node's /etc/os-release file
func (n *Node) GetRHELVersion() (string, error) {
	vContent, err := n.DebugNodeWithChroot("cat", "/etc/os-release")
	if err != nil {
		return "", err
	}

	r := regexp.MustCompile(`VERSION_ID="?(?P<rhel_version>[^"]+)"?`)
	match := r.FindStringSubmatch(vContent)
	if len(match) == 0 {
		msg := fmt.Sprintf("No RHEL_VERSION available in /etc/os-release file: %s", vContent)
		logger.Errorf(msg)
		return "", fmt.Errorf("Error: %s", msg)
	}

	rhelvIndex := r.SubexpIndex("rhel_version")
	rhelVersion := match[rhelvIndex]

	logger.Infof("Node %s RHEL_VERSION %s", n.GetName(), rhelVersion)
	return rhelVersion, nil
}
