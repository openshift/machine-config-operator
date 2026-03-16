package main

import (
	"flag"
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type getOpts struct {
	certDirPath        string
	imageStreamPath    string
	osImageStreamName  string
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

	osImagePullspecCmd = &cobra.Command{
		Use:   "os-image-pullspec",
		Short: "Get the OS image pullspec for the given OSImageStream name",
		Long: `Get the OS image pullspec for a specific OSImageStream name.

This command retrieves the OS image pullspec (full container image reference) for a given OSImageStream name. The OSImageStream name typically corresponds to a specific RHEL/RHCOS version stream (e.g., "rhel-9", "rhel-10", "rhel-9-coreos").

You must provide:
- The --osimagestream-name flag to specify which stream to look up
- Exactly one of --release-image or --imagestream (mutually exclusive)

Examples:
  # Get OS image pullspec for rhel-9 stream from release image:
  $ machine-config-osimagestream get os-image-pullspec \
      --authfile /path/to/pull-secret.json \
      --release-image quay.io/openshift-release-dev/ocp-release:latest \
      --osimagestream-name rhel-9

  # Get OS image pullspec for rhel-10 stream from ImageStream file:
  $ machine-config-osimagestream get os-image-pullspec \
      --authfile /path/to/pull-secret.json \
      --imagestream /path/to/imagestream.json \
      --osimagestream-name rhel-10

For a comprehensive list of all flags and options, see the README.md in this directory.`,
		RunE: runOSImagePullspecCmd,
	}

	o = getOpts{}
)

func init() {
	getCmd.AddCommand(osImageStreamCmd)
	getCmd.AddCommand(defaultNodeImageCmd)
	getCmd.AddCommand(osImagePullspecCmd)

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

	// osimagestream command flags (mutually exclusive - validated at runtime)
	msg := fmt.Sprintf("The output format to use: '%s' or '%s'.", outputFormatJSON, outputFormatYAML)
	osImageStreamCmd.PersistentFlags().StringVar(&o.outputFormat, "output-format", outputFormatJSON, msg)
	osImageStreamCmd.PersistentFlags().StringVar(&o.outputFile, "output-file", "", "Path to write output to a file instead of stdout. Format is auto-detected from extension (.json, .yaml, .yml).")

	// os-image-pullspec command flags
	osImagePullspecCmd.PersistentFlags().StringVar(&o.osImageStreamName, "osimagestream-name", "", "The OSImageStream name to look up.")
	osImagePullspecCmd.MarkPersistentFlagRequired("osimagestream-name")
}

// Validates common CLI flags are set correctly.
func validateCLIArgs(_ *cobra.Command, _ []string) error {
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

	return nil
}

// Retrieves the OSImageStream and writes it to the expected location (stdout
// or file).
func runOSImageStreamCmd(cmd *cobra.Command, args []string) error {
	if err := validateCLIArgs(cmd, args); err != nil {
		return err
	}

	osImageStream, err := getOSImageStreamFromReleaseImageOrImageStream(o)
	if err != nil {
		return err
	}

	return writeOutput(osImageStream, o.outputFormat, o.outputFile)
}

// Retrieves the default node image pullspec and prints it to stdout.
func runDefaultNodeImageCmd(cmd *cobra.Command, args []string) error {
	if err := validateCLIArgs(cmd, args); err != nil {
		return err
	}

	osImageStream, err := getOSImageStreamFromReleaseImageOrImageStream(o)
	if err != nil {
		return err
	}

	defaultOSImageStream, err := getOSImageStreamSetFromOSImageStream(osImageStream, osImageStream.Status.DefaultStream)
	if err != nil {
		return err
	}

	fmt.Println(string(defaultOSImageStream.OSImage))
	return nil
}

// Retrieves the node image pullspec for the given OSImageStream name and
// prints it to stdout.
func runOSImagePullspecCmd(cmd *cobra.Command, args []string) error {
	if err := validateCLIArgs(cmd, args); err != nil {
		return err
	}

	osImageStream, err := getOSImageStreamFromReleaseImageOrImageStream(o)
	if err != nil {
		return err
	}

	target, err := getOSImageStreamSetFromOSImageStream(osImageStream, o.osImageStreamName)
	if err != nil {
		return err
	}

	fmt.Println(string(target.OSImage))
	return nil
}
