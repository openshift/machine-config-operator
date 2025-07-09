package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type outputFormat string

const (
	dockercfgOutputFormat               outputFormat = "dockercfg"
	dockerconfigjsonOutputFormat        outputFormat = "dockerconfigjson"
	k8sDockercfgOutputFormat            outputFormat = "k8s-dockercfg"
	k8sDockerconfigjsonOutputFormat     outputFormat = "k8s-dockerconfigjson"
	k8sJSONDockercfgOutputFormat        outputFormat = "k8s-json-dockercfg"
	k8sJSONDockerconfigjsonOutputFormat outputFormat = "k8s-json-dockerconfigjson"
)

var outputFormats = []outputFormat{
	dockercfgOutputFormat,
	dockerconfigjsonOutputFormat,
	k8sDockercfgOutputFormat,
	k8sDockerconfigjsonOutputFormat,
	k8sJSONDockercfgOutputFormat,
	k8sJSONDockerconfigjsonOutputFormat,
}

func isValidOutputFormat(in string) bool {
	in = strings.ToLower(in)

	for _, format := range outputFormats {
		if outputFormat(in) == format {
			return true
		}
	}

	return false
}

var rootCmd = &cobra.Command{
	Use:   "secret-converter [secret-file]",
	Args:  cobra.ExactArgs(1),
	Short: "A tool to convert image registry secrets to various formats",
	Long: `secret-converter is a command-line tool designed to help
you convert Kubernetes image registry secrets (type kubernetes.io/dockerconfigjson)
into different byte array formats suitable for embedding in applications.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		secretFile := args[0]
		of, err := cmd.Flags().GetString("format")
		if err != nil {
			return fmt.Errorf("could not get output format: %w", err)
		}

		if of == "" {
			return fmt.Errorf("format cannot be empty")
		}

		if !isValidOutputFormat(of) {
			return fmt.Errorf("invalid output format %s, supported formats: %v", of, outputFormats)
		}

		converter, err := newSecretConverter(secretFile, outputFormat(of))
		if err != nil {
			return err
		}

		outBytes, err := converter.convert()
		if err != nil {
			return err
		}

		fmt.Println(string(outBytes))

		return nil
	},
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().StringP("format", "o", "dockerconfigjson", fmt.Sprintf("Output formats: %v", outputFormats))
}
