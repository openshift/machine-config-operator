package utils

import (
	"fmt"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
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
	mcfgv1.AddToScheme(s)

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
func GetAdditionalTrustBundleConfigMapName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("additionaltrustbundle-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the Containerfile ConfigMap name.
func GetContainerfileConfigMapName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("containerfile-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the MachineConfig ConfigMap name.
func GetMCConfigMapName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("mc-%s", getFieldFromMachineOSBuild(mosb))
}

func GetEtcPolicyConfigMapName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("etc-policy-%s", getFieldFromMachineOSBuild(mosb))
}

func GetEtcRegistriesConfigMapName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("etc-registries-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the build job name.
func GetBuildJobName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("build-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the digest configmap name.
func GetDigestConfigMapName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("digest-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the base image pull secret name.
func GetBasePullSecretName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("base-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the final image push secret name.
func GetFinalPushSecretName(mosb *mcfgv1.MachineOSBuild) string {
	return fmt.Sprintf("final-%s", getFieldFromMachineOSBuild(mosb))
}

func getFieldFromMachineOSBuild(mosb *mcfgv1.MachineOSBuild) string {
	return mosb.Name
}
