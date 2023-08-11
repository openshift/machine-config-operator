package main

import (
	"flag"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	setupCmd = &cobra.Command{
		Use:   "setup",
		Short: "Sets up pool for on-cluster build testing",
		Long:  "",
		Run:   runSetupCmd,
	}

	setupOpts struct {
		poolName         string
		waitForBuildInfo bool
	}
)

func init() {
	rootCmd.AddCommand(setupCmd)
	setupCmd.PersistentFlags().StringVar(&setupOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to setup")
	setupCmd.PersistentFlags().BoolVar(&setupOpts.waitForBuildInfo, "wait-for-build", false, "Wait for build info")
}

func runSetupCmd(_ *cobra.Command, _ []string) {
	flag.Set("v", "4")
	flag.Set("logtostderr", "true")
	flag.Parse()

	klog.V(2).Infof("Options parsed: %+v", setupOpts)

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	if setupOpts.poolName == "" {
		klog.Fatalln("No pool name provided!")
	}

	if err := mobSetup(framework.NewClientSet(""), setupOpts.poolName, setupOpts.waitForBuildInfo); err != nil {
		klog.Fatal(err)
	}
}

func mobSetup(cs *framework.ClientSet, targetPool string, getBuildInfo bool) error {
	if _, err := createPool(cs, targetPool); err != nil {
		return err
	}

	if err := optInPool(cs, targetPool); err != nil {
		return err
	}

	if !getBuildInfo {
		return nil
	}

	return waitForBuildInfo(cs, targetPool)
}

func waitForBuildInfo(_ *framework.ClientSet, _ string) error {
	klog.Infof("no-op for now")
	return nil
}
