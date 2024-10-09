package build

import (
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
)

func isMachineOSBuildAnythingButSucceeded(mosb *mcfgv1alpha1.MachineOSBuild) bool {
	// If there are no conditions, it means the build has not yet run.
	if len(mosb.Status.Conditions) == 0 {
		return false
	}

	// If the build succeeded, then we don't need to do anything further.
	if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, mcfgv1alpha1.MachineOSBuildSucceeded) {
		return false
	}

	buildStatuses := []mcfgv1alpha1.BuildProgress{
		mcfgv1alpha1.MachineOSBuildPrepared,
		mcfgv1alpha1.MachineOSBuilding,
		mcfgv1alpha1.MachineOSBuildInterrupted,
	}

	// If any of the above build statuses is true, it means a build is in progress.
	for _, buildStatus := range buildStatuses {
		if apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, buildStatus) {
			return true
		}
	}

	return false
}

func getMachineOSConfigNames(moscList []*mcfgv1alpha1.MachineOSConfig) []string {
	out := []string{}

	for _, mosc := range moscList {
		out = append(out, mosc.Name)
	}

	return out
}

func getMachineOSBuildNames(mosbList []*mcfgv1alpha1.MachineOSBuild) []string {
	out := []string{}

	for _, mosc := range mosbList {
		out = append(out, mosc.Name)
	}

	return out
}
