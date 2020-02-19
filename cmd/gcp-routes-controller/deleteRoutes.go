package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"

	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	deleteRoutesCmd = &cobra.Command{
		Use:   "delete-routes",
		Short: "Deletes all GCP routes",
		Long:  "",
		RunE:  runDeleteRoutesCmd,
	}
)

func init() {
	rootCmd.AddCommand(deleteRoutesCmd)
}

func runDeleteRoutesCmd(cmd *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	err := RunDelRoutes()
	if err != nil {
		return err
	}

	return nil
}
