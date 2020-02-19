package main

import (
	"context"
	"flag"

	"github.com/golang/glog"
	operatorclientset "github.com/openshift/client-go/operator/clientset/versioned"
	operatorinformers "github.com/openshift/client-go/operator/informers/externalversions"
	operatorv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
	"github.com/openshift/machine-config-operator/cmd/common"
	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	if startOpts.imagesFile == "" {
		glog.Fatal("--images-json cannot be empty")
	}

	cb, err := clients.NewBuilder(startOpts.kubeconfig)
	if err != nil {
		glog.Fatalf("error creating clients: %v", err)
	}
	run := func(ctx context.Context) {
		ctrlctx := ctrlcommon.CreateControllerContext(cb, ctx.Done(), componentNamespace)
		operatorClient := cb.OperatorClientOrDie("operator-shared-informer")

		etcdInformer, err := getEtcdInformer(operatorClient, ctrlctx.OperatorInformerFactory)
		if err != nil {
			// MCO pod needs to restart for transient apiserver errors
			glog.Errorf("unable to query discovery API %#v", err)
			ctrlcommon.WriteTerminationError(err)
		}

		controller := operator.New(
			componentNamespace, componentName,
			startOpts.imagesFile,
			ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigPools(),
			ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().MachineConfigs(),
			ctrlctx.NamespacedInformerFactory.Machineconfiguration().V1().ControllerConfigs(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().ServiceAccounts(),
			ctrlctx.APIExtInformerFactory.Apiextensions().V1beta1().CustomResourceDefinitions(),
			ctrlctx.KubeNamespacedInformerFactory.Apps().V1().Deployments(),
			ctrlctx.KubeNamespacedInformerFactory.Apps().V1().DaemonSets(),
			ctrlctx.KubeNamespacedInformerFactory.Rbac().V1().ClusterRoles(),
			ctrlctx.KubeNamespacedInformerFactory.Rbac().V1().ClusterRoleBindings(),
			ctrlctx.KubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
			ctrlctx.KubeInformerFactory.Core().V1().ConfigMaps(),
			ctrlctx.ConfigInformerFactory.Config().V1().Infrastructures(),
			ctrlctx.ConfigInformerFactory.Config().V1().Networks(),
			ctrlctx.ConfigInformerFactory.Config().V1().Proxies(),
			ctrlctx.ClientBuilder.MachineConfigClientOrDie(componentName),
			ctrlctx.ClientBuilder.KubeClientOrDie(componentName),
			ctrlctx.ClientBuilder.APIExtClientOrDie(componentName),
			ctrlctx.ClientBuilder.ConfigClientOrDie(componentName),
			ctrlctx.OpenShiftKubeAPIServerKubeNamespacedInformerFactory.Core().V1().ConfigMaps(),
			etcdInformer,
		)

		ctrlctx.NamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.KubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.APIExtInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.ConfigInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OpenShiftKubeAPIServerKubeNamespacedInformerFactory.Start(ctrlctx.Stop)
		ctrlctx.OperatorInformerFactory.Start(ctrlctx.Stop)
		close(ctrlctx.InformersStarted)

		go controller.Run(2, ctrlctx.Stop)

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

func getEtcdInformer(operatorClient operatorclientset.Interface, operatorSharedInformer operatorinformers.SharedInformerFactory) (operatorv1.EtcdInformer, error) {
	operatorGroups, err := operatorClient.Discovery().ServerResourcesForGroupVersion("operator.openshift.io/v1")
	if err != nil {
		glog.Errorf("unable to get operatorGroups: %#v", err)
		return nil, err
	}

	for _, o := range operatorGroups.APIResources {
		if o.Kind == "Etcd" {
			return operatorSharedInformer.Operator().V1().Etcds(), nil
		}
	}
	return nil, nil
}
