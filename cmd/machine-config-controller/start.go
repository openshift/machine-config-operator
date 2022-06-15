package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	containerruntimeconfig "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	"github.com/openshift/machine-config-operator/pkg/controller/drain"
	kubeletconfig "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
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

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		ctrlcommon.WriteTerminationError(fmt.Errorf("creating clients: %w", err))
	}

	resultChannel := make(chan asyncResult, 1)
	resultChannelCount := 0

	// The context that gets passed into this function by the leaderelection stuff is the leaderContext, so we don't use it.
	// In the one case where we would have used it ( where we've lost a leaderelection for some reason) we really just need to insta-die
	// rather than cleanly shut down.
	run := func(_ context.Context) {

		// Set up the signal handler
		go common.SignalHandler(runCancel)

		ctrlctx := ctrlcommon.CreateControllerContext(cb, runContext.Done(), componentName)

		// Start the metrics handler
		resultChannelCount++
		go func() {
			defer utilruntime.HandleCrash()
			err := ctrlcommon.StartMetricsListener(startOpts.promMetricsListenAddress, ctrlctx.Stop)
			resultChannel <- asyncResult{name: "metrics handler", error: err}
		}()

		controllers := createControllers(ctrlctx)

		// Start the shared factory informers that you need to use in your controller
		ctrlctx.InformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)

		// Make sure the informers have started
		close(ctrlctx.InformersStarted)

		// Start the actual controllers
		for num := range controllers {
			resultChannelCount++
			glog.Infof("Starring %s controller", controllers[num].Name())
			// Closure, need to make sure we don't grab the loop reference
			controller := controllers[num]
			go func() {
				defer utilruntime.HandleCrash()
				if controller.Name() == "DrainController" {
					controller.Run(runContext, 5)
				} else {
					controller.Run(runContext, 2)
				}
				resultChannel <- asyncResult{name: controller.Name() + " controller", error: err}
			}()
		}

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

					}
				}
			}
		}
		leaderCancel()
		glog.Info("Finished collecting operator goroutines.")
	}

	leaderElectionCfg := common.GetLeaderElectionConfig(runContext, cb.GetBuilderConfig())

	leaderelection.RunOrDie(leaderContext, leaderelection.LeaderElectionConfig{
		Lock:            common.CreateResourceLock(cb, startOpts.resourceLockNamespace, componentName),
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

func createControllers(ctx *ctrlcommon.ControllerContext) []ctrlcommon.Controller {
	var controllers []ctrlcommon.Controller

	controllers = append(controllers,
		// Our primary MCs come from here
		template.New(
			rootOpts.templates,
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.OpenShiftConfigKubeNamespacedInformerFactory.Core().V1().Secrets(),
			ctx.ConfigInformerFactory.Config().V1().FeatureGates(),
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
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			ctx.ConfigInformerFactory.Config().V1().Schedulers(),
			ctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
		),
		// The drain controller drains pods from nodes
		drain.New(
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			ctx.ClientBuilder.KubeClientOrDie("node-update-controller"),
			ctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
		),
	)

	return controllers
}
