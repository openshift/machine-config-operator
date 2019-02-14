package main

import (
	"context"
	"flag"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/pkg/operator"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/leaderelection"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine Config Operator",
		Long:  "",
		Run:   runStartCmd,
	}

	startOpts struct {
		kubeconfig string
		imagesFile string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
	startCmd.PersistentFlags().StringVar(&startOpts.imagesFile, "images-json", "", "images.json file for MCO.")
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	if startOpts.imagesFile == "" {
		glog.Fatal("--images-json cannot be empty")
	}

	cb, err := common.NewClientBuilder(startOpts.kubeconfig)
	if err != nil {
		glog.Fatalf("error creating clients: %v", err)
	}
	run := func(ctx context.Context) {
		ctrlctx := common.CreateControllerContext(cb, ctx.Done(), componentNamespace)
		if err := startControllers(ctrlctx); err != nil {
			glog.Fatalf("error starting controllers: %v", err)
		}

		ctrlctx.NamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.APIExtInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		close(ctrlctx.InformersStarted)

		select {}
	}

	leaderelection.RunOrDie(context.TODO(), leaderelection.LeaderElectionConfig{
		Lock:          common.CreateResourceLock(cb, componentNamespace, componentName),
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
	go operator.New(
		componentNamespace, componentName,
		startOpts.imagesFile,
		ctx.NamespacedInformerFactory.Machineconfiguration().V1().MCOConfigs(),
		ctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigPools(),
		ctx.NamespacedInformerFactory.Machineconfiguration().V1().ControllerConfigs(),
		ctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigs(),
		ctx.NamespacedInformerFactory.Machineconfiguration().V1().ControllerConfigs(),
		ctx.KubeNamespacedInformerFactory.Core().V1().ServiceAccounts(),
		ctx.APIExtInformerFactory.Apiextensions().V1beta1().CustomResourceDefinitions(),
		ctx.KubeNamespacedInformerFactory.Apps().V1().Deployments(),
		ctx.KubeNamespacedInformerFactory.Apps().V1().DaemonSets(),
		ctx.KubeNamespacedInformerFactory.Rbac().V1().ClusterRoles(),
		ctx.KubeNamespacedInformerFactory.Rbac().V1().ClusterRoleBindings(),
		ctx.KubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
		ctx.ConfigInformerFactory.Config().V1().Infrastructures(),
		ctx.ConfigInformerFactory.Config().V1().Networks(),
		ctx.ClientBuilder.MachineConfigClientOrDie(componentName),
		ctx.ClientBuilder.KubeClientOrDie(componentName),
		ctx.ClientBuilder.APIExtClientOrDie(componentName),
		ctx.ClientBuilder.ConfigClientOrDie(componentName),
	).Run(2, ctx.Stop)

	return nil
}
