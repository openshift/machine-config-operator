package manifests

import (
	"fmt"
	"reflect"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/lib/resourceread"
)

const (
	ManifestFileNameMCPMaster  ManifestMachineConfigPool = "master.machineconfigpool.yaml"
	ManifestFileNameMCPWorker  ManifestMachineConfigPool = "worker.machineconfigpool.yaml"
	ManifestFileNameMCPArbiter ManifestMachineConfigPool = "arbiter.machineconfigpool.yaml"
)

// ManifestMachineConfigPool represents the filename of a built-in MachineConfigPool manifest
// embedded in the operator.
type ManifestMachineConfigPool string

// GetMachineConfigPool reads and parses the given built-in MachineConfigPool manifest.
func GetMachineConfigPool(pool ManifestMachineConfigPool) (*mcfgv1.MachineConfigPool, error) {
	content, err := ReadFile(string(pool))
	if err != nil {
		return nil, fmt.Errorf("error reading pool manifest %s: %v", string(pool), err)
	}
	return resourceread.ReadMachineConfigPoolV1OrDie(content), nil
}

// GetMachineConfigPools reads and parses all built-in MachineConfigPool manifests
// (master, worker, and arbiter)
func GetMachineConfigPools() ([]*mcfgv1.MachineConfigPool, error) {
	var pools []*mcfgv1.MachineConfigPool
	for _, pool := range []ManifestMachineConfigPool{ManifestFileNameMCPMaster, ManifestFileNameMCPWorker, ManifestFileNameMCPArbiter} {
		poolObj, err := GetMachineConfigPool(pool)
		if err != nil {
			return nil, err
		}
		pools = append(pools, poolObj)
	}
	return pools, nil
}

// ContainsMachineConfigPool reports whether pool is identical (via reflect.DeepEqual) to any
// of the pools in generatedPools.
func ContainsMachineConfigPool(generatedPools []*mcfgv1.MachineConfigPool, pool *mcfgv1.MachineConfigPool) bool {
	if pool == nil || generatedPools == nil {
		return false
	}
	for _, generated := range generatedPools {
		if reflect.DeepEqual(generated, pool) {
			return true
		}
	}
	return false
}
