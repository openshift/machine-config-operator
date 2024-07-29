package buildrequest

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// Computes the Containerfile ConfigMap name.
func GetContainerfileConfigMapName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("containerfile-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the MachineConfig ConfigMap name.
func GetMCConfigMapName(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return fmt.Sprintf("mc-%s", getFieldFromMachineOSBuild(mosb))
}

// Computes the build pod name.
func GetBuildPodName(mosb *mcfgv1alpha1.MachineOSBuild) string {
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

func ValidatePullSecret(secret *corev1.Secret) error {
	_, err := getPullSecretKey(secret)

	if err != nil {
		return fmt.Errorf("invalid secret %s: %w", secret.Name, err)
	}

	return nil
}

// Gets the field from the MachineOSBuild that is used for naming the ephemeral
// build objects. For now, we're using the DesiredConfig name from the
// MachineConfig, but arguably, we should be using the name of the
// MachineOSBuild object instead.
func getFieldFromMachineOSBuild(mosb *mcfgv1alpha1.MachineOSBuild) string {
	return mosb.Spec.DesiredConfig.Name
}

// Compresses and base-64 encodes a given byte array. Ideal for loading an
// arbitrary byte array into a ConfigMap or Secret.
func compressAndEncode(payload []byte) (*bytes.Buffer, error) {
	out := bytes.NewBuffer(nil)

	if len(payload) == 0 {
		return out, nil
	}

	// We need to base64-encode our gzipped data so we can marshal it in and out
	// of a string since ConfigMaps and Secrets expect a textual representation.
	base64Enc := base64.NewEncoder(base64.StdEncoding, out)
	defer base64Enc.Close()

	err := compress(bytes.NewBuffer(payload), base64Enc)
	if err != nil {
		return nil, fmt.Errorf("could not compress and encode payload: %w", err)
	}

	err = base64Enc.Close()
	if err != nil {
		return nil, fmt.Errorf("could not close base64 encoder: %w", err)
	}

	return out, err
}

// Compresses a given io.Reader to a given io.Writer
func compress(r io.Reader, w io.Writer) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("could not initialize gzip writer: %w", err)
	}

	defer gz.Close()

	if _, err := io.Copy(gz, r); err != nil {
		return fmt.Errorf("could not compress payload: %w", err)
	}

	if err := gz.Close(); err != nil {
		return fmt.Errorf("could not close gzipwriter: %w", err)
	}

	return nil
}

// Performs the above operation upon a given secret, potentially creating a new
// secret for insertion with the suffix '-canonical' on its name and a label
// indicating that we've canonicalized it.
func canonicalizePullSecret(secret *corev1.Secret) (*corev1.Secret, error) {
	secret = secret.DeepCopy()

	key, err := getPullSecretKey(secret)
	if err != nil {
		return nil, err
	}

	secretBytes, ok := secret.Data[key]
	if !ok {
		return nil, fmt.Errorf("could not locate key %q in %s", key, secret.Name)
	}

	canonicalizedSecretBytes, _, err := ctrlcommon.ConvertSecretToDockerconfigJSON(secretBytes)
	if err != nil {
		return nil, err
	}

	return newCanonicalSecret(secret, canonicalizedSecretBytes), nil
}

// Creates a new canonicalized secret with the appropriate suffix, labels, etc.
// Does *not* validate whether the inputted secret bytes are in the correct
// format.
func newCanonicalSecret(secret *corev1.Secret, secretBytes []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-canonical", secret.Name),
			Namespace: secret.Namespace,
			Labels: map[string]string{
				constants.CanonicalSecretLabelKey:    "",
				constants.OriginalSecretNameLabelKey: secret.Name,
				constants.OnClusterLayeringLabelKey:  "",
			},
		},
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: secretBytes,
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}
}

// Looks up a given secret key for a given secret type and validates that the
// key is present and the secret is a non-zero length. Returns an error if it
// is the incorrect secret type, missing the appropriate key, or the secret is
// a zero-length.
func getPullSecretKey(secret *corev1.Secret) (string, error) {
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
