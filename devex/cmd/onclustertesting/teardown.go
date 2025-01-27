package main

import (
	"context"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

type teardownOpts struct {
	poolName string
	dir      string
}

func init() {
	teardownOpts := teardownOpts{}

	teardownCmd := &cobra.Command{
		Use:   "teardown",
		Short: "Tears down the pool for on-cluster build testing",
		Long:  "",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runTeardownCmd(teardownOpts)
		},
	}

	teardownCmd.PersistentFlags().StringVar(&teardownOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to teardown")
	teardownCmd.PersistentFlags().StringVar(&teardownOpts.dir, "dir", "", "Dir to store extract build objects")

	rootCmd.AddCommand(teardownCmd)
}

func runTeardownCmd(opts teardownOpts) error {
	utils.ParseFlags()

	if opts.poolName == "" {
		klog.Fatalln("No pool name provided!")
	}

	return mobTeardown(framework.NewClientSet(""), opts.poolName)
}

func mobTeardown(cs *framework.ClientSet, targetPool string) error {
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
	if err != nil && !apierrs.IsNotFound(err) {
		return err
	}

	if err == nil && !hasOurLabel(mcp.Labels) {
		klog.Warningf("Provided MachineConfigPool %q was not created by this program, will ignore", mcp.Name)
		klog.Infof("Will do a label query searching for %q", createdByOnClusterBuildsHelper)

		mcp = nil
	}

	if apierrs.IsNotFound(err) {
		if targetPool == defaultLayeredPoolName {
			klog.Infof("Default MachineConfigPool %q not found, maybe you forgot to provide the pool name?", defaultLayeredPoolName)
		} else {
			klog.Infof("Provided MachineConfigPool %q not found, maybe you provided the wrong pool name?", targetPool)
		}

		mcp = nil
	}

	if err := deleteBuildObjects(cs); err != nil {
		return err
	}

	if mcp != nil {
		if err := teardownPool(cs, mcp); err != nil {
			return err
		}
	}

	if err := deleteAllPoolsWithOurLabel(cs); err != nil {
		return err
	}

	if err := deleteMachineOSBuilds(cs); err != nil {
		return err
	}

	return deleteMachineOSConfigs(cs)
}
