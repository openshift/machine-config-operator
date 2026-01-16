package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/operator"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/klog/v2"
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

func runStartCmd(_ *cobra.Command, _ []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// This is 'main' context that we thread through the controller context and
	// the leader elections. Cancelling this is "stop everything, we are shutting down".
	runContext, runCancel := context.WithCancel(context.Background())

	// To help debugging, immediately log version
	klog.Infof("Version: %s (Raw: %s, Hash: %s)", version.ReleaseVersion, version.Raw, version.Hash)

	if startOpts.imagesFile == "" {
		klog.Fatal("--images-json cannot be empty")
	}

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		klog.Fatalf("error creating clients: %v", err)
	}

	run := func(ctx context.Context) {
		go common.SignalHandler(runCancel)
		ctrlctx := ctrlcommon.CreateControllerContext(ctx, cb)

		// Early start the config informer because feature gate depends on it
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		if fgErr := ctrlctx.FeatureGatesHandler.Connect(ctx); fgErr != nil {
			klog.Fatal(fmt.Errorf("failed to connect to feature gates %w", fgErr))
		}

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
			ctrlctx.ConfigInformerFactory.Config().V1().Images(),
			ctrlctx.ConfigInformerFactory.Config().V1().ImageDigestMirrorSets(),
			ctrlctx.ConfigInformerFactory.Config().V1().ImageTagMirrorSets(),
			ctrlctx.OperatorInformerFactory.Operator().V1alpha1().ImageContentSourcePolicies(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().ServiceAccounts(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().Secrets(),
			ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
			ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Core().V1().Secrets(),
			ctrlctx.OpenShiftConfigManagedKubeNamespacedInformerFactory.Core().V1().Secrets(),
			ctrlctx.ConfigInformerFactory.Config().V1().ClusterOperators(),
			ctrlctx.ClientBuilder.OperatorClientOrDie(componentName),
			ctrlctx.OperatorInformerFactory.Operator().V1().MachineConfigurations(),
			ctrlctx.FeatureGatesHandler,
			ctrlctx.InformerFactory.Machineconfiguration().V1().KubeletConfigs(),
			ctrlctx.InformerFactory.Machineconfiguration().V1().ContainerRuntimeConfigs(),
			ctrlctx.ConfigInformerFactory.Config().V1().Nodes(),
			ctrlctx.ConfigInformerFactory.Config().V1().APIServers(),
			ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineOSConfigs(),
			ctrlctx.ConfigInformerFactory.Config().V1().ClusterVersions(),
			ctrlctx.InformerFactory.Machineconfiguration().V1alpha1().OSImageStreams(),
			ctrlctx,
		)

		ctrlctx.InformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.NamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.APIExtInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftKubeAPIServerKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftConfigKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftConfigManagedKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeMAOSharedInformer.Start(ctrlctx.Stop)

		close(ctrlctx.InformersStarted)

		go controller.Run(2, ctrlctx.Stop)

		// wait here in this function until the context gets cancelled (which tells us whe were being shut down)
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
