package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
)

// outputType is a custom type for defining the desired output format of the secret.
type outputType string

const (
	// typeDockerconfigJSON represents the output type for .dockerconfigjson.
	typeDockerconfigJSON outputType = "dockerconfigjson"
	// typeDockercfg represents the output type for .dockercfg.
	typeDockercfg outputType = "dockercfg"
)

// outputFormat is a custom type for defining the serialization format of the output.
type outputFormat string

const (
	// formatJSON represents the JSON output format.
	formatJSON outputFormat = "json"
	// formatYAML represents the YAML output format.
	formatYAML outputFormat = "yaml"
)

// config holds the configuration parameters for the secret conversion tool.
type config struct {
	outputType   outputType
	outputFormat outputFormat
	embed        bool
	outputFile   string
	secretFile   string
}

// validate checks if the configuration parameters are valid.
// It returns an error if outputType or outputFormat are not among the allowed values.
func (c *config) validate() error {
	if cfg.outputType != typeDockerconfigJSON && cfg.outputType != typeDockercfg {
		return fmt.Errorf("error: invalid output type '%s', must be '%s' or '%s'", cfg.outputType, typeDockerconfigJSON, typeDockercfg)
	}

	if cfg.outputFormat != formatJSON && cfg.outputFormat != formatYAML {
		return fmt.Errorf("error: invalid output type '%s', must be '%s' or '%s'", cfg.outputFormat, formatJSON, formatYAML)
	}

	return nil
}

// getSecretType returns the corresponding Kubernetes corev1.SecretType based on the configured outputType.
// It returns an error if the outputType is unknown or missing.
func (c *config) getSecretType() (corev1.SecretType, error) {
	if c.outputType == typeDockerconfigJSON {
		return corev1.SecretTypeDockerConfigJson, nil
	}

	if c.outputType == typeDockercfg {
		return corev1.SecretTypeDockercfg, nil
	}

	return "", fmt.Errorf("unknown or missing output type %q", c.outputType)
}

// cfg is the global configuration instance for the secretconverter tool.
var cfg config

// rootCmd represents the base command when called without any subcommands.
// It defines the main entry point and logic for the secret conversion.
var rootCmd = &cobra.Command{
	Use:   "secretconverter [flags] [secret-file]",
	Args:  cobra.ExactArgs(1),
	Short: "A CLI tool to convert secrets to Docker config types and embed into Kubernetes secrets",
	Long: `secretconverter is a command-line tool that reads a secret from a file,
converts it to either a .dockerconfigjson or .dockercfg type, and can
optionally embed the converted secret into a Kubernetes Secret manifest.`,
	RunE: func(_ *cobra.Command, args []string) error {
		secretFile := args[0]
		cfg.secretFile = secretFile

		sc, err := newSecretConverter(&cfg)
		if err != nil {
			return fmt.Errorf("could not create secret converter: %w", err)
		}

		if err := sc.convert(); err != nil {
			return err
		}

		return nil
	},
}

// init initializes the root command and sets up its persistent flags.
// These flags control the output type, format, embedding, and output file.
func init() {
	rootCmd.PersistentFlags().StringVarP((*string)(&cfg.outputType), "output", "o", string(typeDockerconfigJSON), fmt.Sprintf("Desired output type: '%s' or '%s'", typeDockerconfigJSON, typeDockercfg))
	rootCmd.PersistentFlags().StringVarP((*string)(&cfg.outputFormat), "type", "t", "", fmt.Sprintf("Output type for the converted secret: '%s' or '%s'", formatJSON, formatYAML))
	rootCmd.PersistentFlags().BoolVarP(&cfg.embed, "embed", "k", false, "Embed the converted secret into a Kubernetes Secret manifest")
	rootCmd.PersistentFlags().StringVarP(&cfg.outputFile, "output-file", "w", "", "Path to a file to write the output instead of stdout")
}

// main is the entry point of the secretconverter tool.
// It executes the root command and handles any errors.
func main() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}
