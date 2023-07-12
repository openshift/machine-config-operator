package main

import (
	"context"
	"flag"
	"os"

	"github.com/ghodss/yaml"
	"github.com/openshift/machine-config-operator/internal/clients"
	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

var (
	dumpCmd = &cobra.Command{
		Use:   "dump",
		Short: "Dumps generated Machine OS Builder objects",
		Long:  "",
		Run:   runDumpCmd,
	}
)

func init() {
	rootCmd.AddCommand(dumpCmd)
}

func runDumpCmd(cmd *cobra.Command, args []string) {
	flag.Set("v", "4")
	flag.Set("logtostderr", "true")
	flag.Parse()

	klog.V(2).Infof("Options parsed: %+v", startOpts)

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cb, err := clients.NewBuilder("")
	if err != nil {
		klog.Fatalln(err)
	}

	kubeclient := cb.KubeClientOrDie("machine-os-builder")
	mcfgclient := cb.MachineConfigClientOrDie("machine-os-builder")

	onClusterBuildConfigMap, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "on-cluster-build-config", metav1.GetOptions{})
	if err != nil {
		klog.Fatalln(err)
	}

	osImageURLConfigMap, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "machine-config-osimageurl", metav1.GetOptions{})
	if err != nil {
		klog.Fatalln(err)
	}

	pool, err := mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, "infra", metav1.GetOptions{})
	if err != nil {
		klog.Fatalln(err)
	}

	mc, err := mcfgclient.MachineconfigurationV1().MachineConfigs().Get(ctx, pool.Spec.Configuration.Name, metav1.GetOptions{})
	if err != nil {
		klog.Fatalln(err)
	}

	customBuildPod := false

	items, err := build.DumpImageBuildRequestObjects(mc, pool, osImageURLConfigMap, onClusterBuildConfigMap, customBuildPod)

	// items = append(items, []runtime.Object{onClusterBuildConfigMap, osImageURLConfigMap, pool, mc}...)

	if err != nil {
		klog.Fatalln(err)
	}

	for _, item := range items {
		os.Stdout.Write([]byte("---\n"))
		out, err := yaml.Marshal(item)
		if err != nil {
			klog.Fatalln(err)
		}

		os.Stdout.Write(out)
	}
}
