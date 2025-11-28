package extended

import (
	"context"
	"fmt"
	"strconv"
	"time"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"k8s.io/apimachinery/pkg/util/wait"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

const (
	// ControlPlaneMachineSetName is the name of the singleton ControlPlaneMachineSet resource
	ControlPlaneMachineSetName = "cluster"
)

// BootImageResource is an interface for resources that have boot images (MachineSet and ControlPlaneMachineSet)
type BootImageResource interface {
	GetOC() *exutil.CLI
	GetArchitectureOrFail() architecture.Architecture
	GetCoreOsBootImage() (string, error)
	String() string
}

// ControlPlaneMachineSet struct to handle ControlPlaneMachineSet resources
type ControlPlaneMachineSet struct {
	Resource
}

// ControlPlaneMachineSetList struct to handle lists of ControlPlaneMachineSet resources
type ControlPlaneMachineSetList struct {
	ResourceList
}

// NewControlPlaneMachineSet constructs a new ControlPlaneMachineSet struct
func NewControlPlaneMachineSet(oc *exutil.CLI, namespace, name string) *ControlPlaneMachineSet {
	return &ControlPlaneMachineSet{*NewNamespacedResource(oc, "controlplanemachineset", namespace, name)}
}

// NewControlPlaneMachineSetList constructs a new ControlPlaneMachineSetList struct to handle all existing ControlPlaneMachineSets
func NewControlPlaneMachineSetList(oc *exutil.CLI, namespace string) *ControlPlaneMachineSetList {
	return &ControlPlaneMachineSetList{*NewNamespacedResourceList(oc, "controlplanemachineset", namespace)}
}

// GetState returns the state of the ControlPlaneMachineSet (Active or Inactive)
func (cpms ControlPlaneMachineSet) GetState() (string, error) {
	return cpms.Get(`{.spec.state}`)
}

// GetStateOrFail returns the state of the ControlPlaneMachineSet and fails the test if any error happens
func (cpms ControlPlaneMachineSet) GetStateOrFail() string {
	state, err := cpms.GetState()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting state from %s", cpms)
	return state
}

// IsActive returns true if the ControlPlaneMachineSet is Active
func (cpms ControlPlaneMachineSet) IsActive() bool {
	state, err := cpms.GetState()
	if err != nil {
		logger.Errorf("Error getting state: %s", err)
		return false
	}
	return state == "Active"
}

// GetReplicas returns the number of replicas configured
func (cpms ControlPlaneMachineSet) GetReplicas() (int, error) {
	replicasStr, err := cpms.Get(`{.spec.replicas}`)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(replicasStr)
}

// GetReplicasOrFail returns the number of replicas and fails the test if any error happens
func (cpms ControlPlaneMachineSet) GetReplicasOrFail() int {
	replicas, err := cpms.GetReplicas()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting replicas from %s", cpms)
	return replicas
}

// GetReadyReplicas returns the number of ready replicas
func (cpms ControlPlaneMachineSet) GetReadyReplicas() (int, error) {
	readyReplicasStr, err := cpms.Get(`{.status.readyReplicas}`)
	if err != nil {
		return -1, err
	}
	if readyReplicasStr == "" {
		return 0, nil
	}
	return strconv.Atoi(readyReplicasStr)
}

// GetUpdatedReplicas returns the number of updated replicas
func (cpms ControlPlaneMachineSet) GetUpdatedReplicas() (int, error) {
	updatedReplicasStr, err := cpms.Get(`{.status.updatedReplicas}`)
	if err != nil {
		return -1, err
	}
	if updatedReplicasStr == "" {
		return 0, nil
	}
	return strconv.Atoi(updatedReplicasStr)
}

// GetIsReady returns true if the ControlPlaneMachineSet instances are ready
func (cpms ControlPlaneMachineSet) GetIsReady() bool {
	configuredReplicas, err := cpms.GetReplicas()
	if err != nil {
		logger.Infof("Cannot get configured replicas. Error: %s", err)
		return false
	}

	readyReplicas, err := cpms.GetReadyReplicas()
	if err != nil {
		logger.Infof("Cannot get ready replicas. Error: %s", err)
		return false
	}

	updatedReplicas, err := cpms.GetUpdatedReplicas()
	if err != nil {
		logger.Infof("Cannot get updated replicas. Error: %s", err)
		return false
	}

	logger.Infof("ConfiguredReplicas: %d, ReadyReplicas: %d, UpdatedReplicas: %d", configuredReplicas, readyReplicas, updatedReplicas)

	return configuredReplicas == readyReplicas && readyReplicas == updatedReplicas
}

// WaitUntilReady waits until the ControlPlaneMachineSet reports a Ready status
func (cpms ControlPlaneMachineSet) WaitUntilReady(duration string) error {
	pDuration, err := time.ParseDuration(duration)
	if err != nil {
		logger.Errorf("Error parsing duration %s. Error: %s", duration, err)
		return err
	}

	immediate := false
	pollerr := wait.PollUntilContextTimeout(context.TODO(), 20*time.Second, pDuration, immediate, func(_ context.Context) (bool, error) {
		return cpms.GetIsReady(), nil
	})

	return pollerr
}

// GetCoreOsBootImage returns the configured coreOsBootImage in this ControlPlaneMachineSet
func (cpms ControlPlaneMachineSet) GetCoreOsBootImage() (string, error) {
	// the coreOs boot image is stored differently in the ControlPlaneMachineSet spec depending on the platform
	coreOsBootImagePath := ""
	switch p := exutil.CheckPlatform(cpms.oc); p {
	case AWSPlatform:
		coreOsBootImagePath = `{.spec.template.machines_v1beta1_machine_openshift_io.spec.providerSpec.value.ami.id}`
	case GCPPlatform:
		// For GCP, dynamically find the boot disk index
		bootDiskIndex, err := GCPGetControlPlaneMachinesetBootDiskIndex(cpms)
		if err != nil {
			return "", err
		}
		coreOsBootImagePath = fmt.Sprintf(`{.spec.template.machines_v1beta1_machine_openshift_io.spec.providerSpec.value.disks[%d].image}`, bootDiskIndex)
	case VspherePlatform:
		coreOsBootImagePath = `{.spec.template.machines_v1beta1_machine_openshift_io.spec.providerSpec.value.template}`
	case AzurePlatform:
		coreOsBootImagePath = `{.spec.template.machines_v1beta1_machine_openshift_io.spec.providerSpec.value.image}`
	default:
		e2e.Failf("ControlPlaneMachineSet.GetCoreOsBootImage method is only supported for GCP, Vsphere, Azure and AWS infrastructure")
	}

	return cpms.Get(coreOsBootImagePath)
}

// GetCoreOsBootImageOrFail returns the configured coreOsBootImage in this ControlPlaneMachineSet and fails the test case if any error happened
func (cpms ControlPlaneMachineSet) GetCoreOsBootImageOrFail() string {
	img, err := cpms.GetCoreOsBootImage()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the coreos boot image value in %s", cpms)
	return img
}

// SetCoreOsBootImage sets the value of the configured coreos boot image
func (cpms ControlPlaneMachineSet) SetCoreOsBootImage(coreosBootImage string) error {
	// the coreOs boot image is stored differently in the ControlPlaneMachineSet spec depending on the platform
	patchCoreOsBootImagePath := ""
	switch p := exutil.CheckPlatform(cpms.oc); p {
	case AWSPlatform:
		patchCoreOsBootImagePath = "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/ami/id"
	case GCPPlatform:
		// For GCP, dynamically find the boot disk index
		bootDiskIndex, err := GCPGetControlPlaneMachinesetBootDiskIndex(cpms)
		if err != nil {
			return err
		}
		patchCoreOsBootImagePath = fmt.Sprintf("/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/disks/%d/image", bootDiskIndex)
	case VspherePlatform:
		patchCoreOsBootImagePath = "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/template"
	case AzurePlatform:
		patchCoreOsBootImagePath = "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/image"
	default:
		e2e.Failf("ControlPlaneMachineSet.SetCoreOsBootImage method is only supported for GCP, Vsphere, Azure and AWS platforms")
	}

	return cpms.Patch("json", fmt.Sprintf(`[{"op": "add", "path": "%s", "value": %s}]`,
		patchCoreOsBootImagePath, QuoteIfNotJSON(coreosBootImage)))
}

// GetArchitecture returns the architecture for this ControlPlaneMachineSet
func (cpms ControlPlaneMachineSet) GetArchitecture() (architecture.Architecture, error) {
	platform := exutil.CheckPlatform(cpms.GetOC())
	if platform == VspherePlatform {
		// In vsphere only the AMD64 architecture is supported
		return architecture.AMD64, nil
	}

	// Get machines created by the ControlPlaneMachineSet
	machines, err := cpms.GetMachines()
	if err != nil {
		return architecture.UNKNOWN, err
	}

	if len(machines) == 0 {
		return architecture.UNKNOWN, fmt.Errorf("ControlPlaneMachineSet %s has no machines, so we cannot get the architecture from any existing machine", cpms.GetName())
	}

	// Get the node associated with the first machine
	node, err := machines[0].GetNode()
	if err != nil {
		return architecture.UNKNOWN, err
	}

	return node.GetArchitecture()
}

// GetArchitectureOrFail returns the architecture and fails the test if any error happens
func (cpms ControlPlaneMachineSet) GetArchitectureOrFail() architecture.Architecture {
	arch, err := cpms.GetArchitecture()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the architecture in %s", cpms)
	return arch
}

// GetUserDataSecretName returns the name of the secret used for user-data
func (cpms ControlPlaneMachineSet) GetUserDataSecretName() (string, error) {
	return cpms.Get(`{.spec.template.machines_v1beta1_machine_openshift_io.spec.providerSpec.value.userDataSecret.name}`)
}

// GetUserDataSecret returns the secret used for user-data
func (cpms ControlPlaneMachineSet) GetUserDataSecret() (*Secret, error) {
	secretName, err := cpms.GetUserDataSecretName()
	if err != nil {
		return nil, err
	}
	return NewSecret(cpms.GetOC(), MachineAPINamespace, secretName), nil
}

// SetUserDataSecret configures the ControlPlaneMachineSet to use the provided user-data secret in the machine-api namespace
func (cpms ControlPlaneMachineSet) SetUserDataSecret(userDataSecretName string) error {
	return cpms.Patch("json", `[{ "op": "replace", "path": "/spec/template/machines_v1beta1_machine_openshift_io/spec/providerSpec/value/userDataSecret/name", "value": "`+userDataSecretName+`" }]`)
}

// GetMachines returns a slice with the machines created for this ControlPlaneMachineSet
func (cpms ControlPlaneMachineSet) GetMachines() ([]*Machine, error) {
	ml := NewMachineList(cpms.oc, cpms.GetNamespace())
	ml.ByLabel("machine.openshift.io/cluster-api-machine-role=master")
	ml.ByLabel("machine.openshift.io/cluster-api-machine-type=master")
	ml.SortByTimestamp()
	return ml.GetAll()
}

// GetMachinesOrFail get machines from ControlPlaneMachineSet or fail the test if any error occurred
func (cpms ControlPlaneMachineSet) GetMachinesOrFail() []*Machine {
	ml, err := cpms.GetMachines()
	o.Expect(err).NotTo(o.HaveOccurred(), "Get machines of ControlPlaneMachineSet %s failed", cpms.GetName())
	return ml
}

// GetNodes returns a slice with all nodes that have been created for this ControlPlaneMachineSet
func (cpms ControlPlaneMachineSet) GetNodes() ([]*Node, error) {
	machines, mErr := cpms.GetMachines()
	if mErr != nil {
		return nil, mErr
	}

	nodes := []*Node{}
	for _, m := range machines {
		n, nErr := m.GetNode()
		if nErr != nil {
			return nil, nErr
		}

		nodes = append(nodes, n)
	}
	return nodes, nil
}

// GetNodesOrFail returns a slice with all nodes that have been created for this ControlPlaneMachineSet and fails the test if any error happens
func (cpms ControlPlaneMachineSet) GetNodesOrFail() []*Node {
	nodes, err := cpms.GetNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the nodes that belong to %s", cpms)
	return nodes
}

// GetAll returns a []*ControlPlaneMachineSet list with all existing ControlPlaneMachineSets
func (cpmsl *ControlPlaneMachineSetList) GetAll() ([]*ControlPlaneMachineSet, error) {
	allCPMSResources, err := cpmsl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allCPMS := make([]*ControlPlaneMachineSet, 0, len(allCPMSResources))

	for _, cpmsRes := range allCPMSResources {
		allCPMS = append(allCPMS, NewControlPlaneMachineSet(cpmsl.oc, cpmsRes.GetNamespace(), cpmsRes.GetName()))
	}

	return allCPMS, nil
}

// GetAllOrFail returns a []*ControlPlaneMachineSet list with all existing ControlPlaneMachineSets and fail the test if it is not possible
func (cpmsl *ControlPlaneMachineSetList) GetAllOrFail() []*ControlPlaneMachineSet {
	allCpms, err := cpmsl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing ControlPlaneMachineSets")

	return allCpms
}

// DeleteOneMachineAndWaitForRecreation deletes one machine from the ControlPlaneMachineSet and waits for a new one to be created and ready
// This function validates that the CPMS correctly recreates deleted machines
func DeleteOneMachineAndWaitForRecreation(cpms *ControlPlaneMachineSet) {
	machines := cpms.GetMachinesOrFail()
	o.Expect(machines).To(o.HaveLen(3), "Expected 3 control plane machines")

	machineToDelete := machines[0]
	logger.Infof("Deleting machine %s", machineToDelete.GetName())
	o.Expect(machineToDelete.Delete("--wait=false")).To(o.Succeed(), "Error deleting machine %s", machineToDelete)

	// Wait for the machine to be deleted
	o.Eventually(machineToDelete.Exists, "30m", "30s").Should(o.BeFalse(),
		"Machine %s was not deleted", machineToDelete)
	logger.Infof("Machine %s deleted", machineToDelete.GetName())

	// Wait for the machines to be created and ready
	o.Eventually(func(gm o.Gomega) {
		expectedNumMachines := 3
		currentMachines := cpms.GetMachinesOrFail()
		gm.Expect(currentMachines).To(o.HaveLen(expectedNumMachines),
			"Wrong number of controlplane machines")

		// Check all machines are ready
		for _, m := range currentMachines {
			gm.Expect(m.IsRunning()).To(o.BeTrue(),
				"Machine %s is not running yet", m.GetName())
		}
	}, "5m", "30s").Should(o.Succeed(), "New machine was not created or is not ready")
	logger.Infof("New machine created and ready")

	// Wait for ControlPlaneMachineSet to be ready
	o.Eventually(cpms.GetIsReady, "5m", "30s").Should(o.BeTrue(),
		"ControlPlaneMachineSet %s is not ready after machine replacement", cpms.GetName())
}

// GCPGetControlPlaneMachinesetBootDiskIndex returns the index of the boot disk in the GCP disks array
// In GCP, the boot disk is identified by the "boot: true" field in the disk specification
func GCPGetControlPlaneMachinesetBootDiskIndex(cpms ControlPlaneMachineSet) (int, error) {
	// Get the disks array from the ControlPlaneMachineSet spec
	disksJSON, err := cpms.Get(`{.spec.template.machines_v1beta1_machine_openshift_io.spec.providerSpec.value.disks}`)
	if err != nil {
		return -1, fmt.Errorf("Failed to get disks array: %v", err)
	}

	// Parse the disks array and find the boot disk
	disks := gjson.Parse(disksJSON).Array()
	for i, disk := range disks {
		if disk.Get("boot").Bool() {
			logger.Infof("Found boot disk at index %d", i)
			return i, nil
		}
	}

	return -1, fmt.Errorf("No boot disk found in disks array (no disk with boot: true)")
}
