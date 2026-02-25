package extended

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"k8s.io/apimachinery/pkg/util/wait"
	e2e "k8s.io/kubernetes/test/e2e/framework"
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

// NewMachineSetList constructs a new MachineSetList struct
func NewMachineSetList(oc *exutil.CLI, namespace string) *MachineSetList {
	return &MachineSetList{*NewNamespacedResourceList(oc, MachineSetFullName, namespace)}
}

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

// GetIsReady returns true if the MachineSet instances are ready
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
func (ms MachineSet) GetMachines() ([]*Machine, error) {
	ml := NewMachineList(ms.oc, ms.GetNamespace())
	ml.ByLabel("machine.openshift.io/cluster-api-machineset=" + ms.GetName())
	ml.SortByTimestamp()
	return ml.GetAll()
}

// GetMachinesByPhase get machine by phase e.g. Running, Provisioning, Provisioned, Deleting etc.
func (ms MachineSet) GetMachinesByPhase(phase string) ([]*Machine, error) {
	// add poller to check machine phase periodically.
	machines := []*Machine{}
	pollerr := wait.PollUntilContextTimeout(context.TODO(), 3*time.Second, 20*time.Second, true, func(_ context.Context) (bool, error) {
		ml, err := ms.GetMachines()
		if err != nil {
			return false, err
		}
		for _, m := range ml {
			mPhase, err := m.GetPhase()
			if err != nil {
				return false, err
			}
			if mPhase == phase {
				machines = append(machines, m)
			}
		}
		return len(machines) > 0, nil
	})

	return machines, pollerr
}

// GetMachinesByPhaseOrFail call GetMachineByPhase or fail the test if any error occurred
func (ms MachineSet) GetMachinesByPhaseOrFail(phase string) []*Machine {
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
	patchCoreOsBootImagePath, err := ms.GetCoreOSBootImagePath(exutil.CheckPlatform(ms.oc))
	if err != nil {
		return err
	}

	return ms.Patch("json", fmt.Sprintf(`[{"op": "add", "path": "%s", "value": %s}]`,
		patchCoreOsBootImagePath, QuoteIfNotJSON(coreosBootImage)))
}

// GetArchitecture returns the architecture configured for this Machineset
func (ms MachineSet) GetArchitecture() (architecture.Architecture, error) {
	labeledArch, err := ms.Get(`{.metadata.annotations.capacity\.cluster-autoscaler\.kubernetes\.io/labels}`)
	if err != nil {
		return architecture.UNKNOWN, err
	}

	if !strings.Contains(labeledArch, "kubernetes.io/arch=") {
		platform := exutil.CheckPlatform(ms.GetOC())
		if platform == VspherePlatform {
			// In vsphere only the AMD64 architecture is supported
			return architecture.AMD64, nil
		}

		logger.Infof("No arch annotation in the machineset. Not Vsphere. We need to get the architecture from the existing nodes created by the machineset %s", ms.GetName())
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

	return architecture.FromString(strings.Split(labeledArch, "kubernetes.io/arch=")[1]), nil
}

// GetArchitectureOrFail returns the architecture configured for this Machineset and fails the test if any error happens
func (ms MachineSet) GetArchitectureOrFail() architecture.Architecture {
	arch, err := ms.GetArchitecture()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the annotated architecture in %s", ms)
	return arch
}

// SetArchitecture sets the architecture annotation for this MachineSet
func (ms MachineSet) SetArchitecture(arch string) error {
	return ms.Patch("json",
		fmt.Sprintf(`[{"op": "add", "path": "/metadata/annotations/capacity.cluster-autoscaler.kubernetes.io~1labels", "value": "kubernetes.io/arch=%s"}]`,
			arch))
}

// GetUserDataSecretName returns the name of the user-data secret
func (ms MachineSet) GetUserDataSecretName() (string, error) {
	return ms.Get(`{.spec.template.spec.providerSpec.value.userDataSecret.name}`)
}

// GetUserDataSecret returns the secret used for user-data
func (ms MachineSet) GetUserDataSecret() (*Secret, error) {
	secretName, err := ms.GetUserDataSecretName()
	if err != nil {
		return nil, err
	}
	return NewSecret(ms.GetOC(), MachineAPINamespace, secretName), nil
}

// SetUserDataSecret configures the machineset to use the provided user-data secret in the machine-config-api namespace
func (ms MachineSet) SetUserDataSecret(userDataSecretName string) error {
	return ms.Patch("json", `[{ "op": "replace", "path": "/spec/template/spec/providerSpec/value/userDataSecret/name", "value": "`+userDataSecretName+`" }]`)
}

// GetCoreOsBootImage returns the configured coreOsBootImage in this machineset
func (ms MachineSet) GetCoreOsBootImage() (string, error) {
	// the coreOs boot image is stored differently in the machineset spec depending on the platform
	// currently we only support testing the coresOs boot image in GCP platform.
	coreOsBootImagePath := ""
	switch p := exutil.CheckPlatform(ms.oc); p {
	case AWSPlatform:
		coreOsBootImagePath = `{.spec.template.spec.providerSpec.value.ami.id}`
	case GCPPlatform:
		// For GCP, dynamically find the boot disk index
		bootDiskIndex, err := GCPGetMachineSetBootDiskIndex(ms)
		if err != nil {
			return "", err
		}
		coreOsBootImagePath = fmt.Sprintf(`{.spec.template.spec.providerSpec.value.disks[%d].image}`, bootDiskIndex)
	case VspherePlatform:
		coreOsBootImagePath = `{.spec.template.spec.providerSpec.value.template}`
	case AzurePlatform:
		coreOsBootImagePath = `{.spec.template.spec.providerSpec.value.image}`
	default:
		e2e.Failf("Machineset.GetCoreOsBootImage method is only supported for GCP and AWS infrastructure")
	}

	return ms.Get(coreOsBootImagePath)
}

// GetCoreOsBootImageOrFail returns the configured coreOsBootImage in this machineset and fails the test case if any error happened
func (ms MachineSet) GetCoreOsBootImageOrFail() string {
	img, err := ms.GetCoreOsBootImage()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the coreos boot image value in %s", ms)
	return img
}

// GetAll returns a []*MachineSet list with all existing machinesets
func (msl *MachineSetList) GetAll() ([]*MachineSet, error) {
	allMSResources, err := msl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMS := make([]*MachineSet, 0, len(allMSResources))

	for _, msRes := range allMSResources {
		allMS = append(allMS, NewMachineSet(msl.oc, msRes.GetNamespace(), msRes.GetName()))
	}

	return allMS, nil
}

// GetAllOrFail returns a []*Machineset list with all existing machinesets and fail the test if it is not possible
func (msl *MachineSetList) GetAllOrFail() []*MachineSet {
	allMs, err := msl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing MachineSets")

	return allMs
}

// GetReplicas returns a []*Machineset list with all existing machinesets with the replica value matching the condition
func (msl *MachineSetList) GetReplicas(comparison string, replicas int) ([]*MachineSet, error) {
	var (
		allowedComparisson = []string{"<", ">", "==", "!="}
		validComparisson   = false
	)

	for _, ac := range allowedComparisson {
		if comparison == ac {
			validComparisson = true
			break
		}
	}

	if !validComparisson {
		return nil, fmt.Errorf("The provided comparison %s is not in the allowed comparisons list %s",
			comparison, allowedComparisson)
	}

	filter := fmt.Sprintf(`?(@.spec.replicas%s%d0)`, comparison, replicas)
	msl.SetItemsFilter(filter)

	return msl.GetAll()
}

// WaitForRunningMachines waits for the specified number of running machines to be created from this MachineSet
// Returns the running machines on success
func (ms MachineSet) WaitForRunningMachines(expectedReplicas int, timeout, pollInterval time.Duration) []Machine {
	var runningMachines []Machine

	o.Eventually(func() bool {
		// Get all machines from the machineset
		machines, err := ms.GetMachines()
		if err != nil {
			logger.Infof("Failed to get machines for machineset %s: %v", ms.GetName(), err)
			return false
		}

		if len(machines) == 0 {
			logger.Infof("No machines found yet for machineset %s", ms.GetName())
			return false
		}

		// Check how many machines are running
		runningMachines = []Machine{}
		for _, machine := range machines {
			isRunning, err := machine.IsRunning()
			if err != nil {
				logger.Infof("Failed to get phase for machine %s: %v", machine.GetName(), err)
				continue
			}
			if isRunning {
				runningMachines = append(runningMachines, *machine)
			}
		}

		if len(runningMachines) >= expectedReplicas {
			logger.Infof("MachineSet %s has %d/%d running machines", ms.GetName(), len(runningMachines), expectedReplicas)
			return true
		}

		logger.Infof("MachineSet %s has %d/%d running machines, waiting...", ms.GetName(), len(runningMachines), expectedReplicas)
		return false
	}, timeout, pollInterval).Should(o.BeTrue(), "Timed out waiting for %d running machines from MachineSet %s", expectedReplicas, ms.GetName())

	return runningMachines
}

// duplicateMachinesetSecret duplicates a userData secret and uses the provided functions to modify the userData and the disableTemplating data if they are not nil
//
//nolint:unparam // modifyDisableTemplating may be nil, kept for flexibility
func duplicateMachinesetSecret(oc *exutil.CLI, secretName, newName string,
	modifyUserData func(userData string) (string, error), modifyDisableTemplating func(disableTemplating string) (string, error)) (*Secret, error) {
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

	if modifyUserData != nil {
		userData, err = modifyUserData(userData)
		if err != nil {
			logger.Errorf("Error modifying the userData content with the provided modification function")
			return nil, err
		}
	}

	if modifyDisableTemplating != nil {
		disableTemplating, err = modifyDisableTemplating(disableTemplating)
		if err != nil {
			logger.Errorf("Error modifying the disableTemplating content with the provided modification function")
			return nil, err
		}
	}

	logger.Debugf("New userData info:\n%s", userData)
	oc.NotShowInfo()
	defer oc.SetShowInfo()

	_, err = oc.AsAdmin().WithoutNamespace().Run("create").Args("secret", "generic", newName, "-n", MachineAPINamespace,
		"--from-literal", fmt.Sprintf("userData=%s", userData),
		"--from-literal", fmt.Sprintf("disableTemplating=%s", disableTemplating)).Output()

	return NewSecret(oc.AsAdmin(), MachineAPINamespace, newName), err
}

// convertUserDataToNewVersion converts the provided userData ignition config into the provided version format
//
//nolint:unparam // newIgnitionVersion is always "2.2.0", but kept for flexibility
func convertUserDataToNewVersion(userData, newIgnitionVersion string) (string, error) {
	var err error

	currentIgnitionVersionResult := gjson.Get(userData, "ignition.version")
	if !currentIgnitionVersionResult.Exists() || currentIgnitionVersionResult.String() == "" {
		logger.Debugf("Could not get ignition version from ignition userData: %s", userData)
		return "", fmt.Errorf("Could not get ignition version from ignition userData. Enable debug GINKGO_TEST_ENABLE_DEBUG_LOG to get more info")
	}
	currentIgnitionVersion := currentIgnitionVersionResult.String()

	if CompareVersions(currentIgnitionVersion, "=", newIgnitionVersion) {
		logger.Infof("Current ignition version %s is the same as the new ignition version %s. No need to manipulate the userData info",
			currentIgnitionVersion, newIgnitionVersion)
	} else {
		if CompareVersions(newIgnitionVersion, "<", "3.0.0") {
			logger.Infof("New ignition version is %s, we need to adapt the userData ignition config to 2.0 config", newIgnitionVersion)
			userData, err = ConvertUserDataIgnition3ToIgnition2(userData)
			if err != nil {
				return "", err
			}
		}
		logger.Infof("Replace ignition version '%s' with  version '%s'", currentIgnitionVersion, newIgnitionVersion)
		userData, err = sjson.Set(userData, "ignition.version", newIgnitionVersion)
		if err != nil {
			return "", err
		}
	}

	return userData, nil
}

// ConvertUserDataIgnition3ToIgnition2 transforms an ignitionV3 userdata configuration into an ignitionV2 userdata configuration
// IMPORTANT: If the ignition config includes storage or systemd they will be deleted. Don't expect this configuration to actually work
//
//	The resulting 2.2.0 ignition config is only for testing purposes. If it is needed to actually transform the ignition version to
//	2.2.0 then this function needs to be modified.
func ConvertUserDataIgnition3ToIgnition2(ignition3 string) (string, error) {
	var (
		ignition2 = ignition3
		err       error
	)
	logger.Infof("Replace the 'merge' action with the 'append' action")
	merge := gjson.Get(ignition3, "ignition.config.merge")
	if !merge.Exists() {
		logger.Debugf("Could not find the 'merge' information in the ignition3 ignition config: %s", ignition3)
		return "", fmt.Errorf("Could not find the 'merge' information in the ignition3 ignition config. Enable debug GINKGO_TEST_ENABLE_DEBUG_LOG to get more info")
	}
	ignition2, err = sjson.SetRaw(ignition2, "ignition.config.append", merge.String())
	if err != nil {
		return "", err
	}

	logger.Infof("Delete ignition.config.merge field")
	ignition2, err = sjson.Delete(ignition2, "ignition.config.merge")
	if err != nil {
		return "", err
	}

	logger.Infof("Delete storage field to create stub ignition config")
	ignition2, err = sjson.Delete(ignition2, "storage")
	if err != nil {
		return "", err
	}

	logger.Infof("Delete systemd field to create stub ignition config")
	ignition2, err = sjson.Delete(ignition2, "systemd")
	if err != nil {
		return "", err
	}

	return ignition2, nil
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

// GetCoreOSBootImagePath returns the JSON patch path for the boot image in this machineset
func (ms MachineSet) GetCoreOSBootImagePath(platform string) (string, error) {
	patchCoreOsBootImagePath := ""
	switch platform {
	case AWSPlatform:
		patchCoreOsBootImagePath = "/spec/template/spec/providerSpec/value/ami/id"
	case GCPPlatform:
		// For GCP, dynamically find the boot disk index
		bootDiskIndex, err := GCPGetMachineSetBootDiskIndex(ms)
		if err != nil {
			return "", err
		}
		patchCoreOsBootImagePath = fmt.Sprintf("/spec/template/spec/providerSpec/value/disks/%d/image", bootDiskIndex)
	case VspherePlatform:
		patchCoreOsBootImagePath = "/spec/template/spec/providerSpec/value/template"
	case AzurePlatform:
		patchCoreOsBootImagePath = "/spec/template/spec/providerSpec/value/image"
	default:
		return "", fmt.Errorf("Machineset.GetCoreOSBootImagePath method is only supported for GCP, Vsphere, Azure and AWS platforms")
	}

	return patchCoreOsBootImagePath, nil
}

// GCPGetMachineSetBootDiskIndex returns the index of the boot disk in the GCP disks array
// In GCP, the boot disk is identified by the "boot: true" field in the disk specification
func GCPGetMachineSetBootDiskIndex(ms MachineSet) (int, error) {
	// Get the disks array from the MachineSet spec
	disksJSON, err := ms.Get(`{.spec.template.spec.providerSpec.value.disks}`)
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

// AllNodesUpdated returns true if all nodes in this machineset are updated
func (ms MachineSet) AllNodesUpdated() (bool, error) {
	nodes, err := ms.GetNodes()
	if err != nil {
		return false, err
	}

	for _, node := range nodes {
		updated, err := node.IsUpdated()
		if err != nil {
			return false, err
		}
		if !updated {
			return false, nil
		}
	}

	return true, nil
}

// GetScalableMachineSet return a machineset that can be scaled to add new nodes to the cluster. We select a machineset that already has node to make sure that it is safe to scale it up
func GetScalableMachineSet(oc *exutil.CLI) (*MachineSet, error) {
	machinesets, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetReplicas(">", 0)
	if err != nil {
		return nil, err
	}

	if len(machinesets) == 0 {
		return nil, fmt.Errorf("There is no machineset that can be used to scale nodes safely")
	}

	return machinesets[0], nil
}
