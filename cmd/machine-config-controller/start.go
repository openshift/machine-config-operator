package main

import (
	"context"
	"flag"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	containerruntimeconfig "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	kubeletconfig "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine Config Controller",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig string
		templates  string

		resourceLockNamespace string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.resourceLockNamespace, "resourcelock-namespace", metav1.NamespaceSystem, "Path to the template files used for creating MachineConfig objects")
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	stop := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		ctrlcommon.WriteTerminationError(errors.Wrapf(err, "Creating clients"))
	}
	run := func(ctx context.Context) {
		ctrlctx := ctrlcommon.CreateControllerContext(cb, ctx.Done(), componentName)

		controllers := createControllers(ctrlctx)

		// Start the shared factory informers that you need to use in your controller
		ctrlctx.InformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)

		close(ctrlctx.InformersStarted)

		trustedCAWatcher, err := common.NewTrustedCAWatcher()
		if err != nil {
			glog.Errorf("Failed to watch trusted CA: %#v", err)
			ctrlcommon.WriteTerminationError(err)
		}
		defer trustedCAWatcher.Close()
		go trustedCAWatcher.Run(stop)

		for _, c := range controllers {
			go c.Run(2, ctrlctx.Stop)
		}

		select {
		case <-ctrlctx.Stop:
		case <-stop:
			cancel()
		}
	}

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:          common.CreateResourceLock(cb, startOpts.resourceLockNamespace, componentName),
		LeaseDuration: common.LeaseDuration,
		RenewDeadline: common.RenewDeadline,
		RetryPeriod:   common.RetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {
				glog.Warningf("Stop leading")
			},
		},
	})
	glog.Infof("Shutting down MCC")
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
			ctx.ClientBuilder.KubeClientOrDie("kubelet-config-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("kubelet-config-controller"),
		),
		containerruntimeconfig.New(
			rootOpts.templates,
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ContainerRuntimeConfigs(),
			ctx.ConfigInformerFactory.Config().V1().Images(),
			ctx.OperatorInformerFactory.Operator().V1alpha1().ImageContentSourcePolicies(),
			ctx.ConfigInformerFactory.Config().V1().ClusterVersions(),
			ctx.ClientBuilder.KubeClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.ConfigClientOrDie("container-runtime-config-controller"),
		),
		// The renderer creates "rendered" MCs from the MC fragments generated by
		// the above sub-controllers, which are then consumed by the node controller
		render.New(
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.ClientBuilder.KubeClientOrDie("render-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("render-controller"),
		),
		// The node controller consumes data written by the above
		node.New(
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			ctx.ConfigInformerFactory.Config().V1().Schedulers(),
			ctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
		),
	)

	return controllers
}
