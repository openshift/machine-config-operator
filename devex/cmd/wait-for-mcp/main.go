package main

import (
	"os"
	"time"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/rollout"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"k8s.io/klog/v2"
)

func main() {
	var timeout string

	rootCmd := &cobra.Command{
		Use:   "wait-for-mcp",
		Short: "Waits for a given MachineConfigPool to complete its updates.",
		Long:  "",
		RunE: func(_ *cobra.Command, args []string) error {
			return waitForMCPRollout(args, timeout)
		},
	}

	rootCmd.PersistentFlags().StringVar(&timeout, "timeout", "15m", "Timeout expressed in 0h0m0s format.")

	os.Exit(cli.Run(rootCmd))
}

func waitForMCPRollout(args []string, timeout string) error {
	parsedTimeout, err := time.ParseDuration(timeout)
	if err != nil {
		return err
	}

	klog.Infof("Timeout set to %s", parsedTimeout)

	if len(args) == 0 {
		klog.Infof("No MachineConfigPool(s) provided, will watch all pools")
		return rollout.WaitForAllMachineConfigPoolsToComplete(framework.NewClientSet(""), parsedTimeout)
	}

	return rollout.WaitForMachineConfigPoolsToComplete(framework.NewClientSet(""), args, parsedTimeout)
}
