package main

import (
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	clearStatusCmd = &cobra.Command{
		Use:   "clear-build-status",
		Short: "Tears down the pool for on-cluster build testing",
		Long:  "",
		Run:   runClearStatusCmd,
	}

	clearStatusOpts struct {
		poolName string
	}
)

func init() {
	rootCmd.AddCommand(clearStatusCmd)
	clearStatusCmd.PersistentFlags().StringVar(&clearStatusOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to clear build status on")
}

func runClearStatusCmd(_ *cobra.Command, _ []string) {
	common(clearStatusOpts)

	if clearStatusOpts.poolName == "" {
		klog.Fatalln("No pool name provided!")
	}

	failOnError(clearBuildStatusesOnPool(framework.NewClientSet(""), clearStatusOpts.poolName))
}
