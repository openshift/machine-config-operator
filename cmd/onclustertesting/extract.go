package main

import (
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	extractCmd = &cobra.Command{
		Use:   "extract",
		Short: "Extracts the Dockerfile and MachineConfig from an on-cluster build",
		Long:  "",
		Run:   runExtractCmd,
	}

	extractOpts struct {
		poolName      string
		machineConfig string
		targetDir     string
		noConfigMaps  bool
	}
)

func init() {
	rootCmd.AddCommand(extractCmd)
	extractCmd.PersistentFlags().StringVar(&extractOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to extract")
	extractCmd.PersistentFlags().StringVar(&extractOpts.machineConfig, "machineconfig", "", "MachineConfig name to extract")
	extractCmd.PersistentFlags().StringVar(&extractOpts.targetDir, "dir", "", "Dir to store extract build objects")
}

func runExtractCmd(_ *cobra.Command, _ []string) {
	common(extractOpts)

	if extractOpts.poolName == "" && extractOpts.machineConfig == "" {
		klog.Fatalln("No pool name or MachineConfig name provided!")
	}

	if extractOpts.poolName != "" && extractOpts.machineConfig != "" {
		klog.Fatalln("Either pool name or MachineConfig must be provided. Not both!")
	}

	targetDir := getDir(extractOpts.targetDir)

	cs := framework.NewClientSet("")

	if extractOpts.machineConfig != "" {
		failOnError(extractBuildObjectsForRenderedMC(cs, extractOpts.machineConfig, targetDir))
		return
	}

	if extractOpts.poolName != "" {
		failOnError(extractBuildObjectsForTargetPool(cs, extractOpts.poolName, targetDir))
		return
	}
}
