package main

import (
	"context"
	"flag"
	"fmt"

	features "github.com/openshift/api/features"
	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	certrotationcontroller "github.com/openshift/machine-config-operator/pkg/controller/certrotation"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	containerruntimeconfig "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	"github.com/openshift/machine-config-operator/pkg/controller/drain"
	kubeletconfig "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	machinesetbootimage "github.com/openshift/machine-config-operator/pkg/controller/machine-set-boot-image"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	"github.com/openshift/machine-config-operator/pkg/controller/pinnedimageset"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine Config Controller",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig               string
		templates                string
		promMetricsListenAddress string
		resourceLockNamespace    string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.resourceLockNamespace, "resourcelock-namespace", metav1.NamespaceSystem, "Path to the template files used for creating MachineConfig objects")
	startCmd.PersistentFlags().StringVar(&startOpts.promMetricsListenAddress, "metrics-listen-address", "127.0.0.1:8797", "Listen address for prometheus metrics listener")
}

func runStartCmd(_ *cobra.Command, _ []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// This is 'main' context that we thread through the controller context and
	// the leader elections. Cancelling this is "stop everything, we are shutting down".
	runContext, runCancel := context.WithCancel(context.Background())

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		ctrlcommon.WriteTerminationError(fmt.Errorf("creating clients: %w", err))
	}

	run := func(ctx context.Context) {
		go common.SignalHandler(runCancel)

		// Start the metrics handler

		ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb)

		go ctrlcommon.StartMetricsListener(startOpts.promMetricsListenAddress, ctrlctx.Stop, ctrlcommon.RegisterMCCMetrics)

		controllers := createControllers(ctrlctx)
		draincontroller := drain.New(
			drain.DefaultConfig(),
			ctrlctx.KubeInformerFactory.Core().V1().Nodes(),
			ctrlctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctrlctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
			ctrlctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
			ctrlctx.FeatureGatesHandler,
		)

		certrotationcontroller, err := certrotationcontroller.New(
			ctrlctx.ClientBuilder.KubeClientOrDie("cert-rotation-controller"),
			ctrlctx.ClientBuilder.ConfigClientOrDie("cert-rotation-controller"),
			ctrlctx.ClientBuilder.MachineClientOrDie("cert-rotation-controller"),
			ctrlctx.ClientBuilder.AROClientOrDie("cert-rotation-controller"),
			ctrlctx.KubeMAOSharedInformer.Core().V1().Secrets(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().Secrets(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
			ctrlctx.ConfigInformerFactory.Config().V1().Infrastructures(),
		)
		if err != nil {
			klog.Fatalf("unable to start cert rotation controller: %v", err)
		}

		// Start the shared factory informers that you need to use in your controller
		ctrlctx.InformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeMAOSharedInformer.Start(ctrlctx.Stop)
		ctrlctx.OCLInformerFactory.Start(ctrlctx.Stop)

		close(ctrlctx.InformersStarted)

		if fgErr := ctrlctx.FeatureGatesHandler.Connect(ctx); fgErr != nil {
			klog.Fatal(fmt.Errorf("failed to connect to feature gates %w", fgErr))
		}

		if ctrlctx.FeatureGatesHandler.Enabled(features.FeatureGatePinnedImages) && ctrlctx.FeatureGatesHandler.Enabled(features.FeatureGateMachineConfigNodes) {
			pinnedImageSet := pinnedimageset.New(
				ctrlctx.InformerFactory.Machineconfiguration().V1().PinnedImageSets(),
				ctrlctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
				ctrlctx.ClientBuilder.KubeClientOrDie("pinned-image-set-controller"),
				ctrlctx.ClientBuilder.MachineConfigClientOrDie("pinned-image-set-controller"),
			)

			go pinnedImageSet.Run(2, ctrlctx.Stop)
			// start the informers again to enable feature gated types.
			// see comments in SharedInformerFactory interface.
			ctrlctx.InformerFactory.Start(ctrlctx.Stop)
		}

		if ctrlcommon.IsBootImageControllerRequired(ctrlctx) {
			machineSetBootImage := machinesetbootimage.New(
				ctrlctx.ClientBuilder.KubeClientOrDie("machine-set-boot-image-controller"),
				ctrlctx.ClientBuilder.MachineClientOrDie("machine-set-boot-image-controller"),
				ctrlctx.KubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
				ctrlctx.MachineInformerFactory.Machine().V1beta1().MachineSets(),
				ctrlctx.ConfigInformerFactory.Config().V1().Infrastructures(),
				ctrlctx.ClientBuilder.OperatorClientOrDie(componentName),
				ctrlctx.OperatorInformerFactory.Operator().V1().MachineConfigurations(),
				ctrlctx.FeatureGatesHandler,
			)
			go machineSetBootImage.Run(ctrlctx.Stop)
			// start the informers again to enable feature gated types.
			// see comments in SharedInformerFactory interface.
			ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)
			ctrlctx.MachineInformerFactory.Start(ctrlctx.Stop)
			ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
			ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)
		}

		for _, c := range controllers {
			go c.Run(2, ctrlctx.Stop)
		}
		go draincontroller.Run(5, ctrlctx.Stop)
		go certrotationcontroller.Run(ctx, 1)

		// wait here in this function until the context gets cancelled (which tells us when we are being shut down)
		<-ctx.Done()
	}

	common.DoLeaderElectionAndRunOrDie(runContext, &common.RunOpts{
		Namespace:     startOpts.resourceLockNamespace,
		ComponentName: componentName,
		Builder:       cb,
		OnStart:       run,
	})
}

func createControllers(ctx *ctrlcommon.ControllerContext) []ctrlcommon.Controller {

	var controllers []ctrlcommon.Controller
	controllers = append(controllers,
		// Our primary MCs come from here
		template.New(
			rootOpts.templates,
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.OpenShiftConfigKubeNamespacedInformerFactory.Core().V1().Secrets(),
			ctx.ConfigInformerFactory.Config().V1().APIServers(),
			ctx.ClientBuilder.KubeClientOrDie("template-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("template-controller"),
		),
		// Add all "sub-renderers here"
		kubeletconfig.New(
			rootOpts.templates,
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().KubeletConfigs(),
			ctx.ConfigInformerFactory.Config().V1().FeatureGates(),
			ctx.ConfigInformerFactory.Config().V1().Nodes(),
			ctx.ConfigInformerFactory.Config().V1().APIServers(),
			ctx.ClientBuilder.KubeClientOrDie("kubelet-config-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("kubelet-config-controller"),
			ctx.ClientBuilder.ConfigClientOrDie("kubelet-config-controller"),
			ctx.FeatureGatesHandler,
		),
		containerruntimeconfig.New(
			rootOpts.templates,
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ContainerRuntimeConfigs(),
			ctx.ConfigInformerFactory.Config().V1().Images(),
			ctx.ConfigInformerFactory.Config().V1().ImageDigestMirrorSets(),
			ctx.ConfigInformerFactory.Config().V1().ImageTagMirrorSets(),
			ctx.ConfigInformerFactory,
			ctx.OperatorInformerFactory.Operator().V1alpha1().ImageContentSourcePolicies(),
			ctx.ConfigInformerFactory.Config().V1().ClusterVersions(),
			ctx.ClientBuilder.KubeClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.ConfigClientOrDie("container-runtime-config-controller"),
			ctx.FeatureGatesHandler,
		),
		// The renderer creates "rendered" MCs from the MC fragments generated by
		// the above sub-controllers, which are then consumed by the node controller
		render.New(
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ContainerRuntimeConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().KubeletConfigs(),
			ctx.OperatorInformerFactory.Operator().V1().MachineConfigurations(),
			ctx.ClientBuilder.KubeClientOrDie("render-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("render-controller"),
			ctx.FeatureGatesHandler,
		),
		// The node controller consumes data written by the above
		node.New(
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			ctx.KubeInformerFactory.Core().V1().Pods(),
			ctx.OCLInformerFactory.Machineconfiguration().V1().MachineOSConfigs(),
			ctx.OCLInformerFactory.Machineconfiguration().V1().MachineOSBuilds(),
			ctx.ConfigInformerFactory.Config().V1().Schedulers(),
			ctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
			ctx.FeatureGatesHandler,
		),
	)

	return controllers
}
