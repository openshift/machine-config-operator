package buildrequest

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/containers/image/v5/docker"
	"github.com/distribution/reference"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// compressAndEncode compresses and base-64 encodes a given byte array. Ideal for loading an
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

// compress compresses a given io.Reader to a given io.Writer.
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

// canonicalizePullSecret creates a new image registry secret for insertion with the suffix
// '-canonical' on its name and a label indicating that we've canonicalized it.
func canonicalizePullSecret(secret *corev1.Secret) (*corev1.Secret, error) {
	k8sSecret, err := secrets.NormalizeDockerConfigJSONSecret(secret.DeepCopy())
	if err != nil {
		return nil, err
	}

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
			corev1.DockerConfigJsonKey: k8sSecret.Data[corev1.DockerConfigJsonKey],
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}, nil
}

// validateImageHasDigestedPullspec validates if the given pullspec has a SHA256 digest.
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
