package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/openshift/machine-config-operator/internal/clients"
	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/leaderelection"
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
		kubeconfig string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
}

// Checks if the on-cluster-build-config ConfigMap exists. If it exists, return the ConfigMap.
// If not, return an error.
func getBuildControllerConfigMap(ctx context.Context, cb *clients.Builder) (*corev1.ConfigMap, error) {
	kubeclient := cb.KubeClientOrDie(componentName)
	cmName := build.OnClusterBuildConfigMapName
	cm, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, cmName, metav1.GetOptions{})

	if err != nil && apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("configmap %s does not exist. Please create it before opting into on-cluster builds", cmName)
	}

	if err != nil {
		return nil, err
	}

	return cm, nil
}

// Creates a new BuildController configured for a certain image builder based
// upon the imageBuilderType key in the on-cluster-build-config ConfigMap.
func getBuildController(ctx context.Context, cb *clients.Builder) (*build.Controller, error) {
	onClusterBuildConfigMap, err := getBuildControllerConfigMap(ctx, cb)
	if err != nil {
		return nil, err
	}

	imageBuilderType, err := build.GetImageBuilderType(onClusterBuildConfigMap)
	if err != nil {
		return nil, err
	}

	ctrlCtx := ctrlcommon.CreateControllerContext(ctx, cb)
	buildClients := build.NewClientsFromControllerContext(ctrlCtx)
	cfg := build.DefaultBuildControllerConfig()

	if imageBuilderType == build.OpenshiftImageBuilder {
		return build.NewWithImageBuilder(cfg, buildClients), nil
	}

	return build.NewWithCustomPodBuilder(cfg, buildClients), nil
}

func runStartCmd(_ *cobra.Command, _ []string) {
	flag.Set("v", "4")
	flag.Set("logtostderr", "true")
	flag.Parse()

	klog.V(2).Infof("Options parsed: %+v", startOpts)

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	// This is the 'main' context that we thread through the build controller context and
	// the leader elections. Cancelling this is "stop everything, we are shutting down".
	ctx, cancel := context.WithCancel(context.Background())

	cb, err := clients.NewBuilder("")
	if err != nil {
		klog.Fatalln(err)
	}

	run := func(ctx context.Context) {
		go common.SignalHandler(cancel)

		ctrl, err := getBuildController(ctx, cb)
		if err != nil {
			klog.Fatalln(err)
			var invalidImageBuiler *build.ErrInvalidImageBuilder
			if errors.As(err, &invalidImageBuiler) {
				klog.Errorf("The user passed an invalid imageBuilderType of %s", invalidImageBuiler.InvalidType)
				cancel()
				os.Exit(255)
			}
		}
		go ctrl.Run(ctx, 5)
		<-ctx.Done()
		cancel()
	}

	leaderElectionCfg := common.GetLeaderElectionConfig(cb.GetBuilderConfig())

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            common.CreateResourceLock(cb, ctrlcommon.MCONamespace, componentName),
		ReleaseOnCancel: true,
		LeaseDuration:   leaderElectionCfg.LeaseDuration.Duration,
		RenewDeadline:   leaderElectionCfg.RenewDeadline.Duration,
		RetryPeriod:     leaderElectionCfg.RetryPeriod.Duration,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {
				klog.Infof("Stopped leading. MOB terminating.")
				os.Exit(0)
			},
		},
	})
	panic("unreachable")
}
