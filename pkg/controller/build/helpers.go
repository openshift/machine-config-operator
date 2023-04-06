package build

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/containers/image/v5/docker/reference"
	imagetypes "github.com/containers/image/v5/types"
	"github.com/opencontainers/go-digest"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

const (
	canonicalSecretSuffix string = "-canonical"
)

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

// Parses the output of `$ skopeo inspect
// docker://registry.hostname/org/repo:latest` into a struct to get the image
// pullspec and digest.
func parseSkopeoOutputIntoImagePullspec(skopeoBytes []byte) (string, error) {
	// Copy / pasta'ed from: https://github.com/containers/skopeo/blob/main/cmd/skopeo/inspect/output.go
	type skopeoOutput struct {
		Name          string `json:",omitempty"`
		Tag           string `json:",omitempty"`
		Digest        digest.Digest
		RepoTags      []string
		Created       *time.Time
		DockerVersion string
		Labels        map[string]string
		Architecture  string
		Os            string
		Layers        []string
		LayersData    []imagetypes.ImageInspectLayer
		Env           []string
	}

	out := &skopeoOutput{}

	if err := json.Unmarshal(skopeoBytes, out); err != nil {
		return "", err
	}

	return parseImagePullspecWithDigest(out.Name, out.Digest)
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
func parseImagePullspec(pullspec, imageSHA string) (string, error) {
	imageDigest, err := digest.Parse(imageSHA)
	if err != nil {
		return "", err
	}

	return parseImagePullspecWithDigest(pullspec, imageDigest)
}

// Converts a legacy Docker pull secret into a more modern representation.
// Essentially, it converts {"registry.hostname.com": {"username": "user"...}}
// into {"auths": {"registry.hostname.com": {"username": "user"...}}}. If it
// encounters a pull secret already in this configuration, it will return the
// input secret as-is. Returns either the supplied data or the newly-configured
// representation of said data, a boolean to indicate whether it was converted,
// and any errors resulting from the conversion process.
func canonicalizePullSecretBytes(secretBytes []byte) ([]byte, bool, error) {
	type newStyleAuth struct {
		Auths map[string]interface{} `json:"auths,omitempty"`
	}

	// Try marshaling the new-style secret first:
	newStyleDecoded := &newStyleAuth{}
	if err := json.Unmarshal(secretBytes, newStyleDecoded); err != nil {
		return nil, false, fmt.Errorf("could not decode new-style pull secret: %w", err)
	}

	// We have an new-style secret, so we can just return here.
	if len(newStyleDecoded.Auths) != 0 {
		return secretBytes, false, nil
	}

	// We need to convert the legacy-style secret to the new-style.
	oldStyleDecoded := map[string]interface{}{}
	if err := json.Unmarshal(secretBytes, &oldStyleDecoded); err != nil {
		return nil, false, fmt.Errorf("could not decode legacy-style pull secret: %w", err)
	}

	out, err := json.Marshal(&newStyleAuth{
		Auths: oldStyleDecoded,
	})

	return out, err == nil, err
}

// Performs the above operation upon a given secret, potentially creating a new
// secret for insertion with the suffix '-canonical' on its name.
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

	canonicalizedSecretBytes, canonicalized, err := canonicalizePullSecretBytes(secretBytes)
	if err != nil {
		return nil, err
	}

	if !canonicalized {
		return secret, nil
	}

	out := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s%s", secret.Name, canonicalSecretSuffix),
			Namespace: secret.Namespace,
		},
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: canonicalizedSecretBytes,
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}

	return out, nil
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
