package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"

	"github.com/openshift/machine-config-operator/pkg/operator"
	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	bootstrapCmd = &cobra.Command{
		Use:   "bootstrap",
		Short: "Machine Config Operator in bootstrap mode",
		Long:  "",
		Run:   runBootstrapCmd,
	}

	bootstrapOpts struct {
		configFile     string
		destinationDir string
	}
)

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.destinationDir, "dest-dir", "", "The destination directory where MCO writes the manifests.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.configFile, "config-file", "", "MCOConfig file.")
}

func runBootstrapCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	if bootstrapOpts.destinationDir == "" {
		glog.Fatal("--dest-dir cannot be empty")
	}

	if bootstrapOpts.configFile == "" {
		glog.Fatal("--config-file cannot be empty")
	}

	if err := operator.RenderBootstrap(bootstrapOpts.configFile, bootstrapOpts.destinationDir); err != nil {
		glog.Fatalf("error rendering bootstrap manifests: %v", err)
	}
}
