package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/yaml"

	imagev1 "github.com/openshift/api/image/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"k8s.io/klog/v2"
)

// Retrieves the OSImageStream for the given release image or ImageStream path.
func getOSImageStreamFromReleaseImageOrImageStream(opts getOpts) (_ *mcfgv1alpha1.OSImageStream, errOut error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)
	defer cancel()

	if opts.outputFormat != "json" && opts.outputFormat != "yaml" {
		return nil, fmt.Errorf("unsupported output format %q: must be 'json' or 'yaml'", opts.outputFormat)
	}

	start := time.Now()
	defer func() {
		klog.V(defaultLogVerbosity).Infof("Retrieved OSImageStream in %s", time.Since(start))
	}()

	sysCtx, err := imageutils.NewSysContextFromFilesystem(imageutils.SysContextPaths{
		AdditionalTrustBundles: opts.trustBundlePaths,
		CertDir:                opts.certDirPath,
		PerHostCertDir:         opts.perHostCertsPath,
		Proxy:                  getProxyConfig(),
		PullSecret:             opts.authfilePath,
		RegistryConfig:         opts.registryConfigPath,
	})

	if err != nil {
		return nil, fmt.Errorf("could not prepare system context: %w", err)
	}

	defer func() {
		if err := sysCtx.Cleanup(); err != nil {
			klog.Warningf("unable to clean resources after OSImageStream inspection: %s", err)
			errOut = errors.Join(errOut, err)
		}
	}()

	inspectorFactory := &osimagestream.DefaultImagesInspectorFactory{}
	imagesInspector := inspectorFactory.ForContext(sysCtx.SysContext)

	imageStreamProvider, err := getImageStreamProvider(ctx, sysCtx, imagesInspector, opts.imageStreamPath, opts.releaseImage)
	if err != nil {
		return nil, fmt.Errorf("could not get ImageStream provider: %w", err)
	}

	streamSources := []osimagestream.StreamSource{
		osimagestream.NewImageStreamStreamSource(imagesInspector, imageStreamProvider, osimagestream.NewImageStreamExtractor()),
	}

	osImageStream, err := osimagestream.BuildOSImageStreamFromSources(ctx, streamSources)
	if err != nil {
		return nil, err
	}

	annoKey := fmt.Sprintf("machineconfiguration.openshift.io/generated-by-%s", componentName)
	metav1.SetMetaDataAnnotation(&osImageStream.ObjectMeta, annoKey, "")

	return osImageStream, nil
}

// Creates a proxy status if the appropriate env vars are set. Returns nil when
// none of the env vars are set.
func getProxyConfig() *configv1.ProxyStatus {
	proxyStatus := &configv1.ProxyStatus{}

	if httpProxy := os.Getenv("HTTP_PROXY"); httpProxy != "" {
		proxyStatus.HTTPProxy = httpProxy
	}

	if httpsProxy := os.Getenv("HTTPS_PROXY"); httpsProxy != "" {
		proxyStatus.HTTPSProxy = httpsProxy
	}

	// Although a newer version of container-libs uses the NO_PROXY env var, the
	// version we are using now does not. We should add that functionality here
	// if https://redhat.atlassian.net/browse/MCO-2016 is addressed.

	// If none of the environment variables were set, return a nil config.
	if proxyStatus.HTTPProxy == "" && proxyStatus.HTTPSProxy == "" {
		return nil
	}

	return proxyStatus
}

// detectFormatFromFilepath determines the output format based on file extension.
// Returns the detected format or falls back to the provided fallbackFormat.
func detectFormatFromFilepath(filepath, fallbackFormat string) string {
	if strings.HasSuffix(filepath, ".yaml") || strings.HasSuffix(filepath, ".yml") {
		return "yaml"
	}

	if strings.HasSuffix(filepath, ".json") {
		return "json"
	}

	return fallbackFormat
}

// Writes the OSImageStream to a file or stdout in the desired format.
func writeOutput(osImageStream *mcfgv1alpha1.OSImageStream, format, outputFile string) error {
	// Detect format from file extension if outputFile is provided
	finalFormat := format
	if outputFile != "" {
		finalFormat = detectFormatFromFilepath(outputFile, format)
	}

	var outBytes []byte

	switch finalFormat {
	case "json":
		out, err := json.MarshalIndent(osImageStream, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal OSImageStream to JSON: %w", err)
		}
		outBytes = out
	case "yaml":
		out, err := yaml.Marshal(osImageStream)
		if err != nil {
			return fmt.Errorf("failed to marshal OSImageStream to YAML: %w", err)
		}
		outBytes = append([]byte("---\n"), out...) // Add the --- to the YAML document.
	default:
		return fmt.Errorf("unsupported output format %q: must be 'json' or 'yaml'", finalFormat)
	}

	// Write to file if outputFile is specified, otherwise write to stdout
	if outputFile != "" {
		if err := os.WriteFile(outputFile, append(outBytes, '\n'), 0o644); err != nil {
			return fmt.Errorf("failed to write output to file %q: %w", outputFile, err)
		}
		klog.Infof("Successfully wrote output to %s in %s format", outputFile, finalFormat)
	} else {
		if _, err := fmt.Fprintf(os.Stdout, "%s\n", outBytes); err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
	}

	return nil
}

// Instantiates an ImageStreamProvider based upon whether the ImageStream
// source is the release image pullspec or a local file.
func getImageStreamProvider(ctx context.Context, sysCtx *imageutils.SysContext, imagesInspector osimagestream.ImagesInspector, imageStreamPath, releaseImage string) (osimagestream.ImageStreamProvider, error) {
	if imageStreamPath != "" {
		is, err := getImageStreamFromFile(imageStreamPath)
		if err != nil {
			return nil, err
		}

		return osimagestream.NewImageStreamProviderResource(is), nil
	}

	klog.V(defaultLogVerbosity).Infof("Will read ImageStream from release image %q", releaseImage)

	return osimagestream.NewImageStreamProviderNetwork(imagesInspector, releaseImage), nil
}

// Reads the ImageStream from a given path on the fileystem.
func getImageStreamFromFile(imageStreamPath string) (*imagev1.ImageStream, error) {
	isBytes, err := os.ReadFile(imageStreamPath)
	if err != nil {
		return nil, err
	}

	is := &imagev1.ImageStream{}
	if err := yaml.Unmarshal(isBytes, is); err != nil {
		return nil, fmt.Errorf("could not decode imagestream: %w", err)
	}

	klog.V(defaultLogVerbosity).Infof("Read ImageStream from %q", imageStreamPath)

	return is, nil
}
