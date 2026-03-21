package main

import (
	"flag"
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type getOpts struct {
	certDirPath        string
	imageStreamPath    string
	outputFile         string
	outputFormat       string
	perHostCertsPath   string
	authfilePath       string
	registryConfigPath string
	releaseImage       string
	trustBundlePaths   []string
}

var (
	getCmd = &cobra.Command{
		Use:   "get",
		Short: "Retrieves OSImageStream or default node image information",
	}

	osImageStreamCmd = &cobra.Command{
		Use:   "osimagestream",
		Short: "Get the default OSImageStream",
		Long: `Get the OSImageStream from a release payload image or ImageStream file.

You must provide exactly one of --release-image or --imagestream. These flags are mutually exclusive.

Examples:
  # Get OSImageStream from release image:
  $ machine-config-osimagestream get osimagestream \
      --authfile /path/to/pull-secret.json \
      --release-image quay.io/openshift-release-dev/ocp-release:latest

  # Get OSImageStream from ImageStream file:
  $ machine-config-osimagestream get osimagestream \
      --authfile /path/to/pull-secret.json \
      --imagestream /path/to/imagestream.json

For a comprehensive list of all flags and options, see the README.md in this directory.`,
		RunE: runOSImageStreamCmd,
	}

	defaultNodeImageCmd = &cobra.Command{
		Use:   "default-node-image",
		Short: "Get the default node OS image pullspec",
		Long: `Get the default node OS image pullspec from a release payload image or ImageStream file.

This command retrieves and outputs the default node OS image pullspec (full container image reference).

You must provide exactly one of --release-image or --imagestream. These flags are mutually exclusive.

Examples:
  # Get the default node OS image pullspec from the release image:
  $ machine-config-osimagestream get default-node-image \
      --authfile /path/to/pull-secret.json \
      --release-image quay.io/openshift-release-dev/ocp-release:latest

  # Get the default node OS image pullspec from an ImageStream file:
  $ machine-config-osimagestream get default-node-image \
      --authfile /path/to/pull-secret.json \
      --imagestream /path/to/imagestream.yaml

For a comprehensive list of all flags and options, see the README.md in this directory.`,
		RunE: runDefaultNodeImageCmd,
	}

	o = getOpts{}
)

func init() {
	// Add both to get command
	getCmd.AddCommand(osImageStreamCmd)
	getCmd.AddCommand(defaultNodeImageCmd)
	rootCmd.AddCommand(getCmd)

	// Shared persistent flags for get command
	getCmd.PersistentFlags().StringVar(&o.authfilePath, "authfile", "", "Path to an image registry authentication file.")

	getCmd.PersistentFlags().StringVar(&o.certDirPath, "cert-dir", "", "Path to directory containing certificates.")
	getCmd.PersistentFlags().StringVar(&o.registryConfigPath, "registry-config", "", "Path to registries.conf.")
	getCmd.PersistentFlags().StringArrayVar(&o.trustBundlePaths, "trust-bundle", []string{}, "Path(s) to additional trust bundle file(s) or directory(ies). Can be specified multiple times.")
	getCmd.PersistentFlags().StringVar(&o.perHostCertsPath, "per-host-certs", "", "Path to per-host certificates directory.")
	getCmd.PersistentFlags().StringVar(&o.releaseImage, "release-image", "", "The OCP / OKD release payload image to run against.")
	getCmd.PersistentFlags().StringVar(&o.imageStreamPath, "imagestream", "", "Path to the ImageStream file to run against.")

	getCmd.MarkPersistentFlagRequired("authfile")

	// OSImageStream flags (mutually exclusive - validated at runtime)
	osImageStreamCmd.PersistentFlags().StringVar(&o.outputFormat, "output-format", "json", "The output format to use: 'json' or 'yaml'.")
	osImageStreamCmd.PersistentFlags().StringVar(&o.outputFile, "output-file", "", "Path to write output to a file instead of stdout. Format is auto-detected from extension (.json, .yaml, .yml).")
}

func runOSImageStreamCmd(_ *cobra.Command, _ []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, show version
	klog.V(defaultLogVerbosity).Infof("Version: %+v (%s)", version.Raw, version.Hash)

	// Validate mutually exclusive flags
	if o.imageStreamPath != "" && o.releaseImage != "" {
		klog.Exitf("--imagestream and --release-image are mutually exclusive")
	}

	if o.imageStreamPath == "" && o.releaseImage == "" {
		klog.Exitf("either --imagestream or --release-image must be provided")
	}

	osImageStream, err := getOSImageStreamFromReleaseImageOrImageStream(o)
	if err != nil {
		return err
	}

	return writeOutput(osImageStream, o.outputFormat, o.outputFile)
}

func runDefaultNodeImageCmd(_ *cobra.Command, _ []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, show version
	klog.V(defaultLogVerbosity).Infof("Version: %+v (%s)", version.Raw, version.Hash)

	// Validate mutually exclusive flags
	if o.imageStreamPath != "" && o.releaseImage != "" {
		klog.Exitf("--imagestream and --release-image are mutually exclusive")
	}

	if o.imageStreamPath == "" && o.releaseImage == "" {
		klog.Exitf("either --imagestream or --release-image must be provided")
	}

	osImageStream, err := getOSImageStreamFromReleaseImageOrImageStream(o)
	if err != nil {
		return err
	}

	defaultOSImageStream, err := osimagestream.GetOSImageStreamSetByName(osImageStream, osImageStream.Status.DefaultStream)
	if err != nil {
		return err
	}

	fmt.Println(string(defaultOSImageStream.OSImage))
	return nil
}
