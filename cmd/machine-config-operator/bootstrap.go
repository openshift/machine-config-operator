package main

import (
	"flag"
	"fmt"
	"io/ioutil"

	"github.com/golang/glog"
	"github.com/spf13/cobra"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	"github.com/openshift/machine-config-operator/pkg/operator"
	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	bootstrapCmd = &cobra.Command{
		Use:   "bootstrap",
		Short: "Machine Config Operator in bootstrap mode",
		Long:  "",
		Run:   runBootstrapCmd,
	}

	bootstrapOpts struct {
		baremetalRuntimeCfgImage               string
		cloudConfigFile                        string
		configFile                             string
		cloudProviderCAFile                    string
		corednsImage                           string
		destinationDir                         string
		haproxyImage                           string
		imagesConfigMapFile                    string
		infraConfigFile                        string
		infraImage                             string
		releaseImage                           string
		keepalivedImage                        string
		kubeCAFile                             string
		mcoImage                               string
		oauthProxyImage                        string
		networkConfigFile                      string
		oscontentImage                         string
		pullSecretFile                         string
		rootCAFile                             string
		proxyConfigFile                        string
		additionalTrustBundleFile              string
		dnsConfigFile                          string
		imageReferences                        string
		baseOperatingSystemContainer           string
		baseOperatingSystemExtensionsContainer string
	}
)

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.rootCAFile, "root-ca", "/etc/ssl/kubernetes/ca.crt", "path to root CA certificate")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.kubeCAFile, "kube-ca", "", "path to kube-apiserver serving-ca bundle")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.pullSecretFile, "pull-secret", "/assets/manifests/pull.json", "path to secret manifest that contains pull secret.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.destinationDir, "dest-dir", "", "The destination directory where MCO writes the manifests.")
	bootstrapCmd.MarkFlagRequired("dest-dir")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcoImage, "machine-config-operator-image", "", "Image for Machine Config Operator.")
	bootstrapCmd.MarkFlagRequired("machine-config-operator-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.oscontentImage, "machine-config-oscontent-image", "", "Image for osImageURL")
	bootstrapCmd.MarkFlagRequired("machine-config-oscontent-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.infraImage, "infra-image", "", "Image for Infra Containers.")
	bootstrapCmd.MarkFlagRequired("infra-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.releaseImage, "release-image", "", "Release image used for cluster installation.")
	bootstrapCmd.MarkFlagRequired("release-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.configFile, "config-file", "", "ClusterConfig ConfigMap file.")
	bootstrapCmd.MarkFlagRequired("config-file")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.infraConfigFile, "infra-config-file", "/assets/manifests/cluster-infrastructure-02-config.yml", "File containing infrastructure.config.openshift.io manifest.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.networkConfigFile, "network-config-file", "/assets/manifests/cluster-network-02-config.yml", "File containing network.config.openshift.io manifest.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.cloudConfigFile, "cloud-config-file", "", "File containing the config map that contains the cloud config for cloudprovider.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.proxyConfigFile, "proxy-config-file", "/assets/manifests/cluster-proxy-01-config.yaml", "File containing proxy.config.openshift.io manifest.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.dnsConfigFile, "dns-config-file", "/assets/manifests/cluster-dns-02-config.yml", "File containing dns.config.openshift.io manifest.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.additionalTrustBundleFile, "additional-trust-bundle-config-file", "/assets/manifests/user-ca-bundle-config.yaml", "File containing the additional user provided CA bundle manifest.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.keepalivedImage, "keepalived-image", "", "Image for Keepalived.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.corednsImage, "coredns-image", "", "Image for CoreDNS.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.haproxyImage, "haproxy-image", "", "Image for haproxy.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.baremetalRuntimeCfgImage, "baremetal-runtimecfg-image", "", "Image for baremetal-runtimecfg.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.oauthProxyImage, "oauth-proxy-image", "", "Image for origin oauth proxy.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.imageReferences, "image-references", "", "File containing imagestreams (from cluster-version-operator)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.cloudProviderCAFile, "cloud-provider-ca-file", "", "path to cloud provider CA certificate")

}

// findImage returns the image with a particular tag in an imagestream.
func findImage(stream *imagev1.ImageStream, name string) (string, error) {
	for _, tag := range stream.Spec.Tags {
		if tag.Name == name {
			// we found the short name in ImageStream
			if tag.From != nil && tag.From.Kind == "DockerImage" {
				return tag.From.Name, nil
			}
		}
	}
	return "", fmt.Errorf("could not find %s in images", name)
}

func findImageOrDie(stream *imagev1.ImageStream, name string) string {
	img, err := findImage(stream, name)
	if err != nil {
		glog.Fatalf("Failed to find %s in image references", name)
	}
	return img
}

func runBootstrapCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	if bootstrapOpts.imageReferences != "" {
		imageRefData, err := ioutil.ReadFile(bootstrapOpts.imageReferences)
		if err != nil {
			glog.Fatalf("failed to read %s: %v", bootstrapOpts.imageReferences, err)
		}

		imgstream := resourceread.ReadImageStreamV1OrDie(imageRefData)

		bootstrapOpts.mcoImage = findImageOrDie(imgstream, "machine-config-operator")
		bootstrapOpts.oscontentImage = findImageOrDie(imgstream, "machine-os-content")
		bootstrapOpts.keepalivedImage = findImageOrDie(imgstream, "keepalived-ipfailover")
		bootstrapOpts.corednsImage = findImageOrDie(imgstream, "coredns")
		bootstrapOpts.baremetalRuntimeCfgImage = findImageOrDie(imgstream, "baremetal-runtimecfg")
		// TODO: Hmm, this one doesn't actually seem to be passed right now at bootstrap time by the installer
		bootstrapOpts.oauthProxyImage = findImageOrDie(imgstream, "oauth-proxy")
		bootstrapOpts.infraImage = findImageOrDie(imgstream, "pod")
		bootstrapOpts.haproxyImage = findImageOrDie(imgstream, "haproxy-router")
		bootstrapOpts.baseOperatingSystemContainer, err = findImage(imgstream, "rhel-coreos-8")
		if err != nil {
			glog.Warningf("Base OS container not found: %s", err)
		}
		bootstrapOpts.baseOperatingSystemExtensionsContainer, err = findImage(imgstream, "rhel-coreos-8-extensions")
		if err != nil {
			glog.Warningf("Base OS extensions container not found: %s", err)
		}
	}

	imgs := operator.Images{
		RenderConfigImages: operator.RenderConfigImages{
			MachineConfigOperator:                  bootstrapOpts.mcoImage,
			MachineOSContent:                       bootstrapOpts.oscontentImage,
			KeepalivedBootstrap:                    bootstrapOpts.keepalivedImage,
			CorednsBootstrap:                       bootstrapOpts.corednsImage,
			BaremetalRuntimeCfgBootstrap:           bootstrapOpts.baremetalRuntimeCfgImage,
			OauthProxy:                             bootstrapOpts.oauthProxyImage,
			BaseOperatingSystemContainer:           bootstrapOpts.baseOperatingSystemContainer,
			BaseOperatingSystemExtensionsContainer: bootstrapOpts.baseOperatingSystemExtensionsContainer,
		},
		ControllerConfigImages: operator.ControllerConfigImages{
			InfraImage:          bootstrapOpts.infraImage,
			Keepalived:          bootstrapOpts.keepalivedImage,
			Coredns:             bootstrapOpts.corednsImage,
			Haproxy:             bootstrapOpts.haproxyImage,
			BaremetalRuntimeCfg: bootstrapOpts.baremetalRuntimeCfgImage,
		},
	}

	if err := operator.RenderBootstrap(
		bootstrapOpts.additionalTrustBundleFile,
		bootstrapOpts.proxyConfigFile,
		bootstrapOpts.configFile,
		bootstrapOpts.infraConfigFile,
		bootstrapOpts.networkConfigFile,
		bootstrapOpts.dnsConfigFile,
		bootstrapOpts.cloudConfigFile,
		bootstrapOpts.cloudProviderCAFile,
		bootstrapOpts.rootCAFile, bootstrapOpts.kubeCAFile, bootstrapOpts.pullSecretFile,
		&imgs,
		bootstrapOpts.destinationDir,
		bootstrapOpts.releaseImage,
	); err != nil {
		glog.Fatalf("error rendering bootstrap manifests: %v", err)
	}
}
