package main

import (
	"context"
	"errors"
	"io/fs"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

var (
	teardownCmd = &cobra.Command{
		Use:   "teardown",
		Short: "Tears down the pool for on-cluster build testing",
		Long:  "",
		Run:   runTeardownCmd,
	}

	teardownOpts struct {
		poolName string
		extract  bool
		dir      string
	}
)

func init() {
	rootCmd.AddCommand(teardownCmd)
	teardownCmd.PersistentFlags().StringVar(&teardownOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to teardown")
	teardownCmd.PersistentFlags().BoolVar(&teardownOpts.extract, "extract-objects", false, "Extract and store build objects on disk before teardown")
	teardownCmd.PersistentFlags().StringVar(&teardownOpts.dir, "dir", "", "Dir to store extract build objects")
}

func runTeardownCmd(_ *cobra.Command, _ []string) {
	common(teardownOpts)

	if teardownOpts.poolName == "" {
		klog.Fatalln("No pool name provided!")
	}

	targetDir := getDir(teardownOpts.dir)

	failOnError(mobTeardown(framework.NewClientSet(""), teardownOpts.poolName, targetDir, teardownOpts.extract))
}

func mobTeardown(cs *framework.ClientSet, targetPool, targetDir string, extractObjects bool) error {
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if extractObjects {
		klog.Infof("Extracting build objects (if they exist) to %s", targetDir)
		if err := extractBuildObjects(cs, mcp, targetDir); err != nil {
			if errors.Is(err, fs.ErrNotExist) || apierrs.IsNotFound(err) {
				klog.Warningf("Recovered from: %s", err)
			} else {
				return err
			}
		}
	} else {
		klog.Infof("Skipping build object extraction")
	}

	if err := deleteBuildObjects(cs, mcp); err != nil {
		return err
	}

	if err := teardownPool(cs, mcp); err != nil {
		return err
	}

	if err := deleteAllNonStandardPools(cs); err != nil {
		return err
	}

	return deleteAllMachineConfigsForPool(cs, targetPool)
}
