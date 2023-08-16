package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/openshift/machine-config-operator/internal/clients"
	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine OS Builder",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig           string
		createDefaults       bool
		copyGlobalPullSecret bool
	}

	errFoo = fmt.Errorf("configmap not found, will no-op")
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().BoolVar(&startOpts.createDefaults, "create-defaults", false, "Create default values for machine-os-builder")
	startCmd.PersistentFlags().BoolVar(&startOpts.copyGlobalPullSecret, "copy-global-pull-secret", false, "Copy the global pull secret into the MCO namespace")
}

// Checks if the on-cluster-build-config ConfigMap exists. If it exists, return the ConfigMap.
// If not, return an error.
func getBuildControllerConfigMap(ctx context.Context, cb *clients.Builder) (*corev1.ConfigMap, error) {
	kubeclient := cb.KubeClientOrDie(componentName)
	cmName := build.OnClusterBuildConfigMapName
	cm, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, cmName, metav1.GetOptions{})

	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("configmap %s does not exist. Please create it before opting into on-cluster builds", cmName)
		}
		return nil, err
	}

	return cm, nil
}

// Determines which image builder to start based upon the imageBuilderType key
// in the on-cluster-build-config ConfigMap. Defaults to custom-pod-builder.
func getImageBuilderType(cm *corev1.ConfigMap) string {
	configMapImageBuilder, ok := cm.Data[build.ImageBuilderTypeConfigMapKey]
	defaultBuilder := build.OpenshiftImageBuilder

	if !ok {
		klog.Infof("%s not set, defaulting to %q", build.ImageBuilderTypeConfigMapKey, defaultBuilder)
		return defaultBuilder
	}

	if ok && configMapImageBuilder == "" {
		klog.Infof("%s empty, defaulting to %q", build.ImageBuilderTypeConfigMapKey, defaultBuilder)
		return defaultBuilder
	}

	klog.Infof("%s set to %q", build.ImageBuilderTypeConfigMapKey, configMapImageBuilder)
	return configMapImageBuilder
}

// Creates a new BuildController configured for a certain image builder based
// upon the imageBuilderType key in the on-cluster-build-config ConfigMap.
// Defaults to the custom pod builder.
func getController(ctx context.Context, cb *clients.Builder) (*build.Controller, error) {

	onClusterBuildConfigMap, err := getBuildControllerConfigMap(ctx, cb)
	if err != nil {
		return nil, err
	}

	imageBuilderType := getImageBuilderType(onClusterBuildConfigMap)
	if imageBuilderType != build.CustomPodImageBuilder && imageBuilderType != build.OpenshiftImageBuilder {
		return nil, fmt.Errorf("invalid image type")
	}

	ctrlCtx := ctrlcommon.CreateControllerContext(ctx, cb, componentName)
	buildClients := build.NewClientsFromControllerContext(ctrlCtx)
	cfg := build.DefaultBuildControllerConfig()

	if imageBuilderType == build.OpenshiftImageBuilder {
		return build.NewWithImageBuilder(cfg, buildClients), nil
	}

	return build.NewWithCustomPodBuilder(cfg, buildClients), nil
}

// Starts the controller in a separate Goroutine, but blocks until the supplied
// context is canceled or done.
func startController(ctx context.Context, ctrl *build.Controller) {
	go ctrl.Run(ctx, 5)
	<-ctx.Done()
}

func runStartCmd(_ *cobra.Command, _ []string) {
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

	ctrl, err := getController(ctx, cb)
	if err != nil {
		klog.Fatalln(err)
	}

	startController(ctx, ctrl)
}
