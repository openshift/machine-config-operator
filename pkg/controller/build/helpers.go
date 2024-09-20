package build

import (
	"context"
	"fmt"

	"github.com/containers/image/v5/docker/reference"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
)

// ValidateOnClusterBuildConfig validates the existence of the MachineOSConfig and the required build inputs.
func ValidateOnClusterBuildConfig(kubeclient clientset.Interface, mcfgclient versioned.Interface, layeredMCPs []*mcfgv1.MachineConfigPool) error {
	// Validate the presence of the MachineOSConfig
	machineOSConfigs, err := mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	secretGetter := func(name string) (*corev1.Secret, error) {
		return kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	}

	moscForPoolExists := false
	var moscForPool *mcfgv1alpha1.MachineOSConfig
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

func validateMachineOSConfig(mcpGetter func(string) (*mcfgv1.MachineConfigPool, error), secretGetter func(string) (*corev1.Secret, error), mosc *mcfgv1alpha1.MachineOSConfig) error {
	_, err := mcpGetter(mosc.Spec.MachineConfigPool.Name)
	if err != nil && k8serrors.IsNotFound(err) {
		return fmt.Errorf("no MachineConfigPool named %s exists for MachineOSConfig %s", mosc.Spec.MachineConfigPool.Name, mosc.Name)
	}

	if err != nil {
		return fmt.Errorf("could not get MachineConfigPool %s: %w", mosc.Spec.MachineConfigPool.Name, err)
	}

	secretFields := map[string]string{
		mosc.Spec.BuildInputs.BaseImagePullSecret.Name:     "baseImagePullSecret",
		mosc.Spec.BuildInputs.RenderedImagePushSecret.Name: "renderedImagePushSecret",
		mosc.Spec.BuildOutputs.CurrentImagePullSecret.Name: "currentImagePullSecret",
	}

	for secretName, fieldName := range secretFields {
		if err := validateSecret(secretGetter, mosc, secretName); err != nil {
			return fmt.Errorf("could not validate %s %q for MachineOSConfig %s: %w", fieldName, secretName, mosc.Name, err)
		}
	}

	if _, err := reference.ParseNamed(mosc.Spec.BuildInputs.RenderedImagePushspec); err != nil {
		return fmt.Errorf("could not validate renderdImagePushspec %s for MachineOSConfig %s: %w", mosc.Spec.BuildInputs.RenderedImagePushspec, mosc.Name, err)
	}

	return nil
}

func ValidateMachineOSConfigFromListers(mcpLister mcfglistersv1.MachineConfigPoolLister, secretLister corelisterv1.SecretLister, mosc *mcfgv1alpha1.MachineOSConfig) error {
	mcpGetter := func(name string) (*mcfgv1.MachineConfigPool, error) {
		return mcpLister.Get(name)
	}

	secretGetter := func(name string) (*corev1.Secret, error) {
		return secretLister.Secrets(ctrlcommon.MCONamespace).Get(name)
	}

	return validateMachineOSConfig(mcpGetter, secretGetter, mosc)
}

func validateSecret(secretGetter func(string) (*corev1.Secret, error), mosc *mcfgv1alpha1.MachineOSConfig, secretName string) error {
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

	return utils.ValidatePullSecret(secret)
}

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
		mcfgv1alpha1.MachineOSBuildFailed,
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
