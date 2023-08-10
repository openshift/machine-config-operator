package main

import (
	"context"
	"flag"
	"fmt"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/klog/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"strings"
)

var (
	createDefaultsCmd = &cobra.Command{
		Use:   "create-defaults",
		Short: "Creates defaults for Machine OS Builder and exits",
		Long:  "",
		Run:   runCreateDefaultsCmd,
	}
)

func runCreateDefaultsCmd(cmd *cobra.Command, args []string) {
	flag.Set("v", "4")
	flag.Set("logtostderr", "true")
	flag.Parse()

	klog.V(2).Infof("Options parsed: %+v", startOpts)

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cb, err := clients.NewBuilder("")
	if err != nil {
		klog.Fatalln(err)
	}

	startOpts.createDefaults = true
	if _, err := getOrCreateBuildControllerConfigMap(ctx, cb); err != nil {
		klog.Fatalln(err)
	}

	if _, err := getOrCreateDefaultCustomDockerfileConfigMap(ctx, cb); err != nil {
		klog.Fatalln(err)
	}
}

func init() {
	rootCmd.AddCommand(createDefaultsCmd)
}

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
			Name:      globalPullSecretCopyName,
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

// Gets or creates a configmap using a supplied creation function. Returns the
// created ConfigMap, a boolean (true if created, false if created), and an error.
func getOrCreateConfigMap(ctx context.Context, cb *clients.Builder, name string, createFunc func(context.Context, *clients.Builder) (*corev1.ConfigMap, error)) (*corev1.ConfigMap, bool, error) {
	cmName := fmt.Sprintf("%s/%s", ctrlcommon.MCONamespace, name)

	kubeclient := cb.KubeClientOrDie(componentName)

	cm, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		klog.Infof("ConfigMap %s found, will use", cmName)
		return cm, false, nil
	case err != nil && !apierrors.IsNotFound(err):
		return nil, false, err
	case !startOpts.createDefaults:
		return nil, false, noopErr
	case createFunc == nil:
		return nil, false, fmt.Errorf("missing default ConfigMap creation function for %s", name)
	}

	// At this point, we can assume that we couldn't find the ConfigMap, we want
	// to create a default ConfigMap, and we're able to do so (i.e., we have a
	// creation function). So now we delegate to the creation function.
	cm, err = createFunc(ctx, cb)
	if err != nil {
		return nil, false, err
	}

	klog.Infof("ConfigMap %q not found and --create-defaults is set. Defaults created.", cmName)
	return cm, true, nil
}

// Gets or creates the on-cluster-build-config ConfigMap, depending upon the CLI flags set.
func getOrCreateBuildControllerConfigMap(ctx context.Context, cb *clients.Builder) (*corev1.ConfigMap, error) {
	cm, created, err := getOrCreateConfigMap(ctx, cb, onClusterBuildConfigMapName, createDefaultOnClusterBuildConfigMap)
	if err != nil {
		return nil, err
	}

	if !startOpts.copyGlobalPullSecret {
		return cm, nil
	}

	// The supplied creation func already copies the global pull secret into
	// place when it runs. So if the ConfigMap was created on-demand, the global
	// pull secret already exists and the ConfigMap reflects that.
	if created {
		return cm, nil
	}

	if err := copyGlobalPullSecret(ctx, cb); err != nil {
		return nil, err
	}

	// The ConfigMap already references the global pull secret copy. Nothing else to do here.
	if cm.Data[finalImagePushSecretNameConfigKey] == globalPullSecretCopyName {
		return cm, nil
	}

	// Update the ConfigMap to reflect that we've copied the global pull secret.
	kubeclient := cb.KubeClientOrDie(componentName)
	cm.Data[finalImagePushSecretNameConfigKey] = globalPullSecretCopyName
	return kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Update(ctx, cm, metav1.UpdateOptions{})
}

func createDefaultCustomDockerfileConfigMap(ctx context.Context, cb *clients.Builder) (*corev1.ConfigMap, error) {
	mcfgclient := cb.MachineConfigClientOrDie(componentName)
	kubeclient := cb.KubeClientOrDie(componentName)

	mcpList, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	poolToCustomDockerfile := map[string]string{}
	for _, mcp := range mcpList.Items {
		poolToCustomDockerfile[mcp.Name] = ""
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      customDockerfileConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: poolToCustomDockerfile,
	}

	return kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(ctx, cm, metav1.CreateOptions{})
}

func getOrCreateDefaultCustomDockerfileConfigMap(ctx context.Context, cb *clients.Builder) (*corev1.ConfigMap, error) {
	cm, _, err := getOrCreateConfigMap(ctx, cb, customDockerfileConfigMapName, createDefaultCustomDockerfileConfigMap)
	return cm, err
}

// Copies the global pull secret into the MCO namespace, creates an
// ImageStream, identifies the builder push secret, and creates a ConfigMap
// with all of these default values so that the BuildController can function
// out of the box.
func createDefaultOnClusterBuildConfigMap(ctx context.Context, cb *clients.Builder) (*corev1.ConfigMap, error) {
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

	kubeclient := cb.KubeClientOrDie(componentName)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      onClusterBuildConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"baseImagePullSecretName":    "global-pull-secret-copy",
			"finalImagePushSecretName":   builderPushSecretName,
			"finalImagePullspec":         "image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/os-image",
			imageBuilderTypeConfigMapKey: openshiftImageBuilder,
		},
	}

	return kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(ctx, cm, metav1.CreateOptions{})
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
