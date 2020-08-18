package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"

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
		baremetalRuntimeCfgImage  string
		cloudConfigFile           string
		configFile                string
		cloudProviderCAFile       string
		corednsImage              string
		destinationDir            string
		etcdCAFile                string
		etcdImage                 string
		etcdMetricCAFile          string
		haproxyImage              string
		imagesConfigMapFile       string
		infraConfigFile           string
		infraImage                string
		releaseImage              string
		keepalivedImage           string
		kubeCAFile                string
		kubeClientAgentImage      string
		clusterEtcdOperatorImage  string
		mcoImage                  string
		mdnsPublisherImage        string
		oauthProxyImage           string
		networkConfigFile         string
		oscontentImage            string
		pullSecretFile            string
		rootCAFile                string
		proxyConfigFile           string
		additionalTrustBundleFile string
	}
)

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.etcdCAFile, "etcd-ca", "/etc/ssl/etcd/ca.crt", "path to etcd CA certificate")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.etcdMetricCAFile, "etcd-metric-ca", "/assets/tls/etcd-metric-ca-bundle.crt", "path to etcd metric CA certificate")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.rootCAFile, "root-ca", "/etc/ssl/kubernetes/ca.crt", "path to root CA certificate")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.kubeCAFile, "kube-ca", "", "path to kube-apiserver serving-ca bundle")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.pullSecretFile, "pull-secret", "/assets/manifests/pull.json", "path to secret manifest that contains pull secret.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.destinationDir, "dest-dir", "", "The destination directory where MCO writes the manifests.")
	bootstrapCmd.MarkFlagRequired("dest-dir")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcoImage, "machine-config-operator-image", "", "Image for Machine Config Operator.")
	bootstrapCmd.MarkFlagRequired("machine-config-operator-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.oscontentImage, "machine-config-oscontent-image", "", "Image for osImageURL")
	bootstrapCmd.MarkFlagRequired("machine-config-oscontent-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.etcdImage, "etcd-image", "", "Image for Etcd.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.kubeClientAgentImage, "kube-client-agent-image", "", "Image for Kube Client Agent.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.clusterEtcdOperatorImage, "cluster-etcd-operator-image", "", "Image for Cluster Etcd Operator. An empty string here means the cluster boots without CEO.")
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
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.additionalTrustBundleFile, "additional-trust-bundle-config-file", "/assets/manifests/user-ca-bundle-config.yaml", "File containing the additional user provided CA bundle manifest.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.keepalivedImage, "keepalived-image", "", "Image for Keepalived.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.corednsImage, "coredns-image", "", "Image for CoreDNS.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mdnsPublisherImage, "mdns-publisher-image", "", "Image for mdns-publisher.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.haproxyImage, "haproxy-image", "", "Image for haproxy.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.baremetalRuntimeCfgImage, "baremetal-runtimecfg-image", "", "Image for baremetal-runtimecfg.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.oauthProxyImage, "oauth-proxy-image", "", "Image for origin oauth proxy.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.cloudProviderCAFile, "cloud-provider-ca-file", "", "path to cloud provider CA certificate")

}

func runBootstrapCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	imgs := operator.Images{
		RenderConfigImages: operator.RenderConfigImages{
			MachineConfigOperator:        bootstrapOpts.mcoImage,
			MachineOSContent:             bootstrapOpts.oscontentImage,
			KeepalivedBootstrap:          bootstrapOpts.keepalivedImage,
			CorednsBootstrap:             bootstrapOpts.corednsImage,
			BaremetalRuntimeCfgBootstrap: bootstrapOpts.baremetalRuntimeCfgImage,
			OauthProxy:                   bootstrapOpts.oauthProxyImage,
		},
		ControllerConfigImages: operator.ControllerConfigImages{
			Etcd:                bootstrapOpts.etcdImage,
			InfraImage:          bootstrapOpts.infraImage,
			KubeClientAgent:     bootstrapOpts.kubeClientAgentImage,
			ClusterEtcdOperator: bootstrapOpts.clusterEtcdOperatorImage,
			Keepalived:          bootstrapOpts.keepalivedImage,
			Coredns:             bootstrapOpts.corednsImage,
			MdnsPublisher:       bootstrapOpts.mdnsPublisherImage,
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
		bootstrapOpts.cloudConfigFile,
		bootstrapOpts.cloudProviderCAFile,
		bootstrapOpts.etcdCAFile, bootstrapOpts.etcdMetricCAFile, bootstrapOpts.rootCAFile, bootstrapOpts.kubeCAFile, bootstrapOpts.pullSecretFile,
		&imgs,
		bootstrapOpts.destinationDir,
		bootstrapOpts.releaseImage,
	); err != nil {
		glog.Fatalf("error rendering bootstrap manifests: %v", err)
	}
}
