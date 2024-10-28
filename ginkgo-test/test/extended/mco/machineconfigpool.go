package mco

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	"github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/architecture"
	"github.com/tidwall/gjson"
	"k8s.io/apimachinery/pkg/util/wait"

	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// CertExprity describes the information that MCPs are reporting about a given certificate.
type CertExpiry struct {
	// Bundle where the cert is storaged
	Bundle string `json:"bundle"`
	// Date fields have been temporarily removed by devs:  https://github.com/openshift/machine-config-operator/pull/3866
	// Expiry expiration date for the certificate
	Expiry string `json:"expiry"`
	// Subject certificate's subject
	Subject string `json:"subject"`
}

// MachineConfigPool struct is used to handle MachineConfigPool resources in OCP
type MachineConfigPool struct {
	template string
	Resource
	MinutesWaitingPerNode int
}

// MachineConfigPoolList struct handles list of MCPs
type MachineConfigPoolList struct {
	ResourceList
}

// NewMachineConfigPool create a NewMachineConfigPool struct
func NewMachineConfigPool(oc *exutil.CLI, name string) *MachineConfigPool {
	return &MachineConfigPool{Resource: *NewResource(oc, "mcp", name), MinutesWaitingPerNode: DefaultMinutesWaitingPerNode}
}

// MachineConfigPoolList construct a new node list struct to handle all existing nodes
func NewMachineConfigPoolList(oc *exutil.CLI) *MachineConfigPoolList {
	return &MachineConfigPoolList{*NewResourceList(oc, "mcp")}
}

// String implements the Stringer interface

func (mcp *MachineConfigPool) create() {
	exutil.CreateClusterResourceFromTemplate(mcp.oc, "--ignore-unknown-parameters=true", "-f", mcp.template, "-p", "NAME="+mcp.name)
	mcp.waitForComplete()
}

func (mcp *MachineConfigPool) delete() {
	logger.Infof("deleting custom mcp: %s", mcp.name)
	err := mcp.oc.AsAdmin().WithoutNamespace().Run("delete").Args("mcp", mcp.name, "--ignore-not-found=true").Execute()
	o.Expect(err).NotTo(o.HaveOccurred())
}

func (mcp *MachineConfigPool) pause(enable bool) {
	logger.Infof("patch mcp %v, change spec.paused to %v", mcp.name, enable)
	err := mcp.Patch("merge", `{"spec":{"paused": `+strconv.FormatBool(enable)+`}}`)
	o.Expect(err).NotTo(o.HaveOccurred())
}

// IsPaused return true is mcp is paused
func (mcp *MachineConfigPool) IsPaused() bool {
	return IsTrue(mcp.GetOrFail(`{.spec.paused}`))
}

// IsCustom returns true if the pool is not the master pool nor the worker pool
func (mcp *MachineConfigPool) IsCustom() bool {
	return !mcp.IsMaster() && !mcp.IsWorker()
}

// IsMaster returns true if the pool is the master pool
func (mcp *MachineConfigPool) IsMaster() bool {
	return mcp.GetName() == MachineConfigPoolMaster
}

// IsWorker returns true if the pool is the worker pool
func (mcp *MachineConfigPool) IsWorker() bool {
	return mcp.GetName() == MachineConfigPoolWorker
}

// IsEmpty returns true if the pool has no nodes
func (mcp *MachineConfigPool) IsEmpty() bool {
	var (
		numNodes int
	)

	o.Eventually(func() (err error) {
		numNodes, err = mcp.getMachineCount()
		return err
	}, "2m", "10s").Should(o.Succeed(),
		"It was not possible to get the status.machineCount value for MPC %s", mcp.GetName())
	return numNodes == 0
}

// GetMaxUnavailable gets the value of maxUnavailable
func (mcp *MachineConfigPool) GetMaxUnavailableInt() (int, error) {
	maxUnavailableString, err := mcp.Get(`{.spec.maxUnavailable}`)
	if err != nil {
		return -1, err
	}

	if maxUnavailableString == "" {
		logger.Infof("maxUnavailable not configured in mcp %s, default value is 1", mcp.GetName())
		return 1, nil
	}

	maxUnavailableInt, convErr := strconv.Atoi(maxUnavailableString)

	if convErr != nil {
		logger.Errorf("Error converting maxUnavailableString to integer: %s", convErr)
		return -1, convErr
	}

	return maxUnavailableInt, nil
}

// SetMaxUnavailable sets the value for maxUnavailable
func (mcp *MachineConfigPool) SetMaxUnavailable(maxUnavailable int) {
	logger.Infof("patch mcp %v, change spec.maxUnavailable to %d", mcp.name, maxUnavailable)
	err := mcp.Patch("merge", fmt.Sprintf(`{"spec":{"maxUnavailable": %d}}`, maxUnavailable))
	o.Expect(err).NotTo(o.HaveOccurred())
}

// RemoveMaxUnavailable removes spec.maxUnavailable attribute from the pool config
func (mcp *MachineConfigPool) RemoveMaxUnavailable() {
	logger.Infof("patch mcp %v, removing spec.maxUnavailable", mcp.name)
	err := mcp.Patch("json", `[{ "op": "remove", "path": "/spec/maxUnavailable" }]`)
	o.Expect(err).NotTo(o.HaveOccurred())
}

func (mcp *MachineConfigPool) getConfigNameOfSpec() (string, error) {
	output, err := mcp.Get(`{.spec.configuration.name}`)
	logger.Infof("spec.configuration.name of mcp/%v is %v", mcp.name, output)
	return output, err
}

func (mcp *MachineConfigPool) getConfigNameOfSpecOrFail() string {
	config, err := mcp.getConfigNameOfSpec()
	o.Expect(err).NotTo(o.HaveOccurred(), "Get config name of spec failed")
	return config
}

func (mcp *MachineConfigPool) getConfigNameOfStatus() (string, error) {
	output, err := mcp.Get(`{.status.configuration.name}`)
	logger.Infof("status.configuration.name of mcp/%v is %v", mcp.name, output)
	return output, err
}

func (mcp *MachineConfigPool) getConfigNameOfStatusOrFail() string {
	config, err := mcp.getConfigNameOfStatus()
	o.Expect(err).NotTo(o.HaveOccurred(), "Get config name of status failed")
	return config
}

func (mcp *MachineConfigPool) getMachineCount() (int, error) {
	machineCountStr, ocErr := mcp.Get(`{.status.machineCount}`)
	if ocErr != nil {
		logger.Infof("Error getting machineCount: %s", ocErr)
		return -1, ocErr
	}

	if machineCountStr == "" {
		return -1, fmt.Errorf(".status.machineCount value is not already set in MCP %s", mcp.GetName())
	}

	machineCount, convErr := strconv.Atoi(machineCountStr)

	if convErr != nil {
		logger.Errorf("Error converting machineCount to integer: %s", ocErr)
		return -1, convErr
	}

	return machineCount, nil
}

func (mcp *MachineConfigPool) getDegradedMachineCount() (int, error) {
	dmachineCountStr, ocErr := mcp.Get(`{.status.degradedMachineCount}`)
	if ocErr != nil {
		logger.Errorf("Error getting degradedmachineCount: %s", ocErr)
		return -1, ocErr
	}
	dmachineCount, convErr := strconv.Atoi(dmachineCountStr)

	if convErr != nil {
		logger.Errorf("Error converting degradedmachineCount to integer: %s", ocErr)
		return -1, convErr
	}

	return dmachineCount, nil
}

// getDegradedMachineCount returns the number of updated machines in the pool
func (mcp *MachineConfigPool) getUpdatedMachineCount() (int, error) {
	umachineCountStr, ocErr := mcp.Get(`{.status.updatedMachineCount}`)
	if ocErr != nil {
		logger.Errorf("Error getting updatedMachineCount: %s", ocErr)
		return -1, ocErr
	}
	umachineCount, convErr := strconv.Atoi(umachineCountStr)

	if convErr != nil {
		logger.Errorf("Error converting updatedMachineCount to integer: %s", ocErr)
		return -1, convErr
	}

	return umachineCount, nil
}

func (mcp *MachineConfigPool) pollMachineCount() func() string {
	return mcp.Poll(`{.status.machineCount}`)
}

func (mcp *MachineConfigPool) pollReadyMachineCount() func() string {
	return mcp.Poll(`{.status.readyMachineCount}`)
}

func (mcp *MachineConfigPool) pollDegradedMachineCount() func() string {
	return mcp.Poll(`{.status.degradedMachineCount}`)
}

// GetDegradedStatus returns the value of the 'Degraded' condition in the MCP
func (mcp *MachineConfigPool) GetDegradedStatus() (string, error) {
	return mcp.Get(`{.status.conditions[?(@.type=="Degraded")].status}`)
}

func (mcp *MachineConfigPool) pollDegradedStatus() func() string {
	return mcp.Poll(`{.status.conditions[?(@.type=="Degraded")].status}`)
}

// GetUpdatedStatus returns the value of the 'Updated' condition in the MCP
func (mcp *MachineConfigPool) GetUpdatedStatus() (string, error) {
	return mcp.Get(`{.status.conditions[?(@.type=="Updated")].status}`)
}

// GetUpdatingStatus returns the value of 'Updating' condition in the MCP
func (mcp *MachineConfigPool) GetUpdatingStatus() (string, error) {
	return mcp.Get(`{.status.conditions[?(@.type=="Updating")].status}`)
}

func (mcp *MachineConfigPool) pollUpdatedStatus() func() string {
	return mcp.Poll(`{.status.conditions[?(@.type=="Updated")].status}`)
}

func (mcp *MachineConfigPool) estimateWaitDuration() time.Duration {
	var (
		totalNodes           int
		masterAdjust         = 1.0
		snoModifier          = 0.0
		emptyMCPWaitDuration = 2.0
		minutesDuration      = 1 * time.Minute
	)

	o.Eventually(func() int {
		var err error
		totalNodes, err = mcp.getMachineCount()
		if err != nil {
			return -1
		}
		return totalNodes
	},
		"5m", "5s").Should(o.BeNumerically(">=", 0), fmt.Sprintf("machineCount field has no value in MCP %s", mcp.name))

	// If the pool has no node configured, we wait at least 2.0 minute.
	// There are tests that create pools with 0 nodes and wait for the pools to be updated. They cant wait 0 minutes.
	// We wait 2.0 minutes and not 1 minute because many functions do not poll immediately and they wait a 1 minute interval before starting to poll.
	// If we wait less than this interval the wait function will always fail
	if totalNodes == 0 {
		return time.Duration(emptyMCPWaitDuration * float64(minutesDuration))
	}

	if mcp.IsMaster() {
		masterAdjust = 1.3 // if the pool is the master pool, we wait an extra 30% time
	}

	// Because of https://issues.redhat.com/browse/OCPBUGS-37501 in SNO MCPs can take up to 3 minutes more to be updated because the MCC is not taking the lease properly
	if IsSNO(mcp.GetOC().AsAdmin()) {
		snoModifier = 3
	}

	return time.Duration(((float64(totalNodes*mcp.MinutesWaitingPerNode) * masterAdjust) + snoModifier) * float64(minutesDuration))
}

// SetWaitingTimeForKernelChange increases the time that the MCP will wait for the update to be executed
func (mcp *MachineConfigPool) SetWaitingTimeForKernelChange() {
	mcp.MinutesWaitingPerNode = DefaultMinutesWaitingPerNode + KernelChangeIncWait
}

// SetWaitingTimeForExtensionsChange increases the time that the MCP will wait for the update to be executed
func (mcp *MachineConfigPool) SetWaitingTimeForExtensionsChange() {
	mcp.MinutesWaitingPerNode = DefaultMinutesWaitingPerNode + ExtensionsChangeIncWait
}

// SetDefaultWaitingTime restore the default waiting time that the MCP will wait for the update to be executed
func (mcp *MachineConfigPool) SetDefaultWaitingTime() {
	mcp.MinutesWaitingPerNode = DefaultMinutesWaitingPerNode
}

// GetInternalIgnitionConfigURL return the internal URL used by the nodes in this pool to get the ignition config
func (mcp *MachineConfigPool) GetInternalIgnitionConfigURL(secure bool) (string, error) {
	var (
		// SecurePort is the tls secured port to serve ignition configs
		// InsecurePort is the port to serve ignition configs w/o tls
		port     = IgnitionSecurePort
		protocol = "https"
	)
	internalAPIServerURI, err := GetAPIServerInternalURI(mcp.oc)
	if err != nil {
		return "", err
	}
	if !secure {
		port = IgnitionInsecurePort
		protocol = "http"
	}

	return fmt.Sprintf("%s://%s:%d/config/%s", protocol, internalAPIServerURI, port, mcp.GetName()), nil
}

// GetMCSIgnitionConfig returns the ignition config that the MCS is serving for this pool
func (mcp *MachineConfigPool) GetMCSIgnitionConfig(secure bool, ignitionVersion string) (string, error) {
	var (
		// SecurePort is the tls secured port to serve ignition configs
		// InsecurePort is the port to serve ignition configs w/o tls
		port = IgnitionSecurePort
	)
	if !secure {
		port = IgnitionInsecurePort
	}

	url, err := mcp.GetInternalIgnitionConfigURL(secure)
	if err != nil {
		return "", err
	}

	// We will request the config from a master node
	mMcp := NewMachineConfigPool(mcp.oc.AsAdmin(), MachineConfigPoolMaster)
	masters, err := mMcp.GetNodes()
	if err != nil {
		return "", err
	}
	master := masters[0]

	logger.Infof("Remove the IPV4 iptables rules that block the ignition config")
	removedRules, err := master.RemoveIPTablesRulesByRegexp(fmt.Sprintf("%d", port))
	defer master.ExecIPTables(removedRules)
	if err != nil {
		return "", err
	}

	logger.Infof("Remove the IPV6 ip6tables rules that block the ignition config")
	removed6Rules, err := master.RemoveIP6TablesRulesByRegexp(fmt.Sprintf("%d", port))
	defer master.ExecIP6Tables(removed6Rules)
	if err != nil {
		return "", err
	}

	cmd := []string{"curl", "-s"}
	if secure {
		cmd = append(cmd, "-k")
	}
	if ignitionVersion != "" {
		cmd = append(cmd, []string{"-H", fmt.Sprintf("Accept:application/vnd.coreos.ignition+json;version=%s", ignitionVersion)}...)
	}
	cmd = append(cmd, url)

	stdout, stderr, err := master.DebugNodeWithChrootStd(cmd...)
	if err != nil {
		return stdout + stderr, err
	}
	return stdout, nil
}

// getSelectedNodes returns a list with the nodes that match the .spec.nodeSelector.matchLabels criteria plus the provided extraLabels
func (mcp *MachineConfigPool) getSelectedNodes(extraLabels string) ([]Node, error) {
	mcp.oc.NotShowInfo()
	defer mcp.oc.SetShowInfo()

	labelsString, err := mcp.Get(`{.spec.nodeSelector.matchLabels}`)
	if err != nil {
		return nil, err
	}
	labels := JSON(labelsString)
	o.Expect(labels.Exists()).Should(o.BeTrue(), fmt.Sprintf("The pool has no matchLabels value defined: %s", mcp.PrettyString()))

	nodeList := NewNodeList(mcp.oc)
	// Never select windows nodes
	requiredLabel := "kubernetes.io/os!=windows"
	if extraLabels != "" {
		requiredLabel += ","
		requiredLabel += extraLabels
	}
	for k, v := range labels.ToMap() {
		requiredLabel += fmt.Sprintf(",%s=%s", k, v.(string))
	}
	nodeList.ByLabel(requiredLabel)

	return nodeList.GetAll()
}

// GetNodesByLabel returns a list with the nodes that belong to the machine config pool and contain the given labels
func (mcp *MachineConfigPool) GetNodesByLabel(labels string) ([]Node, error) {
	mcp.oc.NotShowInfo()
	defer mcp.oc.SetShowInfo()

	nodes, err := mcp.getSelectedNodes(labels)
	if err != nil {
		return nil, err
	}

	returnNodes := []Node{}

	for _, item := range nodes {
		node := item
		primaryPool, err := node.GetPrimaryPool()
		if err != nil {
			return nil, err
		}

		if primaryPool.GetName() == mcp.GetName() {
			returnNodes = append(returnNodes, node)
		}
	}

	return returnNodes, nil
}

// GetNodes returns a list with the nodes that belong to the machine config pool, by default, windows nodes will be excluded
func (mcp *MachineConfigPool) GetNodes() ([]Node, error) {
	return mcp.GetNodesByLabel("")
}

// GetNodesWithoutArchitecture returns a list of nodes that belong to this pool and do NOT use the given architectures
func (mcp *MachineConfigPool) GetNodesWithoutArchitecture(arch architecture.Architecture, archs ...architecture.Architecture) ([]Node, error) {
	archsList := arch.String()
	for _, itemArch := range archs {
		archsList = archsList + "," + itemArch.String()
	}
	return mcp.GetNodesByLabel(fmt.Sprintf(`%s notin (%s)`, architecture.NodeArchitectureLabel, archsList))
}

// GetNodesWithoutArchitectureOrFail returns a list of nodes that belong to this pool and do NOT use the given architectures. It fails the test if any error happens
func (mcp *MachineConfigPool) GetNodesWithoutArchitectureOrFail(arch architecture.Architecture, archs ...architecture.Architecture) []Node {
	nodes, err := mcp.GetNodesWithoutArchitecture(arch)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "In MCP %s. Cannot get the nodes NOT using architectures %s", mcp.GetName(), append(archs, arch))
	return nodes
}

// GetNodesByArchitecture returns a list of nodes that belong to this pool and use the given architecture
func (mcp *MachineConfigPool) GetNodesByArchitecture(arch architecture.Architecture, archs ...architecture.Architecture) ([]Node, error) {
	archsList := arch.String()
	for _, itemArch := range archs {
		archsList = archsList + "," + itemArch.String()
	}
	return mcp.GetNodesByLabel(fmt.Sprintf(`%s in (%s)`, architecture.NodeArchitectureLabel, archsList))
}

// GetNodesByArchitecture returns a list of nodes that belong to this pool and use the given architecture. It fails the test if any error happens
func (mcp *MachineConfigPool) GetNodesByArchitectureOrFail(arch architecture.Architecture, archs ...architecture.Architecture) []Node {
	nodes, err := mcp.GetNodesByArchitecture(arch)
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "In MCP %s. Cannot get the nodes using architectures %s", mcp.GetName(), append(archs, arch))
	return nodes
}

// GetNodesOrFail returns a list with the nodes that belong to the machine config pool and fail the test if any error happened
func (mcp *MachineConfigPool) GetNodesOrFail() []Node {
	ns, err := mcp.GetNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Cannot get the nodes in %s MCP", mcp.GetName())
	return ns
}

// GetCoreOsNodes returns a list with the CoreOs nodes that belong to the machine config pool
func (mcp *MachineConfigPool) GetCoreOsNodes() ([]Node, error) {
	return mcp.GetNodesByLabel("node.openshift.io/os_id=rhcos")
}

// GetCoreOsNodesOrFail returns a list with the coreos nodes that belong to the machine config pool and fail the test if any error happened
func (mcp *MachineConfigPool) GetCoreOsNodesOrFail() []Node {
	ns, err := mcp.GetCoreOsNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Cannot get the coreOS nodes in %s MCP", mcp.GetName())
	return ns
}

// GetRhelNodes returns a list with the rhel nodes that belong to the machine config pool
func (mcp *MachineConfigPool) GetRhelNodes() ([]Node, error) {
	return mcp.GetNodesByLabel("node.openshift.io/os_id=rhel")
}

// GetRhelNodesOrFail returns a list with the rhel nodes that belong to the machine config pool and fail the test if any error happened
func (mcp *MachineConfigPool) GetRhelNodesOrFail() []Node {
	ns, err := mcp.GetRhelNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Cannot get the rhel nodes in %s MCP", mcp.GetName())
	return ns
}

// GetSortedNodes returns a list with the nodes that belong to the machine config pool in the same order used to update them
// when a configuration is applied
func (mcp *MachineConfigPool) GetSortedNodes() ([]Node, error) {

	poolNodes, err := mcp.GetNodes()
	if err != nil {
		return nil, err
	}

	if !mcp.IsMaster() {
		return sortNodeList(poolNodes), nil
	}

	return sortMasterNodeList(mcp.oc, poolNodes)

}

// GetSortedNodesOrFail returns a list with the nodes that belong to the machine config pool in the same order used to update them
// when a configuration is applied. If any error happens while getting the list, then the test is failed.
func (mcp *MachineConfigPool) GetSortedNodesOrFail() []Node {
	nodes, err := mcp.GetSortedNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(),
		"Cannot get the list of nodes that belong to '%s' MCP", mcp.GetName())

	return nodes
}

// GetSortedUpdatedNodes returns the list of the UpdatedNodes sorted by the time when they started to be updated.
// If maxUnavailable>0, then the function will fail if more that maxUpdatingNodes are being updated at the same time
func (mcp *MachineConfigPool) GetSortedUpdatedNodes(maxUnavailable int) []Node {
	timeToWait := mcp.estimateWaitDuration()
	logger.Infof("Waiting %s in pool %s for all nodes to start updating.", timeToWait, mcp.name)

	poolNodes, errget := mcp.GetNodes()
	o.Expect(errget).NotTo(o.HaveOccurred(), fmt.Sprintf("Cannot get nodes in pool %s", mcp.GetName()))

	pendingNodes := poolNodes
	updatedNodes := []Node{}
	immediate := false
	err := wait.PollUntilContextTimeout(context.TODO(), 20*time.Second, timeToWait, immediate, func(_ context.Context) (bool, error) {
		// If there are degraded machines, stop polling, directly fail
		degradedstdout, degradederr := mcp.getDegradedMachineCount()
		if degradederr != nil {
			logger.Errorf("the err:%v, and try next round", degradederr)
			return false, nil
		}

		if degradedstdout != 0 {
			logger.Errorf("Degraded MC:\n%s", mcp.PrettyString())
			exutil.AssertWaitPollNoErr(fmt.Errorf("Degraded machines"), fmt.Sprintf("mcp %s has degraded %d machines", mcp.name, degradedstdout))
		}

		// Check that there aren't more thatn maxUpdatingNodes updating at the same time
		if maxUnavailable > 0 {
			totalUpdating := 0
			for _, node := range poolNodes {
				if node.IsUpdating() {
					totalUpdating++
				}
			}
			if totalUpdating > maxUnavailable {
				// print nodes for debug
				mcp.oc.Run("get").Args("nodes").Execute()
				exutil.AssertWaitPollNoErr(fmt.Errorf("maxUnavailable Not Honored. Pool %s, error: %d nodes were updating at the same time. Only %d nodes should be updating at the same time", mcp.GetName(), totalUpdating, maxUnavailable), "")
			}
		}

		remainingNodes := []Node{}
		for _, node := range pendingNodes {
			if node.IsUpdating() {
				logger.Infof("Node %s is UPDATING", node.GetName())
				updatedNodes = append(updatedNodes, node)
			} else {
				remainingNodes = append(remainingNodes, node)
			}
		}

		if len(remainingNodes) == 0 {
			logger.Infof("All nodes have started to be updated on mcp %s", mcp.name)
			return true, nil

		}
		logger.Infof(" %d remaining nodes", len(remainingNodes))
		pendingNodes = remainingNodes
		return false, nil
	})

	exutil.AssertWaitPollNoErr(err, fmt.Sprintf("Could not get the list of updated nodes on mcp %s", mcp.name))
	return updatedNodes
}

// GetCordonedNodes get cordoned nodes (if maxUnavailable > 1 ) otherwise return the 1st cordoned node
func (mcp *MachineConfigPool) GetCordonedNodes() []Node {

	// requirement is: when pool is in updating state, get the updating node list
	o.Expect(mcp.WaitForUpdatingStatus()).NotTo(o.HaveOccurred(), "Waiting for Updating status change failed")
	// polling all nodes in this pool and check whether all cordoned nodes (SchedulingDisabled)
	var allUpdatingNodes []Node
	err := wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, 5*time.Minute, true, func(_ context.Context) (bool, error) {
		nodes, nerr := mcp.GetNodes()
		if nerr != nil {
			return false, fmt.Errorf("Get all linux node failed, will try again in next run %v", nerr)
		}
		for _, node := range nodes {
			schedulable, serr := node.IsSchedulable()
			if serr != nil {
				logger.Errorf("Checking node is schedulable failed %v", serr)
				continue
			}
			if !schedulable {
				allUpdatingNodes = append(allUpdatingNodes, node)
			}
		}

		return len(allUpdatingNodes) > 0, nil
	})

	exutil.AssertWaitPollNoErr(err, fmt.Sprintf("Could not get the list of updating nodes on mcp %s", mcp.GetName()))

	return allUpdatingNodes
}

// GetUnreconcilableNodes get all nodes that value of annotation machineconfiguration.openshift.io/state is Unreconcilable
func (mcp *MachineConfigPool) GetUnreconcilableNodes() ([]Node, error) {

	allUnreconcilableNodes := []Node{}
	allNodes, err := mcp.GetNodes()
	if err != nil {
		return nil, err
	}

	for _, n := range allNodes {
		state := n.GetAnnotationOrFail(NodeAnnotationState)
		if state == "Unreconcilable" {
			allUnreconcilableNodes = append(allUnreconcilableNodes, n)
		}
	}

	return allUnreconcilableNodes, nil
}

// GetUnreconcilableNodesOrFail get all nodes that value of annotation machineconfiguration.openshift.io/state is Unreconcilable
// fail the test if any error occurred
func (mcp *MachineConfigPool) GetUnreconcilableNodesOrFail() []Node {

	allUnreconcilableNodes, err := mcp.GetUnreconcilableNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Cannot get the unreconcilable nodes in %s MCP", mcp.GetName())

	return allUnreconcilableNodes
}

// WaitForNotDegradedStatus waits until MCP is not degraded, if the condition times out the returned error is != nil
func (mcp MachineConfigPool) WaitForNotDegradedStatus() error {
	timeToWait := mcp.estimateWaitDuration()
	logger.Infof("Waiting %s for MCP %s status to be not degraded.", timeToWait, mcp.name)

	immediate := false
	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, immediate, func(_ context.Context) (bool, error) {
		stdout, err := mcp.GetDegradedStatus()
		if err != nil {
			logger.Errorf("the err:%v, and try next round", err)
			return false, nil
		}
		if strings.Contains(stdout, "False") {
			logger.Infof("MCP degraded status is False %s", mcp.name)
			return true, nil
		}
		return false, nil
	})

	if err != nil {
		logger.Errorf("MCP: %s .Error waiting for not degraded status: %s", mcp.GetName(), err)
	}

	return err
}

// WaitForUpdatedStatus waits until MCP is rerpoting updated status, if the condition times out the returned error is != nil
func (mcp MachineConfigPool) WaitForUpdatedStatus() error {
	return mcp.waitForConditionStatus("Updated", "True", mcp.estimateWaitDuration(), 1*time.Minute, false)
}

// WaitImmediateForUpdatedStatus waits until MCP is rerpoting updated status, if the condition times out the returned error is != nil. It starts checking immediately.
func (mcp MachineConfigPool) WaitImmediateForUpdatedStatus() error {
	return mcp.waitForConditionStatus("Updated", "True", mcp.estimateWaitDuration(), 1*time.Minute, true)
}

// WaitForUpdatingStatus waits until MCP is rerpoting updating status, if the condition times out the returned error is != nil
func (mcp MachineConfigPool) WaitForUpdatingStatus() error {
	return mcp.waitForConditionStatus("Updating", "True", 5*time.Minute, 5*time.Second, true)
}

func (mcp MachineConfigPool) waitForConditionStatus(condition, status string, timeout, interval time.Duration, immediate bool) error {

	logger.Infof("Waiting %s for MCP %s condition %s to be %s", timeout, mcp.GetName(), condition, status)

	err := wait.PollUntilContextTimeout(context.TODO(), interval, timeout, immediate, func(_ context.Context) (bool, error) {
		stdout, err := mcp.Get(`{.status.conditions[?(@.type=="` + condition + `")].status}`)
		if err != nil {
			logger.Errorf("the err:%v, and try next round", err)
			return false, nil
		}
		if strings.Contains(stdout, status) {
			logger.Infof("MCP %s condition %s status is %s", mcp.GetName(), condition, stdout)
			return true, nil
		}
		return false, nil
	})

	if err != nil {
		logger.Errorf("MCP: %s .Error waiting for %s status: %s", mcp.GetName(), condition, err)
	}

	return err

}

// WaitForMachineCount waits until MCP is rerpoting the desired number of machineCount in the status, if the condition times out the returned error is != nil
func (mcp MachineConfigPool) WaitForMachineCount(expectedMachineCount int, timeToWait time.Duration) error {
	logger.Infof("Waiting %s for MCP %s to report %d machine count.", timeToWait, mcp.GetName(), expectedMachineCount)

	immediate := true
	err := wait.PollUntilContextTimeout(context.TODO(), 30*time.Second, timeToWait, immediate, func(_ context.Context) (bool, error) {
		mCount, err := mcp.getMachineCount()
		if err != nil {
			logger.Errorf("the err:%v, and try next round", err)
			return false, nil
		}
		if mCount == expectedMachineCount {
			logger.Infof("MCP is reporting %d machine count", mCount)
			return true, nil
		}
		logger.Infof("Expected machine count %d. Reported machine count %d", expectedMachineCount, mCount)
		return false, nil
	})

	if err != nil {
		logger.Errorf("MCP: %s .Error waiting for %d machine count: %s", mcp.GetName(), expectedMachineCount, err)
	}

	return err
}

func (mcp *MachineConfigPool) waitForComplete() {
	timeToWait := mcp.estimateWaitDuration()
	logger.Infof("Waiting %s for MCP %s to be completed.", timeToWait, mcp.name)

	immediate := false
	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, immediate, func(_ context.Context) (bool, error) {
		defer g.GinkgoRecover()
		// If there are degraded machines, stop polling, directly fail
		degradedstdout, degradederr := mcp.getDegradedMachineCount()
		if degradederr != nil {
			logger.Errorf("Error getting the number of degraded machines. Try next round: %s", degradederr)
			return false, nil
		}

		if degradedstdout != 0 {
			return true, fmt.Errorf("mcp %s has degraded %d machines", mcp.name, degradedstdout)
		}

		degradedStatus, err := mcp.GetDegradedStatus()
		if err != nil {
			logger.Errorf("Error getting degraded status.Try next round: %s", err)
			return false, nil
		}

		if degradedStatus != FalseString {
			return true, fmt.Errorf("mcp %s has degraded status: %s", mcp.name, degradedStatus)
		}

		stdout, err := mcp.Get(`{.status.conditions[?(@.type=="Updated")].status}`)
		if err != nil {
			logger.Errorf("the err:%v, and try next round", err)
			return false, nil
		}
		if strings.Contains(stdout, "True") {
			// i.e. mcp updated=true, mc is applied successfully
			logger.Infof("The new MC has been successfully applied to MCP '%s'", mcp.name)
			return true, nil
		}
		return false, nil
	})

	if err != nil {
		exutil.ArchiveMustGatherFile(mcp.GetOC(), extractJournalLogs)
		DebugDegradedStatus(mcp)
	}
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), fmt.Sprintf("mc operation is not completed on mcp %s: %s", mcp.name, err))
}

// GetPoolSynchronizersStatusByType return the PoolsynchronizesStatus matching the give type
func (mcp *MachineConfigPool) GetPoolSynchronizersStatusByType(pType string) (string, error) {
	return mcp.Get(`{.status.poolSynchronizersStatus[?(@.poolSynchronizerType=="` + pType + `")]}`)
}

// IsPinnedImagesComplete returns if the MCP is reporting that there is no pinnedimages operation in progress
func (mcp *MachineConfigPool) IsPinnedImagesComplete() (bool, error) {

	pinnedStatus, err := mcp.GetPoolSynchronizersStatusByType("PinnedImageSets")
	if err != nil {
		return false, err
	}

	mcpMachineCount, err := mcp.Get(`{.status.machineCount}`)
	if err != nil {
		return false, err
	}

	if mcpMachineCount == "" {
		return false, fmt.Errorf("status.machineCount is empty in mcp %s", mcp.GetName())
	}

	pinnedMachineCount := gjson.Get(pinnedStatus, "machineCount").String()
	if pinnedMachineCount == "" {
		return false, fmt.Errorf("pinned status machineCount is empty in mcp %s", mcp.GetName())
	}

	pinnedUnavailableMachineCount := gjson.Get(pinnedStatus, "unavailableMachineCount").String()
	if pinnedUnavailableMachineCount == "" {
		return false, fmt.Errorf("pinned status unavailableMachineCount is empty in mcp %s", mcp.GetName())
	}

	updatedMachineCount := gjson.Get(pinnedStatus, "updatedMachineCount").String()
	if updatedMachineCount == "" {
		return false, fmt.Errorf("pinned status updatedMachineCount is empty in mcp %s", mcp.GetName())
	}

	return mcpMachineCount == pinnedMachineCount && updatedMachineCount == pinnedMachineCount && pinnedUnavailableMachineCount == "0", nil
}

func (mcp *MachineConfigPool) allNodesReportingPinnedSuccess() (bool, error) {
	allNodes, err := mcp.GetNodes()
	if err != nil {
		return false, err
	}

	if len(allNodes) == 0 {
		logger.Infof("Warning, pool %s has no nodes!! We consider all nodes as correctly pinned", mcp.GetName())
	}

	for _, node := range allNodes {
		nodeMCN := node.GetMachineConfigNode()
		if nodeMCN.IsPinnedImageSetsDegraded() {
			logger.Infof("Node %s is pinned degraded. Condition:\n%s", node.GetName(), nodeMCN.GetConditionByType("PinnedImageSetsDegraded"))
			return false, nil
		}

		if nodeMCN.IsPinnedImageSetsProgressing() {
			return false, nil
		}
	}

	return true, nil
}

// waitForPinComplete waits until all images are pinned in the MCP. It fails the test case if the images are not pinned
func (mcp *MachineConfigPool) waitForPinComplete(timeToWait time.Duration) error {
	logger.Infof("Waiting %s for MCP %s to complete pinned images.", timeToWait, mcp.name)

	immediate := false
	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, immediate, func(_ context.Context) (bool, error) {
		pinnedComplete, err := mcp.IsPinnedImagesComplete()
		if err != nil {

			logger.Infof("Error getting pinned complete")
			return false, err
		}

		if !pinnedComplete {
			logger.Infof("Waiting for PinnedImageSets poolSynchronizersStatus status to repot success")
			return false, nil
		}

		allNodesComplete, err := mcp.allNodesReportingPinnedSuccess()
		if err != nil {
			logger.Infof("Error getting if all nodes finished")
			return false, err
		}

		if !allNodesComplete {
			logger.Infof("Waiting for all nodes to report pinned images success")
			return false, nil
		}

		logger.Infof("Pool %s successfully pinned the images! Complete!", mcp.GetName())
		return true, nil
	})

	if err != nil {
		logger.Infof("Pinned images operation is not completed on mcp %s", mcp.name)
	}
	return err
}

// waitForPinApplied waits until MCP reports that it has started to pin images, and then waits until all images are pinned. It fails the test case if the images are not pinned
// Because everything is cached in the pinnedimageset controller, it can happen that if the images are already pinned, the status change is too fast and we can miss it
// This is a problem when we execute test cases that have been previously executed. In order to use this method we need to make sure that the pinned images are not present in the nodes
// or the test will become unstable.
func (mcp *MachineConfigPool) waitForPinApplied(timeToWait time.Duration) error {
	logger.Infof("Waiting %s for MCP %s to apply pinned images.", timeToWait, mcp.name)

	immediate := true
	pinnedStarted := false
	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, immediate, func(_ context.Context) (bool, error) {
		pinnedComplete, err := mcp.IsPinnedImagesComplete()
		if err != nil {
			logger.Infof("Error getting pinned complete")
			return false, err
		}
		if !pinnedStarted && !pinnedComplete {
			pinnedStarted = true
			logger.Infof("Pool %s has started to pin images", mcp.GetName())
		}

		if pinnedStarted {
			if !pinnedComplete {
				logger.Infof("Waiting for PinnedImageSets poolSynchronizersStatus status to repot success")
				return false, nil
			}

			allNodesComplete, err := mcp.allNodesReportingPinnedSuccess()
			if err != nil {
				logger.Infof("Error getting if all nodes finished")
				return false, err
			}
			if !allNodesComplete {
				logger.Infof("Waiting for all nodes to report pinned images success")
				return false, nil
			}

			logger.Infof("Pool %s successfully pinned the images! Complete!", mcp.GetName())
			return true, nil
		}

		logger.Infof("Pool %s has not started to pin images yet", mcp.GetName())
		return false, nil
	})
	if err != nil {
		logger.Infof("Pinned images operation is not applied on mcp %s", mcp.name)
	}
	return err
}

// GetReportedOsImageOverrideValue returns the value of the os_image_url_override prometheus metric for this pool
func (mcp *MachineConfigPool) GetReportedOsImageOverrideValue() (string, error) {
	query := fmt.Sprintf(`os_image_url_override{pool="%s"}`, strings.ToLower(mcp.GetName()))

	mon, err := exutil.NewMonitor(mcp.oc.AsAdmin())
	if err != nil {
		return "", err
	}

	osImageOverride, err := mon.SimpleQuery(query)
	if err != nil {
		return "", err
	}

	jsonOsImageOverride := JSON(osImageOverride)
	status := jsonOsImageOverride.Get("status").ToString()
	if status != "success" {
		return "", fmt.Errorf("Query %s execution failed: %s", query, osImageOverride)
	}

	logger.Infof("%s metric is:%s", query, osImageOverride)

	metricValue := JSON(osImageOverride).Get("data").Get("result").Item(0).Get("value").Item(1).ToString()
	return metricValue, nil
}

// RecoverFromDegraded updates the current and desired machine configs so that the pool can recover from degraded state once the offending MC is deleted
func (mcp *MachineConfigPool) RecoverFromDegraded() error {
	logger.Infof("Recovering %s pool from degraded status", mcp.GetName())
	mcpNodes, _ := mcp.GetNodes()
	for _, node := range mcpNodes {
		logger.Infof("Restoring desired config in node: %s", node)
		if node.IsUpdated() {
			logger.Infof("node is updated, don't need to recover")
		} else {
			err := node.RestoreDesiredConfig()
			if err != nil {
				return fmt.Errorf("Error restoring desired config in node %s. Error: %s",
					mcp.GetName(), err)
			}
		}
	}

	derr := mcp.WaitForNotDegradedStatus()
	if derr != nil {
		logger.Infof("Could not recover from the degraded status: %s", derr)
		return derr
	}

	uerr := mcp.WaitForUpdatedStatus()
	if uerr != nil {
		logger.Infof("Could not recover from the degraded status: %s", uerr)
		return uerr
	}

	return nil
}

// IsRealTimeKernel returns true if the pool is using a realtime kernel
func (mcp *MachineConfigPool) IsRealTimeKernel() (bool, error) {
	nodes, err := mcp.GetNodes()
	if err != nil {
		logger.Errorf("Error getting the nodes in pool %s", mcp.GetName())
		return false, err
	}

	return nodes[0].IsRealTimeKernel()
}

// GetConfiguredMachineConfig return the MachineConfig currently configured in the pool
func (mcp *MachineConfigPool) GetConfiguredMachineConfig() (*MachineConfig, error) {
	currentMcName, err := mcp.Get("{.status.configuration.name}")
	if err != nil {
		logger.Errorf("Error getting the currently configured MC in pool %s: %s", mcp.GetName(), err)
		return nil, err
	}

	logger.Debugf("The currently configured MC in pool %s is: %s", mcp.GetName(), currentMcName)
	return NewMachineConfig(mcp.oc, currentMcName, mcp.GetName()), nil
}

// SanityCheck returns an error if the MCP is Degraded or Updating.
// We can't use WaitForUpdatedStatus or WaitForNotDegradedStatus because they always wait the interval. In a sanity check we want a fast response.
func (mcp *MachineConfigPool) SanityCheck() error {
	timeToWait := mcp.estimateWaitDuration() / 13
	logger.Infof("Waiting %s for MCP %s to be completed.", timeToWait.Round(time.Second), mcp.name)

	const trueStatus = "True"
	var message string

	immediate := true
	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, immediate, func(_ context.Context) (bool, error) {
		// If there are degraded machines, stop polling, directly fail
		degraded, degradederr := mcp.GetDegradedStatus()
		if degradederr != nil {
			message = fmt.Sprintf("Error gettting Degraded status: %s", degradederr)
			return false, nil
		}

		if degraded == trueStatus {
			message = fmt.Sprintf("MCP '%s' is degraded", mcp.GetName())
			return false, nil
		}

		updated, err := mcp.GetUpdatedStatus()
		if err != nil {
			message = fmt.Sprintf("Error gettting Updated status: %s", err)
			return false, nil
		}
		if updated == trueStatus {
			logger.Infof("MCP '%s' is ready for testing", mcp.name)
			return true, nil
		}
		message = fmt.Sprintf("MCP '%s' is not updated", mcp.GetName())
		return false, nil
	})

	if err != nil {
		return fmt.Errorf(message)
	}

	return nil
}

// GetCertsExpiry returns the information about the certificates trackec by the MCP
func (mcp *MachineConfigPool) GetCertsExpiry() ([]CertExpiry, error) {
	expiryString, err := mcp.Get(`{.status.certExpirys}`)
	if err != nil {
		return nil, err
	}

	var certsExp []CertExpiry

	jsonerr := json.Unmarshal([]byte(expiryString), &certsExp)

	if jsonerr != nil {
		return nil, jsonerr
	}

	return certsExp, nil
}

// GetArchitectures returns the list of architectures that the nodes in this pool are using
func (mcp *MachineConfigPool) GetArchitectures() ([]architecture.Architecture, error) {
	archs := []architecture.Architecture{}
	nodes, err := mcp.GetNodes()
	if err != nil {
		return archs, err
	}

	for _, node := range nodes {
		archs = append(archs, node.GetArchitectureOrFail())
	}

	return archs, nil
}

// GetArchitecturesOrFail returns the list of architectures that the nodes in this pool are using, if there is any error it fails the test
func (mcp *MachineConfigPool) GetArchitecturesOrFail() []architecture.Architecture {
	archs, err := mcp.GetArchitectures()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the architectures used by nodes in MCP %s", mcp.GetName())
	return archs
}

// AllNodesUseArch return true if all the nodes in the pool has the given architecture
func (mcp *MachineConfigPool) AllNodesUseArch(arch architecture.Architecture) bool {
	for _, currentArch := range mcp.GetArchitecturesOrFail() {
		if arch != currentArch {
			return false
		}
	}
	return true
}

// CaptureAllNodeLogsBeforeRestart will poll the logs of every node in the pool until thy are restarted and will return them once all nodes have been restarted
func (mcp *MachineConfigPool) CaptureAllNodeLogsBeforeRestart() (map[string]string, error) {

	type nodeLogs struct {
		nodeName string
		nodeLogs string
		err      error
	}

	returnMap := map[string]string{}
	c := make(chan nodeLogs)
	var wg sync.WaitGroup

	timeToWait := mcp.estimateWaitDuration()

	logger.Infof("Waiting %s until all nodes nodes %s MCP are restarted and their logs are captured before restart", timeToWait.String(), mcp.GetName())

	nodes, err := mcp.GetNodes()
	if err != nil {
		return nil, err
	}

	for _, item := range nodes {
		node := item
		wg.Add(1)
		go func() {
			defer g.GinkgoRecover()
			defer wg.Done()

			logger.Infof("Capturing node %s logs until restart", node.GetName())
			logs, err := node.CaptureMCDaemonLogsUntilRestartWithTimeout(timeToWait.String())
			if err != nil {
				logger.Errorf("Error while tring to capture the MCD lgos in node %s before restart", node.GetName())
			} else {
				logger.Infof("Captured MCD logs before node %s was rebooted", node.GetName())
			}

			c <- nodeLogs{nodeName: node.GetName(), nodeLogs: logs, err: err}
		}()
	}

	// We are using a 0 size channel, so every previous channel call will be locked on "c <- nodeLogs" if we directly call wg.Wait because noone is already reading
	// One solution is to wait inside a goroutine and close the channel once every node has reported his log
	// Another solution could be to use a channel with a size = len(nodes) like `c := make(chan nodeLogs, len(nodes))` so that all tasks can write in the channel without being locked
	go func() {
		defer g.GinkgoRecover()

		logger.Infof("Waiting for all pre-reboot logs to be collected")
		wg.Wait()
		logger.Infof("All logs collected. Closing the channel")
		close(c)
	}()

	// Here we read from the channel and unlock the "c <- nodeLogs" instruction. If we call wg.Wait before this point, tasks will be locked there forever
	for nl := range c {
		if nl.err != nil {
			return nil, err
		}
		returnMap[nl.nodeName] = nl.nodeLogs
	}

	return returnMap, nil
}

// GetPinnedImageSets returns a list with the nodes that match the .spec.nodeSelector.matchLabels criteria plus the provided extraLabels
func (mcp *MachineConfigPool) GetPinnedImageSets() ([]PinnedImageSet, error) {
	mcp.oc.NotShowInfo()
	defer mcp.oc.SetShowInfo()

	labelsString, err := mcp.Get(`{.spec.machineConfigSelector.matchLabels}`)
	if err != nil {
		return nil, err
	}

	if labelsString == "" {
		return nil, fmt.Errorf("No machineConfigSelector found in %s", mcp)
	}

	labels := gjson.Parse(labelsString)

	requiredLabel := ""
	labels.ForEach(func(key, value gjson.Result) bool {
		requiredLabel += fmt.Sprintf("%s=%s,", key.String(), value.String())
		return true // keep iterating
	})

	if requiredLabel == "" {
		return nil, fmt.Errorf("No labels matcher could be built for %s", mcp)
	}
	// remove the last comma
	requiredLabel = strings.TrimSuffix(requiredLabel, ",")

	pisList := NewPinnedImageSetList(mcp.oc)
	pisList.ByLabel(requiredLabel)

	return pisList.GetAll()
}

// Reboot reboot all nodes in the pool by using command "oc adm reboot-machine-config-pool mcp/POOLNAME"
func (mcp *MachineConfigPool) Reboot() error {
	logger.Infof("Rebooting nodes in pool %s", mcp.GetName())
	return mcp.oc.WithoutNamespace().Run("adm").Args("reboot-machine-config-pool", "mcp/"+mcp.GetName()).Execute()
}

// WaitForRebooted wait for the "Reboot" method to actually reboot all nodes by using command "oc adm wait-for-node-reboot nodes -l node-role.kubernetes.io/POOLNAME"
func (mcp *MachineConfigPool) WaitForRebooted() error {
	logger.Infof("Waiting for nodes in pool %s to be rebooted", mcp.GetName())
	return mcp.oc.WithoutNamespace().Run("adm").Args("wait-for-node-reboot", "nodes", "-l", "node-role.kubernetes.io/"+mcp.GetName()).Execute()
}

// GetLatestMachineOSBuild returns the latest MachineOSBuild created for this MCP
func (mcp *MachineConfigPool) GetLatestMachineOSBuildOrFail() *MachineOSBuild {
	return NewMachineOSBuild(mcp.oc, fmt.Sprintf("%s-%s-builder", mcp.GetName(), mcp.getConfigNameOfSpecOrFail()))
}

// GetAll returns a []MachineConfigPool list with all existing machine config pools sorted by creation time
func (mcpl *MachineConfigPoolList) GetAll() ([]MachineConfigPool, error) {
	mcpl.ResourceList.SortByTimestamp()
	allMCPResources, err := mcpl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMCPs := make([]MachineConfigPool, 0, len(allMCPResources))

	for _, mcpRes := range allMCPResources {
		allMCPs = append(allMCPs, *NewMachineConfigPool(mcpl.oc, mcpRes.name))
	}

	return allMCPs, nil
}

// GetAllOrFail returns a []MachineConfigPool list with all existing machine config pools sorted by creation time, if any error happens it fails the test
func (mcpl *MachineConfigPoolList) GetAllOrFail() []MachineConfigPool {
	mcps, err := mcpl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing MCP in the cluster")
	return mcps
}

// GetCompactCompatiblePool returns worker pool if the cluster is not compact/SNO. Else it will return master pool or custom pool if worker pool is empty.
// Current logic:
// If worker pool has nodes, we return worker pool
// Else if worker pool is empty
//
//		If custom pools exist
//			If any custom pool has nodes, we return the custom pool
//	     	Else (all custom pools are empty) we are in a Compact/SNO cluster with extra empty custom pools, we return master
//		Else (worker pool is empty and there is no custom pool) we are in a Compact/SNO cluster, we return master
func GetCompactCompatiblePool(oc *exutil.CLI) *MachineConfigPool {
	var (
		wMcp    = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mMcp    = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		mcpList = NewMachineConfigPoolList(oc)
	)

	mcpList.PrintDebugCommand()

	if IsCompactOrSNOCluster(oc) {
		return mMcp
	}

	if !wMcp.IsEmpty() {
		return wMcp
	}

	// The cluster is not Compact/SNO but the the worker pool is empty. All nodes have been moved to one or several custom pool
	for _, mcp := range mcpList.GetAllOrFail() {
		if mcp.IsCustom() && !mcp.IsEmpty() { // All worker pools were moved to cutom pools
			logger.Infof("Worker pool is empty, but there is a custom pool with nodes. Proposing %s MCP for testing", mcp.GetName())
			return &mcp
		}
	}

	e2e.Failf("Something went wrong. There is no suitable pool to execute the test case")
	return nil
}

// GetCoreOsCompatiblePool returns worker pool if it has CoreOs nodes. If there is no CoreOs node in the worker pool, then it returns master pool.
func GetCoreOsCompatiblePool(oc *exutil.CLI) *MachineConfigPool {
	var (
		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
	)

	if len(wMcp.GetCoreOsNodesOrFail()) == 0 {
		logger.Infof("No CoreOs nodes in the worker pool. Using master pool for testing")
		return mMcp
	}

	return wMcp
}

// CreateCustomMCPByLabel Creates a new custom MCP using the nodes in the worker pool with the given label. If numNodes < 0, we will add all existing nodes to the custom pool
// If numNodes == 0, no node will be added to the new custom pool.
func CreateCustomMCPByLabel(oc *exutil.CLI, name, label string, numNodes int) (*MachineConfigPool, error) {
	wMcp := NewMachineConfigPool(oc, MachineConfigPoolWorker)
	nodes, err := wMcp.GetNodesByLabel(label)
	if err != nil {
		logger.Errorf("Could not get the nodes with %s label", label)
		return nil, err
	}

	if len(nodes) < numNodes {
		return nil, fmt.Errorf("The worker MCP only has %d nodes, it is not possible to take %d nodes from worker pool to create a custom pool",
			len(nodes), numNodes)
	}

	customMcpNodes := []Node{}
	for i, item := range nodes {
		n := item
		if numNodes > 0 && i >= numNodes {
			break
		}
		customMcpNodes = append(customMcpNodes, n)
	}

	return CreateCustomMCPByNodes(oc, name, customMcpNodes)
}

// CreateCustomMCP create a new custom MCP with the given name and the given number of nodes
// Nodes will be taken from the worker pool
func CreateCustomMCP(oc *exutil.CLI, name string, numNodes int) (*MachineConfigPool, error) {
	var (
		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
	)

	workerNodes, err := wMcp.GetNodes()
	if err != nil {
		return NewMachineConfigPool(oc, name), err
	}

	if numNodes > len(workerNodes) {
		return NewMachineConfigPool(oc, name), fmt.Errorf("A %d nodes custom pool cannot be created because there are only %d nodes in the %s pool",
			numNodes, len(workerNodes), wMcp.GetName())
	}

	return CreateCustomMCPByNodes(oc, name, workerNodes[0:numNodes])
}

// CreateCustomMCPByNodes creates a new MCP containing the nodes provided in the "nodes" parameter
func CreateCustomMCPByNodes(oc *exutil.CLI, name string, nodes []Node) (*MachineConfigPool, error) {
	customMcp := NewMachineConfigPool(oc, name)

	err := NewMCOTemplate(oc, "custom-machine-config-pool.yaml").Create("-p", fmt.Sprintf("NAME=%s", name))
	if err != nil {
		logger.Errorf("Could not create a custom MCP for worker nodes with nodes %s", nodes)
		return customMcp, err
	}

	for _, n := range nodes {
		err := n.AddLabel(fmt.Sprintf("node-role.kubernetes.io/%s", name), "")
		if err != nil {
			logger.Infof("Error labeling node %s to add it to pool %s", n.GetName(), customMcp.GetName())
		}
		logger.Infof("Node %s added to custom pool %s", n.GetName(), customMcp.GetName())
	}

	expectedNodes := len(nodes)
	err = customMcp.WaitForMachineCount(expectedNodes, 5*time.Minute)
	if err != nil {
		logger.Errorf("The %s MCP is not reporting the expected machine count", customMcp.GetName())
		return customMcp, err
	}

	err = customMcp.WaitImmediateForUpdatedStatus()
	if err != nil {
		logger.Errorf("The %s MCP is not updated", customMcp.GetName())
		return customMcp, err
	}

	return customMcp, nil
}

// DeleteCustomMCP deletes a custom MCP properly unlabeling the nodes first
func DeleteCustomMCP(oc *exutil.CLI, name string) error {
	mcp := NewMachineConfigPool(oc, name)
	if !mcp.Exists() {
		logger.Infof("MCP %s does not exist. No need to remove it", mcp.GetName())
		return nil
	}

	exutil.By(fmt.Sprintf("Removing custom MCP %s", name))

	nodes, err := mcp.GetNodes()
	if err != nil {
		logger.Errorf("Could not get the nodes that belong to MCP %s: %s", mcp.GetName(), err)
		return err
	}

	label := fmt.Sprintf("node-role.kubernetes.io/%s", mcp.GetName())
	for _, node := range nodes {
		logger.Infof("Removing pool label from node %s", node.GetName())
		err := node.RemoveLabel(label)
		if err != nil {
			logger.Errorf("Could not remove the role label from node %s: %s", node.GetName(), err)
			return err
		}
	}

	for _, node := range nodes {
		err := node.WaitForLabelRemoved(label)
		if err != nil {
			logger.Errorf("The label %s was not removed from node %s", label, node.GetName())
		}
	}

	err = mcp.WaitForMachineCount(0, 5*time.Minute)
	if err != nil {
		logger.Errorf("The %s MCP already contains nodes, it cannot be deleted: %s", mcp.GetName(), err)
		return err
	}

	// Wait for worker MCP to be updated before removing the custom pool
	// in order to make sure that no node has any annotation pointing to resources that depend on the custom pool that we want to delete
	wMcp := NewMachineConfigPool(oc, MachineConfigPoolWorker)
	err = wMcp.WaitForUpdatedStatus()
	if err != nil {
		logger.Errorf("The worker MCP was not ready after removing the custom pool: %s", err)
		wMcp.PrintDebugCommand()
		return err
	}

	err = mcp.Delete()
	if err != nil {
		logger.Errorf("The %s MCP could not be deleted: %s", mcp.GetName(), err)
		return err
	}

	logger.Infof("OK!\n")
	return nil
}

// GetPoolAndNodesForArchitectureOrFail returns a MCP in this order of priority:
// 1) The master pool if it is a arm64 compact/SNO cluster.
// 2) A custom pool with 1 arm node in it if there are arm nodes in the worker pool.
// 3) Any existing custom MCP with all nodes using arm64
// 4) The master pools if the master pool is arm64
func GetPoolAndNodesForArchitectureOrFail(oc *exutil.CLI, createMCPName string, arch architecture.Architecture, numNodes int) (*MachineConfigPool, []Node) {
	var (
		wMcp                  = NewMachineConfigPool(oc, MachineConfigPoolWorker)
		mMcp                  = NewMachineConfigPool(oc, MachineConfigPoolMaster)
		masterHasTheRightArch = mMcp.AllNodesUseArch(arch)
		mcpList               = NewMachineConfigPoolList(oc)
	)

	mcpList.PrintDebugCommand()

	if masterHasTheRightArch && IsCompactOrSNOCluster(oc) {
		return mMcp, mMcp.GetNodesOrFail()
	}

	// we check if there is an already existing pool with all its nodes using the requested architecture
	for _, pool := range mcpList.GetAllOrFail() {
		if !pool.IsCustom() {
			continue
		}

		// If there isn't a node with the requested architecture in the worker pool,
		// but there is a custom pool where all nodes have this architecture
		if !pool.IsEmpty() && pool.AllNodesUseArch(arch) {
			logger.Infof("Using the predefined MCP %s", pool.GetName())
			return &pool, pool.GetNodesOrFail()
		}
		logger.Infof("The predefined %s MCP exists, but it is not suitable for testing", pool.GetName())
	}

	// If there are nodes with the rewquested architecture in the worker pool we build our own custom MCP
	if len(wMcp.GetNodesByArchitectureOrFail(arch)) > 0 {
		var err error

		mcp, err := CreateCustomMCPByLabel(oc.AsAdmin(), createMCPName, fmt.Sprintf(`%s=%s`, architecture.NodeArchitectureLabel, arch), numNodes)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the custom pool for infrastructure %s", architecture.ARM64)
		return mcp, mcp.GetNodesOrFail()

	}

	// If we are in a HA cluster but worker nor custom pools meet the achitecture conditions for the test
	// we return the master pool if it is using the right architecture
	if masterHasTheRightArch {
		logger.Infof("The cluster is not a Compact/SNO cluster and there are no %s worker nodes available for testing. We will use the master pool.", arch)
		return mMcp, mMcp.GetNodesOrFail()
	}

	e2e.Failf("Something went wrong. There is no suitable pool to execute the test case using architecture %s", arch)
	return nil, nil
}

// GetPoolAndNodesForNoArchitectureOrFail returns a MCP in this order of priority:
// 1) The master pool if it is a arm64 compact/SNO cluster.
// 2) First pool that is not master and contains any node NOT using the given architecture
func GetPoolWithArchDifferentFromOrFail(oc *exutil.CLI, arch architecture.Architecture) *MachineConfigPool {
	var (
		mcpList = NewMachineConfigPoolList(oc)
		mMcp    = NewMachineConfigPool(oc, MachineConfigPoolMaster)
	)

	mcpList.PrintDebugCommand()

	// we check if there is an already existing pool with all its nodes using the requested architecture
	for _, pool := range mcpList.GetAllOrFail() {
		if pool.IsMaster() {
			continue
		}

		// If there isn't a node with the requested architecture in the worker pool,
		// but there is a custom pool where all nodes have this architecture
		if !pool.IsEmpty() && len(pool.GetNodesWithoutArchitectureOrFail(arch)) > 0 {
			logger.Infof("Using pool %s", pool.GetName())
			return &pool
		}
	}

	// It includes compact and SNO
	if len(mMcp.GetNodesWithoutArchitectureOrFail(arch)) > 0 {
		return mMcp
	}

	e2e.Failf("Something went wrong. There is no suitable pool to execute the test case. There is no pool with nodes using  an architecture different from %s", arch)
	return nil
}

// DebugDegradedStatus prints the necessary information to debug why a MCP became degraded
func DebugDegradedStatus(mcp *MachineConfigPool) {
	var (
		nodeList    = NewNodeList(mcp.GetOC())
		mcc         = NewController(mcp.GetOC())
		maxMCCLines = 30
		maxMCDLines = 30
	)
	_ = mcp.GetOC().Run("get").Args("co", "machine-config").Execute()
	_ = mcp.GetOC().Run("get").Args("mcp").Execute()
	_ = mcp.GetOC().Run("get").Args("nodes", "-o", "wide").Execute()

	logger.Infof("Not updated MCP %s", mcp.GetName())
	logger.Infof("%s", mcp.PrettyString())
	logger.Infof("#######################\n\n")
	allNodes, err := nodeList.GetAll()
	if err == nil {
		for _, node := range allNodes {
			state := node.GetMachineConfigState()
			if state != "Done" {
				logger.Infof("NODE %s IS %s", node.GetName(), state)
				logger.Infof("%s", node.PrettyString())
				logger.Infof("#######################\n\n")
				mcdLogs, err := node.GetMCDaemonLogs("")
				if err != nil {
					logger.Infof("Error getting MCD logs for node %s", node.GetName())
				}
				mcdLines := strings.Split(mcdLogs, "\n")
				lenMCDLogs := len(mcdLines)

				if lenMCDLogs > maxMCDLines {
					mcdLogs = strings.Join(mcdLines[lenMCDLogs-maxMCCLines:], "\n")
				}
				logger.Infof("Node %s MCD logs:\n%s", node.GetName(), mcdLogs)
				logger.Infof("#######################\n\n")
				logger.Infof("MachineConfigNode:\n%s", node.GetMachineConfigNode().PrettyString())
				logger.Infof("#######################\n\n")
			}
		}
	} else {
		logger.Infof("Error getting the list of degraded nodes: %s", err)
	}

	mccLogLines, err := mcc.GetLogsAsList()
	if err != nil {
		logger.Infof("Error getting the logs from MCC: %s", err)
	} else {
		mccLogs := strings.Join(mccLogLines, "\n")
		lenLogs := len(mccLogLines)
		if lenLogs > maxMCCLines {
			mccLogs = strings.Join(mccLogLines[lenLogs-maxMCCLines:], "\n")
		}
		logger.Infof("Last %d lines of MCC:\n%s", maxMCCLines, mccLogs)
	}
}
