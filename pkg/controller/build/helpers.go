package build

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

// ValidateOnClusterBuildConfig validates the existence of the MachineOSConfig and the required build inputs.
func ValidateOnClusterBuildConfig(kubeclient clientset.Interface, mcfgclient versioned.Interface, layeredMCPs []*mcfgv1.MachineConfigPool) error {
	// Validate the presence of the MachineOSConfig
	machineOSConfigs, err := mcfgclient.MachineconfigurationV1().MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	secretGetter := func(name string) (*corev1.Secret, error) {
		return kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	}

	moscForPoolExists := false
	var moscForPool *mcfgv1.MachineOSConfig
	for _, pool := range layeredMCPs {
		moscForPoolExists = false
		for _, mosc := range machineOSConfigs.Items {
			if mosc.Spec.MachineConfigPool.Name == pool.Name {
				moscForPoolExists = true
				moscForPool = &mosc
				break
			}
		}

		if !moscForPoolExists {
			return fmt.Errorf("MachineOSConfig for pool %s missing, did you create it?", pool.Name)
		}

		mcpGetter := func(_ string) (*mcfgv1.MachineConfigPool, error) {
			return pool, nil
		}

		if err := validateMachineOSConfig(mcpGetter, secretGetter, moscForPool); err != nil {
			return err
		}
	}

	return nil
}

func validateMachineOSConfig(mcpGetter func(string) (*mcfgv1.MachineConfigPool, error), secretGetter func(string) (*corev1.Secret, error), mosc *mcfgv1.MachineOSConfig) error {
	_, err := mcpGetter(mosc.Spec.MachineConfigPool.Name)
	if err != nil && k8serrors.IsNotFound(err) {
		return fmt.Errorf("no MachineConfigPool named %s exists for MachineOSConfig %s", mosc.Spec.MachineConfigPool.Name, mosc.Name)
	}

	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %s: %w", mosc.Spec.MachineConfigPool.Name, err)
	}

	secretFields := map[string]string{
		mosc.Spec.RenderedImagePushSecret.Name: "renderedImagePushSecret",
	}
	// Add base image pull secret if it has been defined in the MOSC
	if mosc.Spec.BaseImagePullSecret != nil {
		secretFields[mosc.Spec.BaseImagePullSecret.Name] = "baseImagePullSecret"
	}

	for secretName, fieldName := range secretFields {
		if err := validateSecret(secretGetter, mosc, secretName); err != nil {
			return fmt.Errorf("could not validate %s %q for MachineOSConfig %s: %w", fieldName, secretName, mosc.Name, err)
		}
	}

	if _, err := reference.ParseNamed(string(mosc.Spec.RenderedImagePushSpec)); err != nil {
		return fmt.Errorf("could not validate renderdImagePushspec %s for MachineOSConfig %s: %w", string(mosc.Spec.RenderedImagePushSpec), mosc.Name, err)
	}

	return nil
}

func ValidateMachineOSConfigFromListers(mcpLister mcfglistersv1.MachineConfigPoolLister, secretLister corelisterv1.SecretLister, mosc *mcfgv1.MachineOSConfig) error {
	mcpGetter := func(name string) (*mcfgv1.MachineConfigPool, error) {
		return mcpLister.Get(name)
	}

	secretGetter := func(name string) (*corev1.Secret, error) {
		return secretLister.Secrets(ctrlcommon.MCONamespace).Get(name)
	}

	return validateMachineOSConfig(mcpGetter, secretGetter, mosc)
}

func validateSecret(secretGetter func(string) (*corev1.Secret, error), mosc *mcfgv1.MachineOSConfig, secretName string) error {
	if secretName == "" {
		return fmt.Errorf("no secret name provided")
	}

	secret, err := secretGetter(secretName)

	if err != nil && k8serrors.IsNotFound(err) {
		return fmt.Errorf("secret %s from %s is not found. Did you use the right secret name?", secretName, mosc.Name)
	}

	if err != nil {
		return fmt.Errorf("could not get secret %s for MachineOSConfig %s: %w", secretName, mosc.Name, err)
	}

	return secrets.ValidateKubernetesImageRegistrySecret(secret)
}

// Determines if a MachineOSBuild status update is needed. These are needed
// primarily when we transition from the initial status -> transient state ->
// terminal state.
func isMachineOSBuildStatusUpdateNeeded(oldStatus, curStatus mcfgv1.MachineOSBuildStatus) (bool, string) {
	oldState := ctrlcommon.NewMachineOSBuildStateFromStatus(oldStatus)
	curState := ctrlcommon.NewMachineOSBuildStateFromStatus(curStatus)

	// From having no build conditions to having the initial state set.
	if !oldState.HasBuildConditions() && curState.HasBuildConditions() && curState.IsInInitialState() {
		return true, "in initial state"
	}

	oldTransientState := oldState.GetTransientState()
	curTransientState := curState.GetTransientState()

	// From initial state -> pending or building.
	if oldState.IsInInitialState() && curState.IsInTransientState() {
		return true, fmt.Sprintf("transitioned from initial state -> transient state (%s)", curTransientState)
	}

	// From pending -> building, but not building -> pending.
	if oldState.IsInTransientState() && curState.IsInTransientState() && oldTransientState != curTransientState {
		reason := fmt.Sprintf("transitioned from transient state (%s) -> transient state (%s)", oldTransientState, curTransientState)
		isValid := oldTransientState == mcfgv1.MachineOSBuildPrepared && curTransientState == mcfgv1.MachineOSBuilding
		return isValid, reason
	}

	oldTerminalState := oldState.GetTerminalState()
	curTerminalState := curState.GetTerminalState()

	// From building -> {success, failure, interrupted}
	if oldState.IsInTransientState() && curState.IsInTerminalState() {
		return true, fmt.Sprintf("transitioned from transient state (%s) -> terminal state (%s)", oldTransientState, curTerminalState)
	}

	// From initial state -> {success, failure, interrupted}
	// It's rare that this could occur, but better to be explicit that it can occur.
	if oldState.IsInInitialState() && curState.IsInTerminalState() {
		return true, fmt.Sprintf("transitioned from initial state -> terminal state (%s)", curTerminalState)
	}

	// Once a build enters a terminal state, it cannot transition to any other
	// terminal, transient, or initial state.

	// From {success, failure, interrupted} -> {success, failure, interrupted}
	if oldState.IsInTerminalState() && curState.IsInTerminalState() {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> terminal state (%s)", oldTerminalState, curTerminalState)
	}

	// From {success, failure, interrupted} -> {pending, running}
	if oldState.IsInTerminalState() && curState.IsInTransientState() {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> transient state (%s)", oldTerminalState, curTransientState)
	}

	// From {sucecss, failure, interrupted} -> initial state
	if oldState.IsInTerminalState() && curState.IsInInitialState() {
		return false, fmt.Sprintf("transitioned from terminal state (%s) -> initial state", oldTerminalState)
	}

	// Everything else
	return false, ""
}

// Converts a list of MachineOSConfigs into a list of their names.
func getMachineOSConfigNames(moscList []*mcfgv1.MachineOSConfig) []string {
	out := []string{}

	for _, mosc := range moscList {
		out = append(out, mosc.Name)
	}

	return out
}

// Converts a list of MachineOSBuilds into a list of their names.
func getMachineOSBuildNames(mosbList []*mcfgv1.MachineOSBuild) []string {
	out := []string{}

	for _, mosc := range mosbList {
		out = append(out, mosc.Name)
	}

	return out
}

// Determines if a MachineOSBuild is current for a given MachineOSConfig solely
// by looking at the current build annotation on the MachineOSConfig.
func isMachineOSBuildCurrentForMachineOSConfig(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	// If we don't have the current build annotation, then we cannot even make this determination.
	if !hasCurrentBuildAnnotation(mosc) {
		return false
	}

	// If we have the current build annotation but it does not equal the current
	// MachineOSBuild, then this is clearly not the correct MachineOSBuild.
	if !isCurrentBuildAnnotationEqual(mosc, mosb) {
		return false
	}

	return true
}

// Determines if a MachineOSBuild is current for a given MachineOSConfig by
// considering the current build annotation and the image pullspec. If the
// MachineOSBuild has not (yet) set its final image pushspec, this will return
// false.
func isMachineOSBuildCurrentForMachineOSConfigWithPullspec(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	// If the MachineOSConfig has the same final image pullspec as
	// the MachineOSBuild and the MachineOSBuild's pushspec is populated, we know
	// they're the same.
	return isMachineOSBuildCurrentForMachineOSConfig(mosc, mosb) &&
		mosc.Status.CurrentImagePullSpec == mosb.Status.DigestedImagePushSpec
}

// Determines if a given MachineOSConfig has the current build annotation.
func hasCurrentBuildAnnotation(mosc *mcfgv1.MachineOSConfig) bool {
	return metav1.HasAnnotation(mosc.ObjectMeta, constants.CurrentMachineOSBuildAnnotationKey) && mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey] != ""
}

// Determines if a given MachineOSConfig has the current build annotation and
// it matches the name of the given MachineOSBuild.
func isCurrentBuildAnnotationEqual(mosc *mcfgv1.MachineOSConfig, mosb *mcfgv1.MachineOSBuild) bool {
	if !hasCurrentBuildAnnotation(mosc) {
		return false
	}

	return mosc.Annotations[constants.CurrentMachineOSBuildAnnotationKey] == mosb.Name
}

// Determines if a given MachineOSConfig has the rebuild annotation.
func hasRebuildAnnotation(mosc *mcfgv1.MachineOSConfig) bool {
	return metav1.HasAnnotation(mosc.ObjectMeta, constants.RebuildMachineOSConfigAnnotationKey)
}

// hasPreBuiltImageAnnotation checks if a MachineOSConfig has the pre-built image annotation.
func hasPreBuiltImageAnnotation(mosc *mcfgv1.MachineOSConfig) bool {
	_, exists := mosc.Annotations[constants.PreBuiltImageAnnotationKey]
	return exists
}

// getPreBuiltImage returns the pre-built image from a MachineOSConfig's annotations.
// Returns the image string and a boolean indicating if it exists and is non-empty.
func getPreBuiltImage(mosc *mcfgv1.MachineOSConfig) (string, bool) {
	image, exists := mosc.Annotations[constants.PreBuiltImageAnnotationKey]
	return image, exists && image != ""
}

// shouldSeedWithPreBuiltImage determines if a MachineOSConfig should be seeded with a pre-built image.
// Returns true if:
// - The MOSC has a pre-built image annotation
// - The MOSC does NOT have a current build annotation (meaning seeding hasn't happened yet)
func shouldSeedWithPreBuiltImage(mosc *mcfgv1.MachineOSConfig) bool {
	return hasPreBuiltImageAnnotation(mosc) &&
		!hasCurrentBuildAnnotation(mosc)
}

// isPreBuiltImageAwaitingSeeding checks if a MOSC has pre-built image annotation but hasn't been seeded.
// This is useful for skipping normal build workflows when the seeding workflow should handle it.
// Seeding is considered complete once the currentBuild annotation is set.
func isPreBuiltImageAwaitingSeeding(mosc *mcfgv1.MachineOSConfig) bool {
	return hasPreBuiltImageAnnotation(mosc) && !hasCurrentBuildAnnotation(mosc)
}

// needsPreBuiltImageAnnotationCleanup determines if a MOSC has completed seeding and
// the pre-built image annotation can be safely removed.
// Returns true if:
// - The MOSC has a current build annotation (seeding is complete)
// - The MOSC status has been populated with CurrentImagePullSpec
// - The MOSC still has the PreBuiltImageAnnotationKey (needs cleanup)
func needsPreBuiltImageAnnotationCleanup(mosc *mcfgv1.MachineOSConfig) bool {
	return hasCurrentBuildAnnotation(mosc) &&
		mosc.Status.CurrentImagePullSpec != "" &&
		hasPreBuiltImageAnnotation(mosc)
}

// Looks at the error chain for the given error and determines if the error
// should be ignored or not based upon whether it is a not found error. If it
// should be ignored, this will log the error as well as the name and kind of
// the object that could not be found.
func ignoreErrIsNotFound(err error) error {
	// If this is not an IsNotFound error, just return it.
	if !k8serrors.IsNotFound(err) {
		return err
	}

	// If the error type matches k8serrors.StatusError, extract and log
	// information from it just for visibility reasons.
	var statusErr *k8serrors.StatusError
	if errors.As(err, &statusErr) {
		status := statusErr.Status()
		klog.Warningf("%s %q not found: %s", status.Details.Kind, status.Details.Name, err)
		return nil
	}

	// If the error type somehow does not match k8serrors.StatusError, return it.
	return err
}

// Extracts the namespace and name:tag from an image reference.
func extractNSAndNameWithTag(imageRef string) (string, string, error) {
	// Split the image reference to give an array of [registry, namespace, name:tag]
	parts := strings.SplitN(imageRef, "/", 3)
	if len(parts) < 3 {
		return "", "", fmt.Errorf("invalid image reference: %s", imageRef)
	}

	return parts[1], parts[2], nil
}

var errUnknownBuildFailure = fmt.Errorf("build failed for unknown reason")

// Extracts meaningful error from MachineOSBuild
func getBuildErrorFromMOSB(mosb *mcfgv1.MachineOSBuild) error {
	for _, condition := range mosb.Status.Conditions {
		if condition.Type == "Failed" && condition.Status == metav1.ConditionTrue {
			return fmt.Errorf("%s: %s", condition.Reason, condition.Message)
		}
	}
	return errUnknownBuildFailure
}
