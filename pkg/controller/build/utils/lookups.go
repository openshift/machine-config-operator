package utils

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
)

// Holds a group of listers used for resolving OCL objects to other OCL objects
// and to MachineConfigPools.
type Listers struct {
	// TODO: Consider adding full mcfgclients too.
	MachineOSBuildLister    mcfglistersv1.MachineOSBuildLister
	MachineOSConfigLister   mcfglistersv1.MachineOSConfigLister
	MachineConfigPoolLister mcfglistersv1.MachineConfigPoolLister
	NodeLister              corelistersv1.NodeLister
}

// Gets a MachineConfigPool after first ensuring that the lister is not nil.
func (l *Listers) getMachineConfigPool(name string) (*mcfgv1.MachineConfigPool, error) {
	if l.MachineConfigPoolLister == nil {
		return nil, fmt.Errorf("required MachineConfigPoolLister is nil")
	}

	return l.MachineConfigPoolLister.Get(name)
}

// Lists all MachineConfigPools matching the given selector after first
// ensuring that the lister is not nil.
func (l *Listers) listMachineConfigPools(sel labels.Selector) ([]*mcfgv1.MachineConfigPool, error) {
	if l.MachineConfigPoolLister == nil {
		return nil, fmt.Errorf("required MachineConfigPoolLister is nil")
	}

	return l.MachineConfigPoolLister.List(sel)
}

// Gets a MachineOSConfig after first ensuring that the lister is not nil.
func (l *Listers) getMachineOSConfig(name string) (*mcfgv1.MachineOSConfig, error) {
	if l.MachineOSConfigLister == nil {
		return nil, fmt.Errorf("required MachineOSConfigLister is nil")
	}

	return l.MachineOSConfigLister.Get(name)
}

// Lists all MachineOSConfigs matching the given selector after first
// ensuring that the lister is not nil.
func (l *Listers) listMachineOSConfigs(sel labels.Selector) ([]*mcfgv1.MachineOSConfig, error) {
	if l.MachineOSConfigLister == nil {
		return nil, fmt.Errorf("required MachineOSConfigLister is nil")
	}

	return l.MachineOSConfigLister.List(sel)
}

// Gets a MachineOSBuild after first ensuring that the lister is not nil.
func (l *Listers) getMachineOSBuild(name string) (*mcfgv1.MachineOSBuild, error) {
	if l.MachineOSBuildLister == nil {
		return nil, fmt.Errorf("required MachineOSBuildLister is nil")
	}

	return l.MachineOSBuildLister.Get(name)
}

// Lists all MachineOSBuilds matching the given selector after first
// ensuring that the lister is not nil.
func (l *Listers) listMachineOSBuilds(sel labels.Selector) ([]*mcfgv1.MachineOSBuild, error) {
	if l.MachineOSBuildLister == nil {
		return nil, fmt.Errorf("required MachineOSBuildLister is nil")
	}

	return l.MachineOSBuildLister.List(sel)
}

// Gets the first MachineOSConfig found for a given MachineConfigPool. Use
// GetMachineOSConfigForMachineConfigPoolStrict() if one wants to ensure that
// only a single MachineOSConfig is found for a MachineConfigPool.
func GetMachineOSConfigForMachineConfigPool(mcp *mcfgv1.MachineConfigPool, listers *Listers) (*mcfgv1.MachineOSConfig, error) {
	moscList, err := listers.listMachineOSConfigs(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("could not list MachineOSConfigs: %w", err)
	}

	for _, mosc := range moscList {
		if mosc.Spec.MachineConfigPool.Name == mcp.Name {
			return mosc.DeepCopy(), nil
		}
	}

	errNotFound := k8serrors.NewNotFound(mcfgv1.GroupVersion.WithResource("machineosconfigs").GroupResource(), "")
	return nil, fmt.Errorf("could not find MachineOSConfig for MachineConfigPool %q: %w", mcp.Name, errNotFound)
}

// Gets the MachineOSConfig for a given MachineConfigPool. Unlike
// GetMachineOSConfigForMachineConfigPool(), this version will return an error
// if more than one MachineOSConfig is found associated with a given
// MachineConfigPool.
func GetMachineOSConfigForMachineConfigPoolStrict(mcp *mcfgv1.MachineConfigPool, listers *Listers) (*mcfgv1.MachineOSConfig, error) {
	moscList, err := listers.listMachineOSConfigs(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("could not list MachineOSConfigs: %w", err)
	}

	found := &mcfgv1.MachineOSConfig{}
	others := []string{}

	for _, mosc := range moscList {
		if mosc.Spec.MachineConfigPool.Name == mcp.Name {
			if found == nil {
				found = mosc
			} else {
				others = append(others, mosc.Name)
			}
		}
	}

	if found == nil {
		errNotFound := k8serrors.NewNotFound(mcfgv1.GroupVersion.WithResource("machineosconfigs").GroupResource(), "")
		return nil, fmt.Errorf("could not find MachineOSConfig for MachineConfigPool %q: %w", mcp.Name, errNotFound)
	}

	if found != nil && len(others) == 0 {
		return found.DeepCopy(), nil
	}

	return nil, fmt.Errorf("expected to only find one MachineOSConfig for MachineConfigPool %q, found: %d (%v)", mcp.Name, len(others), others)
}

// Gets the MachineOSConfig for a given MachineOSBuild.
func GetMachineOSConfigForMachineOSBuild(mosb *mcfgv1.MachineOSBuild, listers *Listers) (*mcfgv1.MachineOSConfig, error) {
	moscName, err := GetRequiredLabelValueFromObject(mosb, constants.MachineOSConfigNameLabelKey)
	if err != nil {
		return nil, fmt.Errorf("could not identify MachineOSConfig for MachineOSBuild %q: %w", mosb.Name, err)
	}

	mosc, err := listers.getMachineOSConfig(moscName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineOSConfig %q for MachineOSBuild %q: %w", moscName, mosb.Name, err)
	}

	return mosc.DeepCopy(), nil
}

// Gets the MachineOSBuild for a given MachineConfigPool.
func GetMachineOSBuildForMachineConfigPool(mcp *mcfgv1.MachineConfigPool, listers *Listers) (*mcfgv1.MachineOSBuild, error) {
	mosc, err := GetMachineOSConfigForMachineConfigPool(mcp, listers)
	if err != nil {
		return nil, err
	}

	return getMachineOSBuildForMachineConfigPoolAndMachineOSConfig(mcp, mosc, listers)
}

// Gets the MachineOSBuild which matches a given image pullspec.
func GetMachineOSBuildForImagePullspec(pullspec string, listers *Listers) (*mcfgv1.MachineOSBuild, error) {
	if pullspec == "" {
		return nil, fmt.Errorf("required pullspec empty")
	}

	mosbList, err := listers.listMachineOSBuilds(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, mosb := range mosbList {
		if string(mosb.Status.DigestedImagePushSpec) == pullspec {
			return mosb, nil
		}
	}

	errNotFound := k8serrors.NewNotFound(mcfgv1.GroupVersion.WithResource("machineosbuilds").GroupResource(), "")
	return nil, fmt.Errorf("could not find MachineOSBuild with image pullspec %q: %w", pullspec, errNotFound)
}

// Gets both the MachineOSConfig and the MachineOSBuild for a given MachineConfigPool.
func GetMachineOSConfigAndMachineOSBuildForMachineConfigPool(mcp *mcfgv1.MachineConfigPool, listers *Listers) (*mcfgv1.MachineOSConfig, *mcfgv1.MachineOSBuild, error) {
	mosc, err := GetMachineOSConfigForMachineConfigPoolStrict(mcp, listers)
	if err != nil {
		return nil, nil, err
	}

	mosb, err := getMachineOSBuildForMachineConfigPoolAndMachineOSConfig(mcp, mosc, listers)
	if err != nil {
		return mosc, nil, err
	}

	return mosc, mosb, nil
}

// Gets the MachineOSBuild that belongs to the given MachineConfigPool and MachineOSConfig. Ensures that only a single MachineOSBuild is returned.
func getMachineOSBuildForMachineConfigPoolAndMachineOSConfig(mcp *mcfgv1.MachineConfigPool, mosc *mcfgv1.MachineOSConfig, listers *Listers) (*mcfgv1.MachineOSBuild, error) {
	sel := MachineOSBuildSelector(mosc, mcp)

	mosbList, err := listers.listMachineOSBuilds(sel)
	if err != nil {
		return nil, fmt.Errorf("could not list MachineOSBuilds using selector %q: %w", sel.String(), err)
	}

	if len(mosbList) == 1 {
		return mosbList[0].DeepCopy(), nil
	}

	if len(mosbList) == 0 {
		errNotFound := k8serrors.NewNotFound(mcfgv1.GroupVersion.WithResource("machineosbuilds").GroupResource(), "")
		return nil, fmt.Errorf("could not find MachineOSBuilds for MachineConfigPool %q and MachineOSConfig %q: %w", mcp.Name, mosc.Name, errNotFound)
	}

	names := []string{}

	for _, mosb := range mosbList {
		names = append(names, mosb.Name)
	}

	return nil, fmt.Errorf("single MachineOSBuild expected, found multiple: %v", names)
}

// Gets the MachineConfigPool for a given MachineOSBuild.
func GetMachineConfigPoolForMachineOSBuild(mosb *mcfgv1.MachineOSBuild, listers *Listers) (*mcfgv1.MachineConfigPool, error) {
	mcpName, err := GetRequiredLabelValueFromObject(mosb, constants.TargetMachineConfigPoolLabelKey)
	if err != nil {
		return nil, fmt.Errorf("could not identify MachineConfigPool from MachineOSBuild %q: %w", mosb.Name, err)
	}

	mcp, err := listers.getMachineConfigPool(mcpName)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineConfigPool for MachineOSBuild %q: %w", mosb.Name, err)
	}

	return mcp, nil
}

// Gets the MachineConfigPool for a given MachineOSConfig.
func GetMachineConfigPoolForMachineOSConfig(mosc *mcfgv1.MachineOSConfig, listers *Listers) (*mcfgv1.MachineConfigPool, error) {
	mcp, err := listers.getMachineConfigPool(mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get MachineConfigPool %q for MachineOSConfig %q: %w", mosc.Spec.MachineConfigPool.Name, mosc.Name, err)
	}

	return mcp, nil
}

func GetMachineOSBuildsForMachineOSConfig(mosc *mcfgv1.MachineOSConfig, listers *Listers) ([]*mcfgv1.MachineOSBuild, error) {
	mosc, err := listers.getMachineOSConfig(mosc.Name)
	if err != nil {
		return nil, err
	}

	sel := labels.SelectorFromSet(map[string]string{
		constants.MachineOSConfigNameLabelKey: mosc.Name,
	})

	return listers.listMachineOSBuilds(sel)
}
