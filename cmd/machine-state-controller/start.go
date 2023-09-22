package main

import (
	"context"
	"os"

	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/state"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/klog/v2"
)

var (
	startCmd = &cobra.Command{
		Use:   "start",
		Short: "Starts Machine State Controller",
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

func runStartCmd(_ *cobra.Command, _ []string) {

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		klog.Fatalf("error creating clients: %v", err)
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	runContext, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	run := func(ctx context.Context) {
		go common.SignalHandler(runCancel)

		ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb, ctrlcommon.MCONamespace)

		ctrl := state.New(ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigStates(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().Events(),
			ctrlctx.KubeInformerFactory.Core().V1().Nodes(),
			state.StateControllerConfig{}, ctrlctx.ClientBuilder.KubeClientOrDie(componentName),
			ctrlctx.ClientBuilder.MachineConfigClientOrDie(componentName),
		)

		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.NamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.InformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeMAOSharedInformer.Start(ctrlctx.Stop)

		close(ctrlctx.InformersStarted)

		go ctrl.Run(2, runContext, ctrlctx.Stop, nil)

		<-ctx.Done()

	}

	leaderElectionCfg := common.GetLeaderElectionConfig(cb.GetBuilderConfig())

	leaderelection.RunOrDie(runContext, leaderelection.LeaderElectionConfig{
		Lock:            common.CreateResourceLock(cb, ctrlcommon.MCONamespace, componentName),
		ReleaseOnCancel: true,
		LeaseDuration:   leaderElectionCfg.LeaseDuration.Duration,
		RenewDeadline:   leaderElectionCfg.RenewDeadline.Duration,
		RetryPeriod:     leaderElectionCfg.RetryPeriod.Duration,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {
				klog.Info("Stopped leading. Terminating.")
				os.Exit(0)
			},
		},
	})
	panic("unreachable")
}
