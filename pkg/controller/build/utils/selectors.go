package utils

import (
	"strings"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// Creates the labels for a given MachineOSBuild from the provided
// MachineOSConfig and MachineConfigPool.
func GetMachineOSBuildLabels(mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) map[string]string {
	return map[string]string{
		constants.TargetMachineConfigPoolLabelKey: mcp.Name,
		constants.RenderedMachineConfigLabelKey:   mcp.Spec.Configuration.Name,
		constants.MachineOSConfigNameLabelKey:     mosc.Name,
	}
}

// Creates a selector for a MachineOSBuild that matches the given
// MachineOSConfig and MachineConfigPool.
func MachineOSBuildSelector(mosc *mcfgv1.MachineOSConfig, mcp *mcfgv1.MachineConfigPool) labels.Selector {
	return labels.SelectorFromSet(GetMachineOSBuildLabels(mosc, mcp))
}

// Creates a selector for all MachineOSBuilds which are associated with a given
// MachineOSConfig.
func MachineOSBuildForPoolSelector(mosc *mcfgv1.MachineOSConfig) labels.Selector {
	return labels.SelectorFromSet(map[string]string{
		constants.TargetMachineConfigPoolLabelKey: mosc.Spec.MachineConfigPool.Name,
		constants.MachineOSConfigNameLabelKey:     mosc.Name,
	})
}

// Returns a selector with the appropriate labels for an OS build object label
// query.
func OSBuildSelector() labels.Selector {
	return labelsToSelector([]string{
		constants.OnClusterLayeringLabelKey,
		constants.RenderedMachineConfigLabelKey,
		constants.TargetMachineConfigPoolLabelKey,
	})
}

// Returns a selector with the appropriate labels for an ephemeral build object
// label query.
func EphemeralBuildObjectSelector() labels.Selector {
	return labelsToSelector([]string{
		constants.EphemeralBuildObjectLabelKey,
		constants.OnClusterLayeringLabelKey,
		constants.RenderedMachineConfigLabelKey,
		constants.TargetMachineConfigPoolLabelKey,
	})
}

// Creates a selector for looking up a builder, configmap, or secret associated
// with a given MachineOSBuild and MachineOSConfig.
func EphemeralBuildObjectSelectorForSpecificBuild(mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (labels.Selector, error) {
	selector := labelsToSelector([]string{
		constants.EphemeralBuildObjectLabelKey,
		constants.OnClusterLayeringLabelKey,
	})

	renderedMCReq, err := labels.NewRequirement(constants.RenderedMachineConfigLabelKey, selection.Equals, []string{mosb.Spec.MachineConfig.Name})
	if err != nil {
		return nil, err
	}

	mcpName, err := getMachineConfigPoolNameFromMachineOSConfigOrMachineOSBuild(mosb, mosc)
	if err != nil {
		return nil, err
	}

	mcpReq, err := labels.NewRequirement(constants.TargetMachineConfigPoolLabelKey, selection.Equals, []string{mcpName})
	if err != nil {
		return nil, err
	}

	mosbName, err := getMachineOSBuildNameFromMachineOSConfigOrMachineOSBuild(mosb, mosc)
	if err != nil {
		return nil, err
	}

	mosbNameReq, err := labels.NewRequirement(constants.MachineOSBuildNameLabelKey, selection.Equals, []string{mosbName})
	if err != nil {
		return nil, err
	}

	return selector.Add(*renderedMCReq, *mcpReq, *mosbNameReq), nil
}

// Fetches the MachineConfigPool name from either the MachineOSBuild or the
// MachineOSConfig. For MachineOSBuilds, this value is found as a label.
func getMachineConfigPoolNameFromMachineOSConfigOrMachineOSBuild(mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (string, error) {
	if mosc != nil {
		return mosc.Spec.MachineConfigPool.Name, nil
	}

	return GetRequiredLabelValueFromObject(mosb, constants.TargetMachineConfigPoolLabelKey)
}

// Fetches the MachineOSBuild name from either the MachineOSBuild or the
// MachineOSConfig. For MachineOSConfigs, this value is found as a label.
func getMachineOSBuildNameFromMachineOSConfigOrMachineOSBuild(mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (string, error) {
	if mosb != nil {
		return mosb.Name, nil
	}

	return GetRequiredAnnotationValueFromObject(mosc, constants.CurrentMachineOSBuildAnnotationKey)
}

// Returns a selector with the appropriate labels for a canonicalized secret
// label query.
func CanonicalizedSecretSelector() labels.Selector {
	return labelsToSelector([]string{
		constants.CanonicalSecretLabelKey,
		constants.OriginalSecretNameLabelKey,
		constants.OnClusterLayeringLabelKey,
	})
}

// Takes a list of label keys and converts them into a Selector object that
// will require all label keys to be present.
func labelsToSelector(requiredLabels []string) labels.Selector {
	reqs := []labels.Requirement{}

	for _, label := range requiredLabels {
		req, err := labels.NewRequirement(label, selection.Exists, []string{})
		if err != nil {
			panic(err)
		}

		reqs = append(reqs, *req)
	}

	return labels.NewSelector().Add(reqs...)
}

// Determines if a given object was created by BuildController. This is mostly
// useful for tests and other helpers that may need to clean up after a failed
// run. It first determines if the object is an ephemeral build object, next it
// checks whether the object has all of the required labels, next it checks if
// the object is a canonicalized secret, and finally, it checks whether the
// object is a MachineOSBuild.
func IsObjectCreatedByBuildController(obj metav1.Object) bool {
	if isEphemeralBuildObject(obj) {
		return true
	}

	if hasAllRequiredOSBuildLabels(obj.GetLabels()) {
		return true
	}

	secret, ok := obj.(*corev1.Secret)
	if ok && isCanonicalizedSecret(secret) {
		return true
	}

	if _, ok := obj.(*mcfgv1.MachineOSBuild); ok {
		return true
	}

	return false
}

// Determines if a secret has been canonicalized by us by checking both for the
// suffix as well as the labels that we add to the canonicalized secret.
func isCanonicalizedSecret(secret *corev1.Secret) bool {
	return hasCanonicalizedSecretLabels(secret) && strings.HasSuffix(secret.Name, "-canonical")
}

// Determines if a secret has our canonicalized secret label.
func hasCanonicalizedSecretLabels(secret *corev1.Secret) bool {
	return CanonicalizedSecretSelector().Matches(labels.Set(secret.Labels))
}

// Determines if an object is an ephemeral build object by examining its labels.
func isEphemeralBuildObject(obj metav1.Object) bool {
	return EphemeralBuildObjectSelector().Matches(labels.Set(obj.GetLabels()))
}

// Determines if an object is managed by this controller by examining its labels.
func hasAllRequiredOSBuildLabels(inLabels map[string]string) bool {
	return OSBuildSelector().Matches(labels.Set(inLabels))
}
