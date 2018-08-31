package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
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
	glog.Infof("Version: %+v", version.Version)

	cb, err := common.NewClientBuilder(startOpts.kubeconfig)
	if err != nil {
		glog.Fatalf("error creating clients: %v", err)
	}
	stopCh := make(chan struct{})
	run := func(stop <-chan struct{}) {

		ctx := common.CreateControllerContext(cb, stopCh, componentName)
		if err := startControllers(ctx); err != nil {
			glog.Fatalf("error starting controllers: %v", err)
		}

		ctx.InformerFactory.Start(ctx.Stop)
		ctx.KubeInformerFactory.Start(ctx.Stop)
		close(ctx.InformersStarted)
		close(ctx.KubeInformersStarted)

		select {}
	}

	leaderelection.RunOrDie(leaderelection.LeaderElectionConfig{
		Lock:          common.CreateResourceLock(cb, startOpts.resourceLockNamespace, componentName),
		LeaseDuration: common.LeaseDuration,
		RenewDeadline: common.RenewDeadline,
		RetryPeriod:   common.RetryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {
				glog.Fatalf("leaderelection lost")
			},
		},
	})
	panic("unreachable")
}

func startControllers(ctx *common.ControllerContext) error {
	go template.New(
		rootOpts.templates,
		ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
		ctx.ClientBuilder.KubeClientOrDie("template-controller"),
		ctx.ClientBuilder.MachineConfigClientOrDie("template-controller"),
	).Run(2, ctx.Stop)

	go render.New(
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
		ctx.ClientBuilder.KubeClientOrDie("render-controller"),
		ctx.ClientBuilder.MachineConfigClientOrDie("render-controller"),
	).Run(2, ctx.Stop)

	go node.New(
		ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		ctx.KubeInformerFactory.Core().V1().Nodes(),
		ctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
		ctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
	).Run(2, ctx.Stop)

	return nil
}
