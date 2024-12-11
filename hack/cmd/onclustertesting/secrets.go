package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ghodss/yaml"
	"github.com/openshift/machine-config-operator/hack/internal/pkg/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/labels"
)

// Copies the global pull secret from openshift-config/pull-secret into the MCO
// namespace so that it can be used by the custom build pod.
func copyGlobalPullSecret(cs *framework.ClientSet) error {
	src := utils.SecretRef{
		Name:      "pull-secret",
		Namespace: "openshift-config",
	}

	dst := utils.SecretRef{
		Name:      globalPullSecretCloneName,
		Namespace: ctrlcommon.MCONamespace,
	}

	labels := map[string]string{
		createdByOnClusterBuildsHelper: "",
	}

	return utils.CloneSecretWithLabels(cs, src, dst, labels)
}

func copyEtcPkiEntitlementSecret(cs *framework.ClientSet) error {
	name := "etc-pki-entitlement"

	src := utils.SecretRef{
		Name:      name,
		Namespace: "openshift-config-managed",
	}

	dst := utils.SecretRef{
		Name:      name,
		Namespace: ctrlcommon.MCONamespace,
	}

	labels := map[string]string{
		createdByOnClusterBuildsHelper: "",
	}

	err := utils.CloneSecretWithLabels(cs, src, dst, labels)
	if apierrs.IsNotFound(err) {
		klog.Warningf("Secret %s not found, cannot copy", src.String())
		return nil
	}

	return fmt.Errorf("could not copy secret %s to %s: %w", src.String(), dst.String(), err)
}

func getSecretNameFromFile(path string) (string, error) {
	secret, err := loadSecretFromFile(path)

	if err != nil {
		return "", fmt.Errorf("could not get secret name from %q: %w", path, err)
	}

	return secret.Name, nil
}

func loadSecretFromFile(pushSecretPath string) (*corev1.Secret, error) {
	pushSecretBytes, err := os.ReadFile(pushSecretPath)
	if err != nil {
		return nil, err
	}

	secret := &corev1.Secret{}
	if err := yaml.Unmarshal(pushSecretBytes, &secret); err != nil {
		return nil, err
	}

	secret.Labels = map[string]string{
		createdByOnClusterBuildsHelper: "",
	}

	secret.Namespace = ctrlcommon.MCONamespace

	return secret, nil
}

func createSecretFromFile(cs *framework.ClientSet, path string) error {
	secret, err := loadSecretFromFile(path)
	if err != nil {
		return err
	}

	klog.Infof("Loaded secret %q from %s", secret.Name, path)
	return utils.CreateOrRecreateSecret(cs, secret)
}

func deleteSecret(cs *framework.ClientSet, name string) error {
	err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), name, metav1.DeleteOptions{})

	if err != nil {
		return fmt.Errorf("could not delete secret %s: %w", name, err)
	}

	klog.Infof("Deleted secret %q from namespace %q", name, ctrlcommon.MCONamespace)
	return nil
}

func getBuilderPushSecretName(cs *framework.ClientSet) (string, error) {
	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, secret := range secrets.Items {
		if strings.HasPrefix(secret.Name, "builder-dockercfg") {
			klog.Infof("Will use builder secret %q in namespace %q", secret.Name, ctrlcommon.MCONamespace)
			return secret.Name, nil
		}
	}

	return "", fmt.Errorf("could not find matching secret name in namespace %s", ctrlcommon.MCONamespace)
}

func getDefaultPullSecretName(cs *framework.ClientSet) (string, error) {
	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, secret := range secrets.Items {
		if strings.HasPrefix(secret.Name, "default-dockercfg") && !strings.Contains(secret.Name, "canonical") {
			klog.Infof("Will use default secret %q in namespace %q", secret.Name, ctrlcommon.MCONamespace)
			return secret.Name, nil
		}
	}

	return "", fmt.Errorf("could not find matching secret name in namespace %s", ctrlcommon.MCONamespace)
}

// TODO: Dedupe these funcs from BuildController helpers.
func validateSecret(cs *framework.ClientSet, secretName string) error {
	// Here we just validate the presence of the secret, and not its content
	secret, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), secretName, metav1.GetOptions{})
	if err != nil && apierrs.IsNotFound(err) {
		return fmt.Errorf("secret %q not found in namespace %q. Did you use the right secret name?", secretName, ctrlcommon.MCONamespace)
	}

	if err != nil {
		return fmt.Errorf("could not get secret %s: %w", secretName, err)
	}

	if _, err := getPullSecretKey(secret); err != nil {
		return err
	}

	return nil
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

func validateSecretsExist(cs *framework.ClientSet, names []string) error {
	for _, name := range names {
		if err := validateSecret(cs, name); err != nil {
			return err
		}
		klog.Infof("Secret %q exists in namespace %q", name, ctrlcommon.MCONamespace)
	}

	return nil
}

func createLongLivedImagePushSecretForPool(ctx context.Context, cs *framework.ClientSet, poolName string) (string, error) {
	opts := helpers.LongLivedSecretOpts{
		ServiceAccount: metav1.ObjectMeta{
			Name:      "builder",
			Namespace: ctrlcommon.MCONamespace,
		},
		Secret: metav1.ObjectMeta{
			Name:      fmt.Sprintf("ocl-%s-push-secret", poolName),
			Namespace: ctrlcommon.MCONamespace,
		},
		Lifetime: "24h",
	}

	if err := createLongLivedPullSecret(ctx, cs, opts); err != nil {
		return "", err
	}

	return opts.Secret.Name, nil
}

func createLongLivedPullSecret(ctx context.Context, cs *framework.ClientSet, opts helpers.LongLivedSecretOpts) error {
	secretLabels := map[string]string{
		createdByOnClusterBuildsHelper:                                   "",
		"machineconfiguration.openshift.io/long-lived-image-pull-secret": "",
	}

	secret, err := cs.CoreV1Interface.Secrets(opts.Secret.Namespace).Get(ctx, opts.Secret.Name, metav1.GetOptions{})
	if err != nil && !apierrs.IsNotFound(err) {
		return fmt.Errorf("could not look up secret %q: %w", opts.Secret.Name, err)
	}

	sel, err := labels.Set(secretLabels).AsValidatedSelector()
	if err != nil {
		return fmt.Errorf("could not validate selector labels: %w", err)
	}

	if secret != nil && sel.Matches(labels.Set(secret.Labels)) {
		klog.Infof("Found preexisting long-lived secret %q, reusing", opts.Secret.Name)
		return nil
	}

	if err := helpers.CreateLongLivedPullSecret(ctx, cs, opts); err != nil {
		return fmt.Errorf("could not create long-lived pull secret %s: %w", opts.Secret.Name, err)
	}

	secret, err = cs.CoreV1Interface.Secrets(opts.Secret.Namespace).Get(ctx, opts.Secret.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not fetch long-lived pull secret %s for labelling: %w", opts.Secret.Name, err)
	}

	for k, v := range secretLabels {
		metav1.SetMetaDataLabel(&secret.ObjectMeta, k, v)
	}

	_, err = cs.CoreV1Interface.Secrets(opts.Secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})

	if err == nil {
		klog.Infof("Created long-lived image pull secret %q", opts.Secret.Name)
	}

	return err
}
