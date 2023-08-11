package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ghodss/yaml"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

func createBaseImagePullSecret(cs *framework.ClientSet, path string) error {
	if path == "" {
		klog.Infof("No pull secret path provided, will clone global pull secret")
		return copyGlobalPullSecret(cs)
	}

	return createSecretFromFile(cs, path)
}

// Copies the global pull secret from openshift-config/pull-secret into the MCO
// namespace so that it can be used by the custom build pod.
func copyGlobalPullSecret(cs *framework.ClientSet) error {
	globalPullSecret, err := cs.CoreV1Interface.Secrets("openshift-config").Get(context.TODO(), "pull-secret", metav1.GetOptions{})
	if err != nil {
		return err
	}

	secretCopy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      globalPullSecretCloneName,
			Namespace: ctrlcommon.MCONamespace,
			Labels: map[string]string{
				createdByOnClusterBuildsHelper: "",
			},
		},
		Data: globalPullSecret.Data,
		Type: globalPullSecret.Type,
	}

	err = createSecret(cs, secretCopy)
	if err == nil {
		klog.Infof("Cloned global pull secret %q into namespace %q as %q", "pull-secret", ctrlcommon.MCONamespace, secretCopy.Name)
	}

	return nil
}

func createSecret(cs *framework.ClientSet, s *corev1.Secret) error {
	_, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), s, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Created secret %q in namespace %q", s.Name, ctrlcommon.MCONamespace)
		return nil
	}

	if err != nil && !apierrs.IsAlreadyExists(err) {
		return err
	}

	secret, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), s.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if !hasOurLabel(secret.Labels) {
		klog.Infof("Found preexisting user-supplied secret %q, using as-is.", s.Name)
		return nil
	}

	// Delete and recreate.
	klog.Infof("Secret %q was created by us, but could be out of date. Recreating...", s.Name)
	err = cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), s.Name, metav1.DeleteOptions{})
	if err != nil {
		return err
	}

	return createSecret(cs, s)
}

func getSecretNameFromFile(path string) (string, error) {
	secret, err := loadSecretFromFile(path)

	if err != nil {
		return "", err
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

	return createSecret(cs, secret)
}

func cleanupSecrets(cs *framework.ClientSet) error {
	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), getListOptsForOurLabel())

	if err != nil {
		return err
	}

	for _, secret := range secrets.Items {
		if err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secret.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}
		klog.Infof("Deleted secret %q from namespace %q", secret.Name, ctrlcommon.MCONamespace)
	}

	return nil
}

func forceCleanupSecrets(cs *framework.ClientSet) error {
	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})

	if err != nil {
		return err
	}

	toDelete := sets.NewString("global-pull-secret-copy")

	for _, secret := range secrets.Items {
		if strings.HasSuffix(secret.Name, "-canonical") {
			toDelete.Insert(secret.Name)
		}
	}

	for _, secret := range secrets.Items {
		if toDelete.Has(secret.Name) {
			if err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secret.Name, metav1.DeleteOptions{}); err != nil {
				return err
			}
			klog.Infof("Deleted secret %q from namespace %q", secret.Name, ctrlcommon.MCONamespace)
		}
	}

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
