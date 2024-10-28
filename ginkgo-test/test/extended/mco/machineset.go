package mco

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	"github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/architecture"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"k8s.io/apimachinery/pkg/util/wait"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

const (
	// MachineAPINamespace is the namespace where openshift machinesets are created
	MachineAPINamespace = "openshift-machine-api"
)

// MachineSet struct to handle MachineSet resources
type MachineSet struct {
	Resource
}

// MachineSetList struct to handle lists of MachineSet resources
type MachineSetList struct {
	ResourceList
}

// NewMachineSet constructs a new MachineSet struct
func NewMachineSet(oc *exutil.CLI, namespace, name string) *MachineSet {
	return &MachineSet{*NewNamespacedResource(oc, MachineSetFullName, namespace, name)}
}

// NewMachineSetList constructs a new MachineSetListist struct to handle all existing MachineSets
func NewMachineSetList(oc *exutil.CLI, namespace string) *MachineSetList {
	return &MachineSetList{*NewNamespacedResourceList(oc, MachineSetFullName, namespace)}
}

// String implements the Stringer interface
func (ms MachineSet) String() string {
	return ms.GetName()
}

// ScaleTo scales the MachineSet to the exact given value
func (ms MachineSet) ScaleTo(scale int) error {
	return ms.Patch("merge", fmt.Sprintf(`{"spec": {"replicas": %d}}`, scale))
}

// GetReplicaOfSpec return replica number of spec
func (ms MachineSet) GetReplicaOfSpec() string {
	return ms.GetOrFail(`{.spec.replicas}`)
}

// AddToScale scales the MachineSet adding the given value (positive or negative).
func (ms MachineSet) AddToScale(delta int) error {
	currentReplicas, err := strconv.Atoi(ms.GetOrFail(`{.spec.replicas}`))
	if err != nil {
		return err
	}

	return ms.ScaleTo(currentReplicas + delta)
}

// GetIsReady returns true the MachineSet instances are ready
func (ms MachineSet) GetIsReady() bool {
	configuredReplicasString, err := ms.Get(`{.spec.replicas}`)
	if err != nil {
		logger.Infof("Cannot get configured replicas. Err: %s", err)
		return false
	}

	configuredReplicas, err := strconv.Atoi(configuredReplicasString)
	if err != nil {
		logger.Infof("Could not parse configured replicas. Error: %s", err)
		return false
	}

	statusString, err := ms.Get(`{.status}`)
	if err != nil {
		logger.Infof("Cannot get status. Err: %s", err)
		return false
	}

	status := JSON(statusString)
	logger.Infof("status %s", status)
	replicasData, err := status.GetSafe("replicas")
	if err != nil {
		logger.Infof("Cannot get the replicas in the status. Err: %s", err)
		return false
	}

	readyReplicasData, err := status.GetSafe("readyReplicas")
	if err != nil {
		logger.Infof("Cannot get the readyReplicas in the status. Err: %s", err)
		return false
	}

	if !replicasData.Exists() {
		logger.Infof("Replicasdata does not exist")
		return false
	}
	replicas := replicasData.ToInt()
	if replicas == 0 {
		if replicas == configuredReplicas {
			// We cant check the ready status when there is 0 replica configured.
			// So if status.replicas == spec.replicas == 0 then we consider that it is ok
			logger.Infof("Zero replicas")
			return true
		}
		logger.Infof("Zero replicas. Status not updated already.")
		return false
	}
	if !readyReplicasData.Exists() {
		logger.Infof("ReadyReplicasdata does not exist")
		return false
	}
	readyReplicas := readyReplicasData.ToInt()

	logger.Infof("Replicas %d, readyReplicas %d", replicas, readyReplicas)

	replicasAreReady := replicas == readyReplicas
	replicasAreConfigured := replicas == configuredReplicas

	return replicasAreReady && replicasAreConfigured
}

// GetMachines returns a slice with the machines created for this MachineSet
func (ms MachineSet) GetMachines() ([]Machine, error) {
	ml := NewMachineList(ms.oc, ms.GetNamespace())
	ml.ByLabel("machine.openshift.io/cluster-api-machineset=" + ms.GetName())
	ml.SortByTimestamp()
	return ml.GetAll()
}

// GetMachinesOrFail get machines from machineset or fail the test if any error occurred
func (ms MachineSet) GetMachinesOrFail() []Machine {
	ml, err := ms.GetMachines()
	o.Expect(err).NotTo(o.HaveOccurred(), "Get machines of machineset %s failed", ms.GetName())
	return ml
}

// GetMachinesByPhase get machine by phase e.g. Running, Provisioning, Provisioned, Deleting etc.
func (ms MachineSet) GetMachinesByPhase(phase string) ([]Machine, error) {
	// add poller to check machine phase periodically.
	machines := []Machine{}
	pollerr := wait.PollUntilContextTimeout(context.TODO(), 3*time.Second, 20*time.Second, true, func(_ context.Context) (bool, error) {
		ml, err := ms.GetMachines()
		if err != nil {
			return false, err
		}
		for _, m := range ml {
			if m.GetPhase() == phase {
				machines = append(machines, m)
			}
		}
		return len(machines) > 0, nil
	})

	return machines, pollerr
}

// GetMachinesByPhaseOrFail call GetMachineByPhase or fail the test if any error occurred
func (ms MachineSet) GetMachinesByPhaseOrFail(phase string) []Machine {
	ml, err := ms.GetMachinesByPhase(phase)
	o.Expect(err).NotTo(o.HaveOccurred(), "Get machine by phase %s failed", phase)
	o.Expect(ml).ShouldNot(o.BeEmpty(), "No machine found by phase %s in machineset %s", phase, ms.GetName())
	return ml
}

// GetNodes returns a slice with all nodes that have been created for this MachineSet
func (ms MachineSet) GetNodes() ([]*Node, error) {
	machines, mErr := ms.GetMachines()
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

// GetNodesOrFail returns a slice with all nodes that have been created for this MachineSet and fails the test if any error happens
func (ms MachineSet) GetNodesOrFail() []*Node {
	nodes, err := ms.GetNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the nodes that belong to %s", ms)
	return nodes
}

// WaitUntilReady waits until the MachineSet reports a Ready status
func (ms MachineSet) WaitUntilReady(duration string) error {
	pDuration, err := time.ParseDuration(duration)
	if err != nil {
		logger.Errorf("Error parsing duration %s. Errot: %s", duration, err)
		return err
	}

	immediate := false
	pollerr := wait.PollUntilContextTimeout(context.TODO(), 20*time.Second, pDuration, immediate, func(_ context.Context) (bool, error) {
		return ms.GetIsReady(), nil
	})

	return pollerr
}

// Duplicate creates a new MachineSet by ducplicating the MachineSet information but using a new name, the new duplicated Machineset has 0 replicas
// If you need to further modify the new machineset, patch it, and scale it up
// For example, to duplicate a machineset and use a new secret in the duplicated machineset:
// newMs := ms.Duplicate("newname")
// err = newMs.Patch("json", `[{ "op": "replace", "path": "/spec/template/spec/providerSpec/value/userDataSecret/name", "value": "newSecretName" }]`)
// newMs.ScaleTo(1)
func (ms MachineSet) Duplicate(newName string) (*MachineSet, error) {

	res, err := CloneResource(&ms, newName, ms.GetNamespace(),
		// Extra modifications to
		// 1. Create the resource with 0 replicas
		// 2. modify the selector matchLabels
		// 3. modify the selector template metadata labels
		func(resString string) (string, error) {
			newResString, err := sjson.Set(resString, "spec.replicas", 0)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, `spec.selector.matchLabels.machine\.openshift\.io/cluster-api-machineset`, newName)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, `spec.template.metadata.labels.machine\.openshift\.io/cluster-api-machineset`, newName)
			if err != nil {
				return "", err
			}

			return newResString, nil
		},
	)

	if err != nil {
		return nil, err
	}

	logger.Infof("A new machineset %s has been created by cloning %s", res.GetName(), ms.GetName())
	return NewMachineSet(ms.oc, res.GetNamespace(), res.GetName()), nil
}

// SetCoreOsBootImage sets the value of the configured coreos boot image
func (ms MachineSet) SetCoreOsBootImage(coreosBootImage string) error {
	// the coreOs boot image is stored differently in the machineset spec depending on the platform
	// currently we only support testing the coresOs boot image in GCP platform.
	patchCoreOsBootImagePath := GetCoreOSBootImagePath(exutil.CheckPlatform(ms.oc))

	return ms.Patch("json", fmt.Sprintf(`[{"op": "add", "path": "%s", "value": "%s"}]`,
		patchCoreOsBootImagePath, coreosBootImage))
}

// GetArchitecture returns the architecture configured for this Machineset
func (ms MachineSet) GetArchitecture() (*architecture.Architecture, error) {
	labeledArch, err := ms.Get(`{.metadata.annotations.capacity\.cluster-autoscaler\.kubernetes\.io/labels}`)
	if err != nil {
		return nil, err
	}

	expectedFormat := "kubernetes.io/arch="
	if !strings.Contains(labeledArch, "kubernetes.io/arch=") {
		return nil, fmt.Errorf("The annotated architecture is not in the expected format. Annotation: %s. Expected format: %s${ARCH}",
			labeledArch, expectedFormat)
	}

	return PtrTo(architecture.FromString(strings.Split(labeledArch, "kubernetes.io/arch=")[1])), nil
}

func (ms MachineSet) GetArchitectureOrFail() *architecture.Architecture {
	arch, err := ms.GetArchitecture()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the annotated architecture in %s", ms)
	return arch
}

func (ms MachineSet) SetArchitecture(arch string) error {
	return ms.Patch("json",
		fmt.Sprintf(`[{"op": "add", "path": "/metadata/annotations/capacity.cluster-autoscaler.kubernetes.io~1labels", "value": "kubernetes.io/arch=%s"}]`,
			arch))
}

// GetUserDataSecret returns the secret used for user-data
func (ms MachineSet) GetUserDataSecret() (string, error) {
	return ms.Get(`{.spec.template.spec.providerSpec.value.userDataSecret.name}`)
}

// GetCoreOsBootImage returns the configured coreOsBootImage in this machineset
func (ms MachineSet) GetCoreOsBootImage() (string, error) {
	// the coreOs boot image is stored differently in the machineset spec depending on the platform
	// currently we only support testing the coresOs boot image in GCP platform.
	coreOsBootImagePath := ""
	switch p := exutil.CheckPlatform(ms.oc); p {
	case "aws":
		coreOsBootImagePath = `{.spec.template.spec.providerSpec.value.ami.id}`
	case "gcp":
		coreOsBootImagePath = `{.spec.template.spec.providerSpec.value.disks[0].image}`
	default:
		e2e.Failf("Machineset.GetCoreOsBootImage method is only supported for GCP and AWS infrastructure")
	}

	return ms.Get(coreOsBootImagePath)
}

// GetCoreOsBootImage returns the configured coreOsBootImage in this machineset and fails the test case if any error happened
func (ms MachineSet) GetCoreOsBootImageOrFail() string {
	img, err := ms.GetCoreOsBootImage()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the coreos boot image value in %s", ms)
	return img
}

// GetAll returns a []MachineSet list with all existing machinesets
func (msl *MachineSetList) GetAll() ([]MachineSet, error) {
	allMSResources, err := msl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMS := make([]MachineSet, 0, len(allMSResources))

	for _, msRes := range allMSResources {
		allMS = append(allMS, *NewMachineSet(msl.oc, msRes.GetNamespace(), msRes.GetName()))
	}

	return allMS, nil
}

// GetAllOrFail returns a []Machineset list with all existing machinesets and fail the test if it is not possible
func (msl *MachineSetList) GetAllOrFail() []MachineSet {
	allMs, err := msl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing MachineSets")

	return allMs
}

// duplicateMachinesetSecret duplicates a userData secret and prepares the new duplicated secret to use the given ignition version
func duplicateMachinesetSecret(oc *exutil.CLI, secretName, newName, newIgnitionVersion string) (*Secret, error) {
	var (
		currentSecret = NewSecret(oc, MachineAPINamespace, secretName)
		err           error
	)
	userData, udErr := currentSecret.GetDataValue("userData")
	if udErr != nil {
		logger.Errorf("Error getting userData info from secret %s -n %s.\n%s", secretName, MachineAPINamespace, udErr)
		return nil, udErr
	}

	disableTemplating, dtErr := currentSecret.GetDataValue("disableTemplating")
	if dtErr != nil {
		logger.Errorf("Error getting disableTemplating info from secret %s -n %s.\n%s", secretName, MachineAPINamespace, dtErr)
		return nil, dtErr
	}

	currentIgnitionVersionResult := gjson.Get(userData, "ignition.version")
	if !currentIgnitionVersionResult.Exists() || currentIgnitionVersionResult.String() == "" {
		logger.Debugf("Could not get ignition version from ignition userData: %s", userData)
		return nil, fmt.Errorf("Could not get ignition version from ignition userData. Enable debug GINKGO_TEST_ENABLE_DEBUG_LOG to get more info")
	}
	currentIgnitionVersion := currentIgnitionVersionResult.String()

	if CompareVersions(currentIgnitionVersion, "==", newIgnitionVersion) {
		logger.Infof("Current ignition version %s is the same as the new ignition version %s. No need to manipulate the userData info",
			currentIgnitionVersion, newIgnitionVersion)
	} else {
		if CompareVersions(newIgnitionVersion, "<", "3.0.0") {
			logger.Infof("New ignition version is %s, we need to adapt the userData ignition config to 2.0 config", newIgnitionVersion)
			logger.Infof("Replace the 'merge' action with the 'append' action")
			merge := gjson.Get(userData, "ignition.config.merge")
			if !merge.Exists() {
				logger.Debugf("Could not find the 'merge' information in the userData ignition config: %s", userData)
				return nil, fmt.Errorf("Could not find the 'merge' information in the userData ignition config. Enable debug GINKGO_TEST_ENABLE_DEBUG_LOG to get more info")
			}
			userData, err = sjson.SetRaw(userData, "ignition.config.append", merge.String())
			if err != nil {
				return nil, err
			}

			userData, err = sjson.Delete(userData, "ignition.config.merge")
			if err != nil {
				return nil, err
			}
		}
		logger.Infof("Replace ignition version '%s' with  version '%s'", currentIgnitionVersion, newIgnitionVersion)
		userData, err = sjson.Set(userData, "ignition.version", newIgnitionVersion)
		if err != nil {
			return nil, err
		}

	}

	logger.Debugf("New userData info:\n%s", userData)

	_, err = oc.AsAdmin().WithoutNamespace().Run("create").Args("secret", "generic", newName, "-n", MachineAPINamespace,
		"--from-literal", fmt.Sprintf("userData=%s", userData),
		"--from-literal", fmt.Sprintf("disableTemplating=%s", disableTemplating)).Output()

	return NewSecret(oc.AsAdmin(), MachineAPINamespace, newName), err
}

// getUserDataIgnitionVersionFromOCPVersion returns that right ignition version for a given base image version
func getUserDataIgnitionVersionFromOCPVersion(baseImageVersion string) string {
	/* UserData ignition version is defined in https://github.com/openshift/installer/blob/release-4.16/pkg/asset/ignition/machine/node.go#L52
	   4.16: 3.2.0
	   4.15: 3.2.0
	   4.14: 3.2.0
	   4.13: 3.2.0   // change to rhel9
	   4.12: 3.2.0
	   4.11: 3.2.0   // support for arm64 added
	   4.10: 3.1.0
	   4.9: 3.1.0
	   4.8: 3.1.0
	   4.7: 3.1.0
	   4.6: 3.1.0
	   4.5: 2.2.0
	   4.4: 2.2.0
	   4.3: 2.2.0    // support for fips
	   4.2: 2.2.0
	   4.1: 2.2.0
	*/

	if CompareVersions(baseImageVersion, "<", "4.6") {
		return "2.2.0"
	}
	if CompareVersions(baseImageVersion, "<", "4.10") {
		return "3.1.0"
	}
	return "3.2.0"
}

func GetCoreOSBootImagePath(platform string) string {
	patchCoreOsBootImagePath := ""
	switch platform {
	case AWSPlatform:
		patchCoreOsBootImagePath = "/spec/template/spec/providerSpec/value/ami/id"
	case GCPPlatform:
		patchCoreOsBootImagePath = "/spec/template/spec/providerSpec/value/disks/0/image"
	case VspherePlatform:
		patchCoreOsBootImagePath = "/spec/template/spec/providerSpec/value/template"
	default:
		e2e.Failf("Machineset.GetCoreOsBootImage method is only supported for GCP and AWS platforms")
	}

	return patchCoreOsBootImagePath
}

func GetArchitectureFromMachineset(ms *MachineSet, platform string) (architecture.Architecture, error) {
	logger.Infof("Getting architecture for machineset %s", ms.GetName())

	arch, err := ms.GetArchitecture()
	if err == nil {
		return *arch, nil
	}

	// In vsphere the machinesets are not annotated, so wee need to get the architecture from the Nodes if they exist
	if platform != VspherePlatform {
		return architecture.UNKNOWN, err
	}

	logger.Infof("In vsphere we need to get the architecture from the existing nodes created by the machineset %s", ms.GetName())
	nodes, err := ms.GetNodes()
	if err != nil {
		return architecture.UNKNOWN, err
	}

	if len(nodes) == 0 {
		return architecture.UNKNOWN, fmt.Errorf("Machineset %s has no replicas, so we cannot get the architecture from any existing node", ms.GetName())
	}

	narch, err := nodes[0].GetArchitecture()
	if err != nil {
		return architecture.UNKNOWN, err
	}

	return narch, nil
}
