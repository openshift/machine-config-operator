package extended

import (
	"fmt"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// MachineAPIType represents the type of machine management API
type MachineAPIType int

const (
	// MAPI represents the Machine API (machine.openshift.io)
	MAPI MachineAPIType = iota
	// CAPI represents the Cluster API (cluster.x-k8s.io)
	CAPI
)

// ManagedMachineResource defines the operations that boot image tests perform on a machineset resource.
// Both MAPI MachineSets and CAPI MachineSets/MachineDeployments can implement this interface,
// allowing the same test logic to run against different machine management APIs.
type ManagedMachineResource interface {
	BootImageResource
	GetName() string
	GetNamespace() string
	GetSpecOrFail() string
	SetSpec(spec string) error
	Delete(extraParams ...string) error
	Patch(patchType string, patch string, extraParams ...string) error
	Exists() bool
	AddLabel(label, value string) error
	PrettyString() string
	Duplicate(newName string) (ManagedMachineResource, error)
	DuplicateWithBootImage(newName, bootImage string) (ManagedMachineResource, error)
	SetCoreOsBootImage(coreosBootImage string) error
	GetCoreOsBootImageOrFail() string
	GetCoreOSBootImagePath(platform string) (string, error)
	PatchDisks(platform, disksJSON string) error
	AppendDisks(platform string, disks ...string) error
	SetArchitecture(arch string) error
	SetAutoscalerLabels(labels string) error
	GetArchitecture() (architecture.Architecture, error)
	GetUserDataSecret() (*Secret, error)
	GetManagedUserDataSecret() (ManagedUserDataSecret, error)
	SetUserDataSecret(userDataSecretName string) error
	SetPool(poolName string) error
	GetReplicaOfSpec() (string, error)
	ScaleTo(scale int) error
	AddToScale(delta int) error
	GetIsReady() bool
	WaitUntilReady(duration string) error
	GetNodes() ([]*Node, error)
	GetNodesOrFail() []*Node
	GetMachinesByPhase(phase string) ([]ManagedMachine, error)
	AllNodesUpdated() (bool, error)
	GetOSStreamLabel() (string, error)
	AddOSStreamLabel(streamName string) error
	RemoveOSStreamLabel() error
	GetOSStream() string
	GetWorkspaceFolder() (string, error)
	GetVSphereFailureDomain() (string, error)
	GetVSphereConnectionInfo() (*exutil.VSphereConnectionInfo, error)
	GetAPIGroup() string
}

// GetAllManagedMachineResources returns all authoritative ManagedMachineResource resources available in the cluster.
// MAPI MachineSets with status.authoritativeAPI == "ClusterAPI" are excluded as they are just mirrors of the CAPI resources.
// An optional list of MachineAPIType values can be provided to restrict which API types are collected.
// If no types are provided, all types are collected.
// Returns an error if the list cannot be retrieved. Returns an empty slice (not an error) if no resources exist.
func GetAllManagedMachineResources(oc *exutil.CLI, apiTypes ...MachineAPIType) ([]ManagedMachineResource, error) {
	var result []ManagedMachineResource

	collectMAPI := len(apiTypes) == 0
	collectCAPI := len(apiTypes) == 0
	for _, t := range apiTypes {
		switch t {
		case MAPI:
			collectMAPI = true
		case CAPI:
			collectCAPI = true
		}
	}

	if collectMAPI {
		mapiMachineSets, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		if err != nil {
			return nil, err
		}
		for _, ms := range mapiMachineSets {
			authAPI, err := ms.Get(`{.status.authoritativeAPI}`)
			if err != nil {
				return nil, err
			}
			if authAPI == "ClusterAPI" {
				logger.Infof("Skipping MAPI MachineSet %s: authoritativeAPI is ClusterAPI", ms.GetName())
				continue
			}
			result = append(result, ms)
		}
	}

	if collectCAPI {
		capiMachineSets, err := NewCAPIMachineSetList(oc.AsAdmin(), ClusterAPINamespace).GetAll()
		if err != nil {
			logger.Infof("Could not list CAPI MachineSets in %s: %v", ClusterAPINamespace, err)
		} else {
			for _, ms := range capiMachineSets {
				result = append(result, ms)
			}
		}
	}

	return result, nil
}

// GetValidManagedMachineResource returns a ManagedMachineResource with replicas > 0 that is ready and can be used for testing.
// An optional list of MachineAPIType values can be provided to restrict which API types are considered.
func GetValidManagedMachineResource(oc *exutil.CLI, apiTypes ...MachineAPIType) (ManagedMachineResource, error) {
	all, err := GetAllManagedMachineResources(oc, apiTypes...)
	if err != nil {
		return nil, fmt.Errorf("error listing ManagedMachineResources: %w", err)
	}
	for _, ms := range all {
		replicas, err := ms.GetReplicaOfSpec()
		if err != nil || replicas == "0" || !ms.GetIsReady() {
			continue
		}
		return ms, nil
	}
	return nil, fmt.Errorf("no ready ManagedMachineResource with replicas > 0 found")
}

// GetValidManagedMachineResourceByArchitecture returns a ready ManagedMachineResource with
// replicas > 0 that matches the given architecture.
func GetValidManagedMachineResourceByArchitecture(oc *exutil.CLI, arch architecture.Architecture) (ManagedMachineResource, error) {
	all, err := GetAllManagedMachineResources(oc)
	if err != nil {
		return nil, fmt.Errorf("error listing ManagedMachineResources: %w", err)
	}
	for _, ms := range all {
		replicas, err := ms.GetReplicaOfSpec()
		if err != nil || replicas == "0" || !ms.GetIsReady() {
			continue
		}
		msArch, err := ms.GetArchitecture()
		if err != nil {
			continue
		}
		if msArch == arch {
			return ms, nil
		}
	}
	return nil, fmt.Errorf("no ready ManagedMachineResource with architecture %s found", arch)
}
