package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	v1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	containerruntimeconfig "github.com/openshift/machine-config-operator/pkg/controller/container-runtime-config"
	"github.com/openshift/machine-config-operator/pkg/controller/drain"
	kubeletconfig "github.com/openshift/machine-config-operator/pkg/controller/kubelet-config"
	"github.com/openshift/machine-config-operator/pkg/controller/node"
	"github.com/openshift/machine-config-operator/pkg/controller/render"
	"github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coreclientsetv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/scheme"
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
		StateSubControllers      []string
	}
)

func init() {
	rootCmd.AddCommand(startCmd)
	startCmd.PersistentFlags().StringArrayVar(&startOpts.StateSubControllers, "state-controllers", []string{""}, "enable/disable the different health controllers")
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

		ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb, componentName)

		// Start the metrics handler
		go ctrlcommon.StartMetricsListener(startOpts.promMetricsListenAddress, ctrlctx.Stop, ctrlcommon.RegisterMCCMetrics)

		kubeClient := ctrlctx.ClientBuilder.KubeClientOrDie("machine-config-controller")
		controllers, healthEvents, upgradeEvents, controllerMetricEvents := createControllers(ctrlctx, kubeClient)
		draincontroller := drain.New(
			drain.DefaultConfig(),
			ctrlctx.KubeInformerFactory.Core().V1().Nodes(),
			kubeClient,
			ctrlctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
		)

		// Start the shared factory informers that you need to use in your controller
		ctrlctx.InformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)

		close(ctrlctx.InformersStarted)

		select {
		case <-ctrlctx.FeatureGateAccess.InitialFeatureGatesObserved():
			features, err := ctrlctx.FeatureGateAccess.CurrentFeatureGates()
			if err != nil {
				klog.Fatalf("unable to get initial features: %v", err)
			}

			enabled, disabled := getEnabledDisabledFeatures(features)
			klog.Infof("FeatureGates initialized: enabled=%v  disabled=%v", enabled, disabled)
		case <-time.After(1 * time.Minute):
			klog.Errorf("timed out waiting for FeatureGate detection")
			os.Exit(1)
		}

		for _, c := range controllers {
			go c.Run(2, ctrlctx.Stop, healthEvents, controllerMetricEvents)
		}
		go draincontroller.Run(5, ctrlctx.Stop, healthEvents, upgradeEvents, controllerMetricEvents)

		// wait here in this function until the context gets cancelled (which tells us whe were being shut down)
		<-ctx.Done()
	}

	leaderElectionCfg := common.GetLeaderElectionConfig(cb.GetBuilderConfig())

	leaderelection.RunOrDie(runContext, leaderelection.LeaderElectionConfig{
		Lock:            common.CreateResourceLock(cb, startOpts.resourceLockNamespace, componentName),
		ReleaseOnCancel: true,
		LeaseDuration:   leaderElectionCfg.LeaseDuration.Duration,
		RenewDeadline:   leaderElectionCfg.RenewDeadline.Duration,
		RetryPeriod:     leaderElectionCfg.RetryPeriod.Duration,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {
				klog.Infof("Stopped leading. Terminating.")
				os.Exit(0)
			},
		},
	})
	panic("unreachable")
}

func createControllers(ctx *ctrlcommon.ControllerContext, kubeClient kubernetes.Interface) ([]ctrlcommon.Controller, record.EventRecorder, record.EventRecorder, record.EventRecorder) {

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&coreclientsetv1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("openshift-machine-config-operator")})
	upgradeEventsRecorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "upgrade-health"})
	healthEventsRecorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "mcc-health"})
	controllerMetricEvents := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "metrics"})

	var controllers []ctrlcommon.Controller
	statesubcontrollers := []v1.StateSubController{}
	for _, sub := range startOpts.StateSubControllers {
		statesubcontrollers = append(statesubcontrollers, v1.StateSubController(sub))
	}
	controllers = append(controllers,
		// Our primary MCs come from here
		template.New(
			rootOpts.templates,
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.OpenShiftConfigKubeNamespacedInformerFactory.Core().V1().Secrets(),
			kubeClient,
			ctx.ClientBuilder.MachineConfigClientOrDie("template-controller"),
			ctx.FeatureGateAccess,
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
			kubeClient,
			ctx.ClientBuilder.MachineConfigClientOrDie("kubelet-config-controller"),
			ctx.ClientBuilder.ConfigClientOrDie("kubelet-config-controller"),
			ctx.FeatureGateAccess,
		),
		containerruntimeconfig.New(
			rootOpts.templates,
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ContainerRuntimeConfigs(),
			ctx.ConfigInformerFactory.Config().V1().Images(),
			ctx.ConfigInformerFactory.Config().V1().ImageDigestMirrorSets(),
			ctx.ConfigInformerFactory.Config().V1().ImageTagMirrorSets(),
			ctx.OperatorInformerFactory.Operator().V1alpha1().ImageContentSourcePolicies(),
			ctx.ConfigInformerFactory.Config().V1().ClusterVersions(),
			kubeClient,
			ctx.ClientBuilder.MachineConfigClientOrDie("container-runtime-config-controller"),
			ctx.ClientBuilder.ConfigClientOrDie("container-runtime-config-controller"),
			ctx.FeatureGateAccess,
		),
		// The renderer creates "rendered" MCs from the MC fragments generated by
		// the above sub-controllers, which are then consumed by the node controller
		render.New(
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			kubeClient,
			ctx.ClientBuilder.MachineConfigClientOrDie("render-controller"),
		),
		// The node controller consumes data written by the above
		node.New(
			ctx.InformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctx.InformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctx.KubeInformerFactory.Core().V1().Nodes(),
			ctx.KubeInformerFactory.Core().V1().Pods(),
			ctx.ConfigInformerFactory.Config().V1().Schedulers(),
			kubeClient,
			ctx.ClientBuilder.MachineConfigClientOrDie("node-update-controller"),
		),
	)

	return controllers, healthEventsRecorder, upgradeEventsRecorder, controllerMetricEvents
}

func getEnabledDisabledFeatures(features featuregates.FeatureGate) ([]string, []string) {
	var enabled []string
	var disabled []string

	for _, feature := range features.KnownFeatures() {
		if features.Enabled(feature) {
			enabled = append(enabled, string(feature))
		} else {
			disabled = append(disabled, string(feature))
		}
	}

	return enabled, disabled
}
