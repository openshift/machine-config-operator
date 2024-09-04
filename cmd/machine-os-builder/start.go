package main

import (
	"context"
	"errors"
	"flag"
	"os"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	ctrlcommonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func getMachineOSConfigs(ctx context.Context, cb *clients.Builder) (*mcfgv1alpha1.MachineOSConfigList, error) {
	mcfgClient := cb.MachineConfigClientOrDie(componentName)
	return mcfgClient.MachineconfigurationV1alpha1().MachineOSConfigs().List(ctx, metav1.ListOptions{})

}

// Creates a new BuildController configured for a certain image builder based
// upon the imageBuilderType key in the MOSC.
func getBuildControllers(ctx context.Context, cb *clients.Builder) ([]*build.Controller, error) {
	machineOSConfigs, err := getMachineOSConfigs(ctx, cb)
	if err != nil {
		return nil, err
	}

	ctrlCtx := ctrlcommon.CreateControllerContext(ctx, cb)
	buildClients := build.NewClientsFromControllerContext(ctrlCtx)
	cfg := build.DefaultBuildControllerConfig()

	controllersToStart := []*build.Controller{}

	podRequestExisted := 0
	for _, mosc := range machineOSConfigs.Items {
		if mosc.Spec.BuildInputs.ImageBuilder.ImageBuilderType == mcfgv1alpha1.MachineOSImageBuilderType("PodImageBuilder") && podRequestExisted == 0 {
			controllersToStart = append(controllersToStart, build.NewWithCustomPodBuilder(cfg, buildClients))
			podRequestExisted++
		}
	}
	return controllersToStart, nil
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
	cb, err := clients.NewBuilder("")
	if err != nil {
		klog.Fatalln(err)
	}

	run := func(ctx context.Context) {
		go common.SignalHandler(cancel)

		controllers, err := getBuildControllers(ctx, cb)
		if err != nil {
			klog.Fatalln(err)
			var invalidImageBuiler *build.ErrInvalidImageBuilder
			if errors.As(err, &invalidImageBuiler) {
				klog.Errorf("The user passed an invalid imageBuilderType of %s", invalidImageBuiler.InvalidType)
				cancel()
				os.Exit(255)
			}
		}
		for _, ctrl := range controllers {
			go ctrl.Run(ctx, 3)
		}
		<-ctx.Done()
		cancel()
	}

	leaderElectionCfg := common.GetLeaderElectionConfig(cb.GetBuilderConfig())

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            common.CreateResourceLock(cb, ctrlcommonconsts.MCONamespace, componentName),
		ReleaseOnCancel: true,
		LeaseDuration:   leaderElectionCfg.LeaseDuration.Duration,
		RenewDeadline:   leaderElectionCfg.RenewDeadline.Duration,
		RetryPeriod:     leaderElectionCfg.RetryPeriod.Duration,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {
				klog.Infof("Stopped leading. MOB terminating.")
				os.Exit(0)
			},
		},
	})

}
