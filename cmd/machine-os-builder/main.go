package main

import (
	"flag"
	"fmt"

	"github.com/spf13/cobra"
)

const componentName = "machine-config"

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Machine OS Builder",
		Long:  "",
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	fmt.Println("Hello, World!")
}
