package build

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/docker/reference"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	ctrlcommonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	k8stypes "k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
)

// Returns a selector with the appropriate labels for an OS build object label
// query.
func OSBuildSelector() labels.Selector {
	return labelsToSelector([]string{
		OnClusterLayeringLabelKey,
		RenderedMachineConfigLabelKey,
		TargetMachineConfigPoolLabelKey,
	})
}

// Returns a selector with the appropriate labels for an ephemeral build object
// label query.
func EphemeralBuildObjectSelector() labels.Selector {
	return labelsToSelector([]string{
		EphemeralBuildObjectLabelKey,
		OnClusterLayeringLabelKey,
		RenderedMachineConfigLabelKey,
		TargetMachineConfigPoolLabelKey,
	})
}

// Returns a selector with the appropriate labels for a canonicalized secret
// label query.
func CanonicalizedSecretSelector() labels.Selector {
	return labelsToSelector([]string{
		CanonicalSecretLabelKey,
		OriginalSecretNameLabelKey,
		OnClusterLayeringLabelKey,
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

// Determines if a secret has been canonicalized by us by checking both for the
// suffix as well as the labels that we add to the canonicalized secret.
func isCanonicalizedSecret(secret *corev1.Secret) bool {
	return hasCanonicalizedSecretLabels(secret) && strings.HasSuffix(secret.Name, canonicalSecretSuffix)
}

// Determines if a secret has our canonicalized secret label.
func hasCanonicalizedSecretLabels(secret *corev1.Secret) bool {
	return CanonicalizedSecretSelector().Matches(labels.Set(secret.Labels))
}

// Validates whether a secret is canonicalized. Returns an error if not.
func validateCanonicalizedSecret(secret *corev1.Secret) error {
	if !isCanonicalizedSecret(secret) {
		return fmt.Errorf("secret %q is not canonicalized", secret.Name)
	}

	return nil
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

	canonicalizedSecretBytes, canonicalized, err := ctrlcommon.ConvertSecretToDockerconfigJSON(secretBytes)
	if err != nil {
		return nil, err
	}

	if !canonicalized {
		return secret, nil
	}

	return newCanonicalSecret(secret, canonicalizedSecretBytes), nil
}

// Creates a new canonicalized secret with the appropriate suffix, labels, etc.
// Does *not* validate whether the inputted secret bytes are in the correct
// format.
func newCanonicalSecret(secret *corev1.Secret, secretBytes []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s%s", secret.Name, canonicalSecretSuffix),
			Namespace: secret.Namespace,
			Labels: map[string]string{
				CanonicalSecretLabelKey:    "",
				OriginalSecretNameLabelKey: secret.Name,
				OnClusterLayeringLabelKey:  "",
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
		return kubeclient.CoreV1().Secrets(ctrlcommonconsts.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
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
		return secretLister.Secrets(ctrlcommonconsts.MCONamespace).Get(name)
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

	if _, err := getPullSecretKey(secret); err != nil {
		return err
	}

	return nil
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

	if _, ok := obj.(*mcfgv1alpha1.MachineOSBuild); ok {
		return true
	}

	return false
}
