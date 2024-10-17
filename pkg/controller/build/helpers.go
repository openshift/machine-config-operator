package build

import (
	"context"
	"fmt"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/docker/reference"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
)

func validateImageHasDigestedPullspec(pullspec string) error {
	tagged, err := docker.ParseReference("//" + pullspec)
	if err != nil {
		return err
	}

	switch tagged.DockerReference().(type) {
	case reference.Tagged:
		return fmt.Errorf("expected a pullspec with a SHA256 digest, got %q", pullspec)
	case reference.Digested:
		return nil
	default:
		return fmt.Errorf("unknown image reference spec %q", pullspec)
	}
}

// Replaces any tags on the image pullspec with the provided image digest.
func parseImagePullspecWithDigest(pullspec string, imageDigest digest.Digest) (string, error) {
	named, err := reference.ParseNamed(pullspec)
	if err != nil {
		return "", err
	}

	canonical, err := reference.WithDigest(reference.TrimNamed(named), imageDigest)
	if err != nil {
		return "", err
	}

	return canonical.String(), nil
}

// Parses an image pullspec from a string and an image SHA and replaces any
// tags on the pullspec with the provided image SHA.
func ParseImagePullspec(pullspec, imageSHA string) (string, error) {
	imageDigest, err := digest.Parse(imageSHA)
	if err != nil {
		return "", err
	}

	return parseImagePullspecWithDigest(pullspec, imageDigest)
}

// Converts a given Kube object into an object reference.
func toObjectRef(obj interface {
	GetName() string
	GetNamespace() string
	GetUID() k8stypes.UID
	GetObjectKind() schema.ObjectKind
}) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		Kind:      obj.GetObjectKind().GroupVersionKind().Kind,
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		UID:       obj.GetUID(),
	}
}

// Returns any supplied error except ones that match k8serrors.IsNotFound().
func ignoreIsNotFoundErr(err error) error {
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	return nil
}

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

	return buildrequest.ValidatePullSecret(secret)
}
