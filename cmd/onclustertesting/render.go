package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	renderCmd = &cobra.Command{
		Use:   "render",
		Short: "Renders the on-cluster build Dockerfile to disk",
		Long:  "",
		Run:   runRenderCmd,
	}

	renderOpts struct {
		poolName             string
		includeMachineConfig bool
		targetDir            string
	}
)

func init() {
	rootCmd.AddCommand(renderCmd)
	renderCmd.PersistentFlags().StringVar(&renderOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to render")
	renderCmd.PersistentFlags().StringVar(&renderOpts.targetDir, "dir", "", "Dir to store rendered Dockerfile and MachineConfig in")
}

func runRenderCmd(_ *cobra.Command, _ []string) {
	common(renderOpts)

	if renderOpts.poolName == "" {
		failOnError(fmt.Errorf("no pool name provided"))
	}

	cs := framework.NewClientSet("")

	dir := filepath.Join(getDir(renderOpts.targetDir), renderOpts.poolName)

	failOnError(renderDockerfileToDisk(cs, renderOpts.poolName, dir))
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), renderOpts.poolName, metav1.GetOptions{})
	failOnError(err)
	failOnError(storeMachineConfigOnDisk(cs, mcp, dir))
}
