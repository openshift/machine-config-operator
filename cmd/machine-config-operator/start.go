package main

import (
	"context"
	"flag"
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/operator"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
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

type asyncResult struct {
	name  string
	error error
}

func runStartCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// This is the context that signals whether the operator should be running and doing work
	runContext, runCancel := context.WithCancel(context.Background())
	// This is the context that signals whether we should release our leader lease
	leaderContext, leaderCancel := context.WithCancel(context.Background())

	// So we can collect status of our goroutines
	resultChannel := make(chan asyncResult, 1)
	resultChannelCount := 0

	// To help debugging, immediately log version
	glog.Infof("Version: %s (Raw: %s, Hash: %s)", os.Getenv("RELEASE_VERSION"), version.Raw, version.Hash)

	if startOpts.imagesFile == "" {
		glog.Fatal("--images-json cannot be empty")
	}

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		glog.Fatalf("error creating clients: %v", err)
	}
	run := func(_ context.Context) {

		go common.SignalHandler(runCancel)

		ctrlctx := ctrlcommon.CreateControllerContext(cb, runContext.Done(), ctrlcommon.MCONamespace)
		controller := operator.New(
			ctrlcommon.MCONamespace, componentName,
			startOpts.imagesFile,
			ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().ServiceAccounts(),
			ctrlctx.APIExtInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
			ctrlctx.KubeNamespacedInformerFactory.Apps().V1().Deployments(),
			ctrlctx.KubeNamespacedInformerFactory.Apps().V1().DaemonSets(),
			ctrlctx.KubeNamespacedInformerFactory.Rbac().V1().ClusterRoles(),
			ctrlctx.KubeNamespacedInformerFactory.Rbac().V1().ClusterRoleBindings(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
			ctrlctx.KubeInformerFactory.Core().V1().ConfigMaps(),
			ctrlctx.ConfigInformerFactory.Config().V1().Infrastructures(),
			ctrlctx.ConfigInformerFactory.Config().V1().Networks(),
			ctrlctx.ConfigInformerFactory.Config().V1().Proxies(),
			ctrlctx.ConfigInformerFactory.Config().V1().DNSes(),
			ctrlctx.ClientBuilder.MachineConfigClientOrDie(componentName),
			ctrlctx.ClientBuilder.KubeClientOrDie(componentName),
			ctrlctx.ClientBuilder.APIExtClientOrDie(componentName),
			ctrlctx.ClientBuilder.ConfigClientOrDie(componentName),
			ctrlctx.OpenShiftKubeAPIServerKubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
			ctrlctx.KubeInformerFactory.Core().V1().Nodes(),
			ctrlctx.KubeMAOSharedInformer.Core().V1().Secrets(),
		)

		ctrlctx.NamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.APIExtInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftKubeAPIServerKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeMAOSharedInformer.Start(ctrlctx.Stop)
		close(ctrlctx.InformersStarted)

		resultChannelCount++
		go func() {
			defer utilruntime.HandleCrash()
			controller.Run(runContext, 2)
			resultChannel <- asyncResult{name: "main operator", error: err}
		}()

		// TODO(jkyros); This might be overkill for the operator, it only has one goroutine
		var shutdownTimer *time.Timer
		for resultChannelCount > 0 {
			glog.Infof("Waiting on %d outstanding goroutines.", resultChannelCount)
			if shutdownTimer == nil { // running
				select {
				case <-runContext.Done():
					glog.Info("Run context completed; beginning two-minute graceful shutdown period.")
					shutdownTimer = time.NewTimer(2 * time.Minute)

				case result := <-resultChannel:
					// TODO(jkyros): one of our goroutines puked early, this means we shut down everything.
					resultChannelCount--
					if result.error == nil {
						glog.Infof("Collected %s goroutine.", result.name)
					} else {
						glog.Errorf("Collected %s goroutine: %v", result.name, result.error)
						runCancel() // this will cause shutdownTimer initialization in the next loop
					}
				}
			} else { // shutting down
				select {
				case <-shutdownTimer.C: // never triggers after the channel is stopped, although it would not matter much if it did because subsequent cancel calls do nothing.
					leaderCancel()
					shutdownTimer.Stop()
				case result := <-resultChannel:
					resultChannelCount--
					if result.error == nil {
						glog.Infof("Collected %s goroutine.", result.name)
					} else {
						glog.Errorf("Collected %s goroutine: %v", result.name, result.error)
					}
					if resultChannelCount == 0 {
						glog.Info("That was the last one, cancelling the leader lease.")
						leaderCancel()
					}
				}
			}
		}
		glog.Info("Finished collecting operator goroutines.")
	}

	// TODO(jkyros): should this be a different "pre-run" context here?
	leaderElectionCfg := common.GetLeaderElectionConfig(runContext, cb.GetBuilderConfig())

	leaderelection.RunOrDie(leaderContext, leaderelection.LeaderElectionConfig{
		Lock:            common.CreateResourceLock(cb, ctrlcommon.MCONamespace, componentName),
		ReleaseOnCancel: true,
		LeaseDuration:   leaderElectionCfg.LeaseDuration.Duration,
		RenewDeadline:   leaderElectionCfg.RenewDeadline.Duration,
		RetryPeriod:     leaderElectionCfg.RetryPeriod.Duration,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {
				glog.Infof("Stopped leading. Terminating.")
				os.Exit(0)
			},
		},
	})
	panic("unreachable")
}
