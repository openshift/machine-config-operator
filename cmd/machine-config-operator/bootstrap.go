package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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
		baremetalRuntimeCfgImage       string
		cloudConfigFile                string
		configFile                     string
		cloudProviderCAFile            string
		corednsImage                   string
		destinationDir                 string
		haproxyImage                   string
		imagesConfigMapFile            string
		infraConfigFile                string
		infraImage                     string
		releaseImage                   string
		keepalivedImage                string
		kubeCAFile                     string
		mcoImage                       string
		oauthProxyImage                string
		kubeRbacProxyImage             string
		networkConfigFile              string
		oscontentImage                 string
		pullSecretFile                 string
		mcsCAFile                      string
		proxyConfigFile                string
		additionalTrustBundleFile      string
		dnsConfigFile                  string
		imageReferences                string
		baseOSContainerImage           string
		baseOSExtensionsContainerImage string
	}
)

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	// See https://docs.openshift.com/container-platform/4.13/security/certificate_types_descriptions/machine-config-operator-certificates.html
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcsCAFile, "root-ca", "/etc/ssl/kubernetes/ca.crt", "Path to installer-generated root MCS CA")
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
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.baseOSContainerImage, "baseos-image", "", "ostree-bootable container image reference")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.baseOSExtensionsContainerImage, "baseos-extensions-image", "", "Image with extensions")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.kubeRbacProxyImage, "kube-rbac-proxy-image", "", "Image for origin kube-rbac proxy.")
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
		klog.Fatalf("Failed to find %s in image references", name)
	}
	return img
}

func runBootstrapCmd(_ *cobra.Command, _ []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	baseOSContainerImageTag := "rhel-coreos"
	if version.IsFCOS() {
		baseOSContainerImageTag = "fedora-coreos"
	} else if version.IsSCOS() {
		baseOSContainerImageTag = "stream-coreos"
	}

	if bootstrapOpts.imageReferences != "" {
		imageRefData, err := os.ReadFile(bootstrapOpts.imageReferences)
		if err != nil {
			klog.Fatalf("failed to read %s: %v", bootstrapOpts.imageReferences, err)
		}

		imgstream := resourceread.ReadImageStreamV1OrDie(imageRefData)

		bootstrapOpts.mcoImage = findImageOrDie(imgstream, "machine-config-operator")
		bootstrapOpts.keepalivedImage = findImageOrDie(imgstream, "keepalived-ipfailover")
		bootstrapOpts.corednsImage = findImageOrDie(imgstream, "coredns")
		bootstrapOpts.baremetalRuntimeCfgImage = findImageOrDie(imgstream, "baremetal-runtimecfg")
		// TODO: Hmm, this one doesn't actually seem to be passed right now at bootstrap time by the installer
		bootstrapOpts.oauthProxyImage = findImageOrDie(imgstream, "oauth-proxy")
		bootstrapOpts.kubeRbacProxyImage = findImageOrDie(imgstream, "kube-rbac-proxy")
		bootstrapOpts.infraImage = findImageOrDie(imgstream, "pod")
		bootstrapOpts.haproxyImage = findImageOrDie(imgstream, "haproxy-router")
		bootstrapOpts.baseOSContainerImage, err = findImage(imgstream, baseOSContainerImageTag)
		if err != nil {
			klog.Warningf("Base OS container not found: %s", err)
		}
		bootstrapOpts.baseOSExtensionsContainerImage, err = findImage(imgstream, fmt.Sprintf("%s-extensions", baseOSContainerImageTag))
		if err != nil {
			klog.Warningf("Base OS extensions container not found: %s", err)
		}
	}

	imgs := ctrlcommon.Images{
		RenderConfigImages: ctrlcommon.RenderConfigImages{
			MachineConfigOperator:        bootstrapOpts.mcoImage,
			KeepalivedBootstrap:          bootstrapOpts.keepalivedImage,
			CorednsBootstrap:             bootstrapOpts.corednsImage,
			BaremetalRuntimeCfgBootstrap: bootstrapOpts.baremetalRuntimeCfgImage,
			OauthProxy:                   bootstrapOpts.oauthProxyImage,
			KubeRbacProxy:                bootstrapOpts.kubeRbacProxyImage,
		},
		ControllerConfigImages: ctrlcommon.ControllerConfigImages{
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
		bootstrapOpts.mcsCAFile, bootstrapOpts.kubeCAFile, bootstrapOpts.pullSecretFile,
		&imgs,
		bootstrapOpts.destinationDir,
		bootstrapOpts.releaseImage,
	); err != nil {
		klog.Fatalf("error rendering bootstrap manifests: %v", err)
	}
}
