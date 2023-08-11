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
		force    bool
	}
)

func init() {
	rootCmd.AddCommand(teardownCmd)
	teardownCmd.PersistentFlags().StringVar(&teardownOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to teardown")
	teardownCmd.PersistentFlags().BoolVar(&teardownOpts.extract, "extract-objects", false, "Extract and store build objects on disk before teardown")
	teardownCmd.PersistentFlags().StringVar(&teardownOpts.dir, "dir", "", "Dir to store extract build objects")
	teardownCmd.PersistentFlags().BoolVar(&teardownOpts.force, "force", false, "Removes all on-cluster build related objects even if not created by this CLI tool")
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
	if err != nil && !apierrs.IsNotFound(err) {
		return err
	}

	if apierrs.IsNotFound(err) {
		klog.Infof("Pool %s not found", targetPool)
		mcp = nil
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

	if mcp != nil {
		if err := teardownPool(cs, mcp); err != nil {
			return err
		}
	}

	if err := deleteAllNonStandardPools(cs); err != nil {
		return err
	}

	if mcp != nil {
		if err := deleteAllMachineConfigsForPool(cs, mcp); err != nil {
			return err
		}
	}

	if teardownOpts.force {
		return forceCleanup(cs)
	}

	return normalCleanup(cs)
}

func normalCleanup(cs *framework.ClientSet) error {
	if err := cleanupConfigMaps(cs); err != nil {
		return err
	}

	if err := cleanupSecrets(cs); err != nil {
		return err
	}

	return cleanupImagestreams(cs)
}

func forceCleanup(cs *framework.ClientSet) error {
	if err := forceCleanupConfigMaps(cs); err != nil {
		klog.Errorf("could not force clean ConfigMaps")
		return err
	}

	if err := forceCleanupSecrets(cs); err != nil {
		klog.Errorf("could not force clean Secrets")
		return err
	}

	if err := deleteImagestream(cs, "os-image"); err != nil {
		klog.Errorf("could not force clean ImageStream 'os-image'")
		return err
	}

	return nil
}
