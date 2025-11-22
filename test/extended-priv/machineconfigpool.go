package extended

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"k8s.io/apimachinery/pkg/util/wait"
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
		guessedNodes         = 3 // the number of nodes that we will use if we cannot get the actual number of nodes in the cluster
		masterAdjust         = 1.0
		snoModifier          = 0.0
		emptyMCPWaitDuration = 2.0
		minutesDuration      = 1 * time.Minute
	)

	err := Retry(5, 3*time.Second, func() error {
		var err error
		totalNodes, err = mcp.getMachineCount()
		return err
	})

	if err != nil {
		logger.Errorf("Not able to get the number of nodes in the %s MCP. Making a guess of %d nodes. Err: %s", mcp.GetName(), guessedNodes, err)
		totalNodes = guessedNodes
	}

	logger.Infof("Num nodes: %d, wait time per node %d minutes", totalNodes, mcp.MinutesWaitingPerNode)

	// If the pool has no node configured, we wait at least 2.0 minute.
	// There are tests that create pools with 0 nodes and wait for the pools to be updated. They cant wait 0 minutes.
	// We wait 2.0 minutes and not 1 minute because many functions do not poll immediately and they wait a 1 minute interval before starting to poll.
	// If we wait less than this interval the wait function will always fail
	if totalNodes == 0 {
		logger.Infof("Defining waiting time for pool with no nodes")
		return time.Duration(emptyMCPWaitDuration * float64(minutesDuration))
	}

	if mcp.IsMaster() {
		logger.Infof("Increase waiting time because it is master pool")
		masterAdjust = 1.3 // if the pool is the master pool, we wait an extra 30% time
	}

	// Because of https://issues.redhat.com/browse/OCPBUGS-37501 in SNO MCPs can take up to 3 minutes more to be updated because the MCC is not taking the lease properly
	if totalNodes == 1 {
		var isSNO bool
		err = Retry(5, 3*time.Second, func() error {
			var snoErr error
			isSNO, snoErr = IsSNOSafe(mcp.GetOC())
			return snoErr
		})
		if err != nil {
			logger.Errorf("Not able to know if the cluster is SNO. We guess it is SNO. Err: %s", err)
		}
		if isSNO || err != nil {
			logger.Infof("Increase waiting time because it is SNO")
			snoModifier = 3
		}
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

// GetNodesOrFail returns a list with the nodes that belong to the machine config pool and fail the test if any error happened
func (mcp *MachineConfigPool) GetNodesOrFail() []Node {
	ns, err := mcp.GetNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Cannot get the nodes in %s MCP", mcp.GetName())
	return ns
}

// GetCoreOsNodes returns a list with the CoreOs nodes that belong to the machine config pool
func (mcp *MachineConfigPool) GetCoreOsNodes() ([]Node, error) {
	return mcp.GetNodesByLabel("node.openshift.io/os_id=rhel")
}

// GetCoreOsNodesOrFail returns a list with the CoreOs nodes that belong to the machine config pool. If any error happens it fails the test.
func (mcp *MachineConfigPool) GetCoreOsNodesOrFail() []Node {
	ns, err := mcp.GetCoreOsNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Cannot get the coreOS nodes in %s MCP", mcp.GetName())
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

// WaitForNotDegradedStatus waits until MCP is not degraded, if the condition times out the returned error is != nil
func (mcp MachineConfigPool) WaitForNotDegradedStatus() error {
	timeToWait := mcp.estimateWaitDuration()
	logger.Infof("Waiting %s for MCP %s status to be not degraded.", timeToWait, mcp.name)

	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, false, func(_ context.Context) (bool, error) {
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
	return mcp.waitForConditionStatus("Updating", "True", 10*time.Minute, 5*time.Second, true)
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

	err := wait.PollUntilContextTimeout(context.TODO(), 30*time.Second, timeToWait, true, func(_ context.Context) (bool, error) {
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

	waitFunc := func(_ context.Context) (bool, error) {
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
	}

	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, false, waitFunc)
	if err != nil && !strings.Contains(err.Error(), "degraded") {
		mccLogs, logErr := NewController(mcp.GetOC()).GetLogs()
		if logErr != nil {
			logger.Errorf("Error getting MCC logs. Cannot check if drain is taking too long")
		} else {
			mccLatestLogs := GetLastNLines(mccLogs, 20)
			if strings.Contains(mccLatestLogs, "error when evicting") {
				logger.Infof("Some pods are taking too long to be evicted:\n%s", mccLatestLogs)
				logger.Infof("Waiting for MCP %s another round! %s", mcp.name, timeToWait)
				err = wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, true, waitFunc)
			}
		}
	}
	if err != nil {
		DebugDegradedStatus(mcp)
	}

	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), fmt.Sprintf("mc operation is not completed on mcp %s: %s", mcp.name, err))
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

// RecoverFromDegraded updates the current and desired machine configs so that the pool can recover from degraded state once the offending MC is deleted
func (mcp *MachineConfigPool) RecoverFromDegraded() error {
	logger.Infof("Recovering %s pool from degraded status", mcp.GetName())
	mcpNodes, _ := mcp.GetNodes()
	for _, node := range mcpNodes {
		logger.Infof("Restoring desired config in node: %s", node)
		isUpdated, err := node.IsUpdated()
		if err != nil {
			return fmt.Errorf("Error checking if node %s is updated: %s", node.GetName(), err)
		}
		if isUpdated {
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

// GetConfiguredMachineConfig returns the MachineConfig that is currently configured in this pool
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

	err := wait.PollUntilContextTimeout(context.TODO(), 1*time.Minute, timeToWait, true, func(_ context.Context) (bool, error) {
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
		return errors.New(message)
	}

	return nil
}

// GetMOSC returns the MachineOSConfig resource for this pool
func (mcp MachineConfigPool) GetMOSC() (*MachineOSConfig, error) {
	moscList := NewMachineOSConfigList(mcp.GetOC())
	moscList.SetItemsFilter(`?(@.spec.machineConfigPool.name=="` + mcp.GetName() + `")`)
	moscs, err := moscList.GetAll()
	if err != nil {
		return nil, err
	}
	if len(moscs) > 1 {
		moscList.PrintDebugCommand()
		return nil, fmt.Errorf("There are more than one MOSC for pool %s", mcp.GetName())
	}

	if len(moscs) == 0 {
		return nil, nil
	}
	return &(moscs[0]), nil
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

// waitForComplete waits until all MCP in the list are updated
func (mcpl *MachineConfigPoolList) waitForComplete() {
	mcps, err := mcpl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing MCP in the cluster")
	// We always wait for master first, to make sure that we avoid problems in SNO
	for _, mcp := range mcps {
		if mcp.IsMaster() {
			mcp.waitForComplete()
			break
		}
	}

	for _, mcp := range mcps {
		if !mcp.IsMaster() {
			mcp.waitForComplete()
		}
	}
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

// DebugDegradedStatus prints the necessary information to debug why a MCP became degraded
func DebugDegradedStatus(mcp *MachineConfigPool) {
	var (
		nodeList    = NewNodeList(mcp.GetOC())
		mcc         = NewController(mcp.GetOC())
		maxMCCLines = 30
		maxMCDLines = 30
	)

	logger.Infof("START DEBUG")
	_ = mcp.GetOC().Run("get").Args("co", "machine-config").Execute()
	_ = mcp.GetOC().Run("get").Args("mcp").Execute()
	_ = mcp.GetOC().Run("get").Args("nodes", "-o", "wide").Execute()
	logger.Infof("Not updated MCP %s", mcp.GetName())
	logger.Infof("%s", mcp.PrettyString())
	logger.Infof("#######################\n\n")
	allNodes, err := nodeList.GetAll()
	if err == nil {
		for _, node := range allNodes {
			state, err := node.GetMachineConfigState()
			if state != "Done" || err != nil {
				if err != nil {
					logger.Infof("Error getting machine config state for node %s: %v", node.GetName(), err)
				} else {
					logger.Infof("NODE %s IS %s", node.GetName(), state)
				}
				logger.Infof("%s", node.PrettyString())
				logger.Infof("#######################\n\n")
				mcdLogs, err := node.GetMCDaemonLogs("")
				if err != nil {
					logger.Infof("Error getting MCD logs for node %s", node.GetName())
				}
				logger.Infof("Node %s MCD logs:\n%s", node.GetName(), GetLastNLines(mcdLogs, maxMCDLines))
				logger.Infof("#######################\n\n")
				logger.Infof("MachineConfigNode:\n%s", node.GetMachineConfigNode().PrettyString())
				logger.Infof("#######################\n\n")
			}
		}
	} else {
		logger.Infof("Error getting the list of degraded nodes: %s", err)
	}

	mccLogs, err := mcc.GetLogs()
	if err != nil {
		logger.Infof("Error getting the logs from MCC: %s", err)
	}
	logger.Infof("Last %d lines of MCC:\n%s", maxMCCLines, GetLastNLines(mccLogs, maxMCCLines))
	logger.Infof("END DEBUG")
}
