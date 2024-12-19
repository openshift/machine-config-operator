package utils

import (
	"fmt"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Gets the Kind for a given object by constructing a new Scheme and adding all
// of the objects to it.
func GetKindForObject(obj runtime.Object) (string, error) {
	s := runtime.NewScheme()
	corev1.AddToScheme(s)
	batchv1.AddToScheme(s)
	mcfgv1.AddToScheme(s)
	mcfgv1alpha1.AddToScheme(s)

	gvks, _, err := s.ObjectKinds(obj)
	if err != nil {
		return "", err
	}

	return gvks[0].GroupKind().Kind, nil
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

// Computes the AdditionalTrustBundle ConfigMap name based upon the MachineConfigPool name.
func GetAdditionalTrustBundleConfigMapName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("additionaltrustbundle-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the Containerfile ConfigMap name.
func GetContainerfileConfigMapName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("containerfile-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the MachineConfig ConfigMap name.
func GetMCConfigMapName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("mc-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the build job name.
func GetBuildName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("build-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the digest configmap name.
func GetDigestConfigMapName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("digest-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the base image pull secret name.
func GetBasePullSecretName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("base-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the final image push secret name.
func GetFinalPushSecretName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("final-%s", getFieldFromMachineOSBuild(mosb))
}

func getFieldFromMachineOSBuild(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return mosb.Name
}

// Validates that a given secret is an image pull secret.
func ValidatePullSecret(secret *corev1.Secret) error {
	_, err := GetPullSecretKey(secret)

	if err != nil {
		return fmt.Errorf("invalid secret %s: %w", secret.Name, err)
	}

	return nil
}

// Looks up a given secret key for a given secret type and validates that the
// key is present and the secret is a non-zero length. Returns an error if it
// is the incorrect secret type, missing the appropriate key, or the secret is
// a zero-length.
func GetPullSecretKey(secret *corev1.Secret) (string, error) {
	if secret.Type != corev1.SecretTypeDockerConfigJson && secret.Type != corev1.SecretTypeDockercfg {
		return "", fmt.Errorf("unknown secret type %s", secret.Type)
	}

	secretTypes := map[corev1.SecretType]string{
		corev1.SecretTypeDockercfg:        corev1.DockerConfigKey,
		corev1.SecretTypeDockerConfigJson: corev1.DockerConfigJsonKey,
	}

	key := secretTypes[secret.Type]

	val, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("missing %q in %s", key, secret.Name)
	}

	if len(val) == 0 {
		return "", fmt.Errorf("empty value %q in %s", key, secret.Name)
	}

	return key, nil
}
