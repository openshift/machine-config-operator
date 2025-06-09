package main

import (
	"context"
	"flag"
	"os"

	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"k8s.io/client-go/tools/leaderelection"

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
		kubeconfig string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringVar(&startOpts.kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster (testing only)")
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
	defer cancel()

	cb, err := clients.NewBuilder("")
	if err != nil {
		klog.Fatalln(err)
	}

	var shutdownChan <-chan struct{}

	run := func(ctx context.Context) {
		go common.SignalHandler(cancel)

		ctrlCtx := ctrlcommon.CreateControllerContext(ctx, cb)

		ctrl := build.NewOSBuildControllerFromControllerContext(ctrlCtx)

		// Wire up our shutdown channel.
		shutdownChan = ctrl.ShutdownChan()

		// This method blocks the current goroutine until the controller is shut
		// down. This means that we can perform a post-shutdown action after
		// shutdown has completed.
		ctrl.Run(ctx, 3)
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
				// Block this function until the controller shutdown is complete to
				// ensure that the controller both has enough time to finish its
				// cleanup as well as to terminate its lease.
				<-shutdownChan
				klog.Infof("Stopped leading; machine-os-builder terminating.")
				os.Exit(0)
			},
		},
	})

}
