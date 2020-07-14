package main

import (
	"flag"
	"fmt"
	"os"

	daemon "github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var mountContainer = &cobra.Command{
	Use:                   "mount-container",
	DisableFlagsInUseLine: true,
	Short:                 "Pull and mount container",
	Args:                  cobra.ExactArgs(1),
	Run:                   executeMountContainer,
}

// init executes upon import
func init() {
	rootCmd.AddCommand(mountContainer)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
}

func saveToFile(content, path string) error {
	file, err := os.Create(path)
	if err != nil {
		file.Close()
		return fmt.Errorf("Error creating file %s: %v", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	return nil

}

func runMountContainer(_ *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()
	var containerMntLoc, containerImage, containerName string
	containerImage = args[0]

	var err error
	if containerMntLoc, containerName, err = daemon.MountOSContainer(containerImage); err != nil {
		return err
	}
	// Save mounted container name and location into file for later to be used
	// for OS rebase and applying extensions
	if err := saveToFile(containerName, constants.MountedOSContainerName); err != nil {
		return fmt.Errorf("Failed saving container name: %v", err)
	}
	if err := saveToFile(containerMntLoc, constants.MountedOSContainerLocation); err != nil {
		return fmt.Errorf("Failed saving mounted container location: %v", err)
	}

	return nil

}

func executeMountContainer(cmd *cobra.Command, args []string) {
	err := runMountContainer(cmd, args)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}
