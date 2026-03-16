package main

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

const componentName = "machine-config-osimagestream"
const defaultLogVerbosity = 2

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Generate the OSImageStream for a given OCP / OKD release",
		Long:  "",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	os.Exit(cli.Run(rootCmd))
}
