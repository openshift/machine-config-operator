package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"

	"github.com/openshift/machine-config-operator/pkg/controller/bootstrap"
	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	bootstrapCmd = &cobra.Command{
		Use:   "bootstrap",
		Short: "Starts Machine Config Controller in bootstrap mode",
		Long:  "",
		Run:   runbootstrapCmd,
	}

	bootstrapOpts struct {
		manifestsDir   string
		destinationDir string
		pullSecretFile string
	}
)

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.destinationDir, "dest-dir", "", "The destination dir where MCC writes the generated machineconfigs and machineconfigpools.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.manifestsDir, "manifest-dir", "", "The dir where MCC reads the controllerconfig, machineconfigpools and user-defined machineconfigs.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.pullSecretFile, "pull-secret", "", "The pull secret file.")
}

func runbootstrapCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	if bootstrapOpts.manifestsDir == "" || bootstrapOpts.destinationDir == "" {
		glog.Fatalf("--dest-dir or --manifest-dir not set")
	}

	if err := bootstrap.New(rootOpts.templates, bootstrapOpts.manifestsDir, bootstrapOpts.pullSecretFile).Run(bootstrapOpts.destinationDir); err != nil {
		glog.Fatalf("error running MCC[BOOTSTRAP]: %v", err)
	}
}
