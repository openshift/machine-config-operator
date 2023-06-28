package main

import (
	"context"
	"fmt"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/klog/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"strings"
)

// Copies the global pull secret from openshift-config/pull-secret into the MCO
// namespace so that it can be used by the custom build pod.
func copyGlobalPullSecret(ctx context.Context, cb *clients.Builder) error {
	kubeclient := cb.KubeClientOrDie(componentName)

	globalPullSecret, err := kubeclient.CoreV1().Secrets("openshift-config").Get(ctx, "pull-secret", metav1.GetOptions{})
	if err != nil {
		return err
	}

	secretCopy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "global-pull-secret-copy",
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: globalPullSecret.Data,
		Type: globalPullSecret.Type,
	}

	_, err = kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(ctx, secretCopy, metav1.CreateOptions{})
	return ignoreAlreadyExistsErr(err)
}

// Creates an OS ImageStream within the MCO namespace to push the final OS image to.
func createOSImagestream(ctx context.Context, cb *clients.Builder) error {
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "os-image",
			Namespace: ctrlcommon.MCONamespace,
		},
		Spec: imagev1.ImageStreamSpec{},
	}

	_, err := cb.ImageClientOrDie(componentName).ImageV1().ImageStreams(ctrlcommon.MCONamespace).Create(ctx, is, metav1.CreateOptions{})
	return ignoreAlreadyExistsErr(err)
}

// Gets the secret name for a secret within the MCO namespace that can push to
// the os-image ImageStream.
func getBuilderPushSecretName(ctx context.Context, cb *clients.Builder) (string, error) {
	secrets, err := cb.KubeClientOrDie(componentName).CoreV1().Secrets(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})

	if err != nil {
		return "", err
	}

	names := []string{}
	for _, secret := range secrets.Items {
		names = append(names, secret.Name)
		if strings.HasPrefix(secret.Name, "builder-dockercfg") {
			return secret.Name, nil
		}
	}

	return "", fmt.Errorf("builder push secret name not found, found: %v", names)
}

// Determines if we should set up the defaults based upon our CLI flags.
func getOrCreateBuildControllerConfigMap(ctx context.Context, cb *clients.Builder) (*corev1.ConfigMap, error) {
	cmName := fmt.Sprintf("%s/%s", ctrlcommon.MCONamespace, onClusterBuildConfigMapName)

	kubeclient := cb.KubeClientOrDie(componentName)

	onClusterBuildConfigMap, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, onClusterBuildConfigMapName, metav1.GetOptions{})
	if err == nil {
		klog.Infof("ConfigMap %s found, will use", cmName)
		return onClusterBuildConfigMap, nil
	}

	// If this isn't an IsNotFound error, return it to the caller to deal with.
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	// Below this point, it is assumed that we have an IsNotFound error.

	// If we've opted to create defaults, let's do that now.
	if startOpts.createDefaults {
		klog.Infof("ConfigMap %q not found and --create-defaults set, will attempt to create / set defaults:", cmName)
		return createDefaults(ctx, cb)
	}

	// If we've opted not to create defaults, return the noopErr which we look
	// for later so we can enter the no-op loop.
	return nil, noopErr
}

// Copies the global pull secret into the MCO namespace, creates an
// ImageStream, identifies the builder push secret, and creates a ConfigMap
// with all of these default values so that the BuildController can function
// out of the box.
func createDefaults(ctx context.Context, cb *clients.Builder) (*corev1.ConfigMap, error) {
	// Copy the global pull secret into the MCO namespace.
	if err := copyGlobalPullSecret(ctx, cb); err != nil {
		return nil, err
	}
	klog.Infof("Copied global pull secret into MCO namespace")

	// Create the OS image ImageStream in the MCO namespace.
	if err := createOSImagestream(ctx, cb); err != nil {
		return nil, err
	}
	klog.Info("Created 'os-image' ImageStream in MCO namespace")

	// Get the builder push secret name from the MCO namespace.
	builderPushSecretName, err := getBuilderPushSecretName(ctx, cb)
	if err != nil {
		return nil, err
	}

	klog.Infof("Using %q as image push secret for 'os-image' ImageStream", builderPushSecretName)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      onClusterBuildConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"baseImagePullSecretName":    "global-pull-secret-copy",
			"finalImagePushSecretName":   builderPushSecretName,
			"finalImagePullspec":         "image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-image",
			imageBuilderTypeConfigMapKey: customPodImageBuilder,
		},
	}

	kubeclient := cb.KubeClientOrDie(componentName)

	cm, err = kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		return cm, err
	}

	if apierrors.IsAlreadyExists(err) {
		return kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, cm.Name, metav1.GetOptions{})
	}

	return nil, err
}

// Returns nil if we've encountered an already exists error from the Kube API.
func ignoreAlreadyExistsErr(err error) error {
	if err == nil {
		return nil
	}

	if apierrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}
