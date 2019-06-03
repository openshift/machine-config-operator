package main

import (
	"flag"
	"fmt"
	"io/ioutil"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"

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
		cloudConfigFile      string
		configFile           string
		destinationDir       string
		etcdCAFile           string
		etcdImage            string
		etcdMetricCAFile     string
		imagesConfigMapFile  string
		infraConfigFile      string
		infraImage           string
		kubeCAFile           string
		kubeClientAgentImage string
		mccImage             string
		mcdImage             string
		mcsImage             string
		networkConfigFile    string
		oscontentImage       string
		pullSecretFile       string
		rootCAFile           string
		setupEtcdEnvImage    string
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
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mccImage, "machine-config-controller-image", "", "Image for Machine Config Controller.")
	bootstrapCmd.MarkFlagRequired("machine-config-controller-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcsImage, "machine-config-server-image", "", "Image for Machine Config Server.")
	bootstrapCmd.MarkFlagRequired("machine-config-server-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcdImage, "machine-config-daemon-image", "", "Image for Machine Config Daemon.")
	bootstrapCmd.MarkFlagRequired("machine-config-daemon-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.oscontentImage, "machine-config-oscontent-image", "", "Image for osImageURL")
	bootstrapCmd.MarkFlagRequired("machine-config-oscontent-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.etcdImage, "etcd-image", "", "Image for Etcd.")
	bootstrapCmd.MarkFlagRequired("etcd-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.setupEtcdEnvImage, "setup-etcd-env-image", "", "Image for Setup etcd Environment.")
	bootstrapCmd.MarkFlagRequired("setup-etcd-env-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.kubeClientAgentImage, "kube-client-agent-image", "", "Image for Kube Client Agent.")
	bootstrapCmd.MarkFlagRequired("kube-client-agent-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.infraImage, "infra-image", "", "Image for Infra Containers.")
	bootstrapCmd.MarkFlagRequired("infra-image")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.configFile, "config-file", "", "ClusterConfig ConfigMap file.")
	bootstrapCmd.MarkFlagRequired("config-file")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.infraConfigFile, "infra-config-file", "/assets/manifests/cluster-infrastructure-02-config.yaml", "File containing infrastructure.config.openshift.io manifest.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.networkConfigFile, "network-config-file", "/assets/manifests/cluster-network-02-config.yaml", "File containing network.config.openshift.io manifest.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.cloudConfigFile, "cloud-config-file", "", "File containing the config map that contains the cloud config for cloudprovider.")
}

func runBootstrapCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Version, version.Hash)

	imgs := operator.Images{
		MachineConfigController: bootstrapOpts.mccImage,
		MachineConfigDaemon:     bootstrapOpts.mcdImage,
		MachineConfigServer:     bootstrapOpts.mcsImage,
		MachineOSContent:        bootstrapOpts.oscontentImage,
		Etcd:                    bootstrapOpts.etcdImage,
		SetupEtcdEnv:            bootstrapOpts.setupEtcdEnvImage,
		InfraImage:              bootstrapOpts.infraImage,
		KubeClientAgent:         bootstrapOpts.kubeClientAgentImage,
	}

	if err := operator.RenderBootstrap(
		bootstrapOpts.configFile,
		bootstrapOpts.infraConfigFile, bootstrapOpts.networkConfigFile,
		bootstrapOpts.cloudConfigFile,
		bootstrapOpts.etcdCAFile, bootstrapOpts.etcdMetricCAFile, bootstrapOpts.rootCAFile, bootstrapOpts.kubeCAFile, bootstrapOpts.pullSecretFile,
		imgs,
		bootstrapOpts.destinationDir,
	); err != nil {
		glog.Fatalf("error rendering bootstrap manifests: %v", err)
	}
}

func rawImagesFromConfigMapOnDisk(file string) ([]byte, error) {
	data, err := ioutil.ReadFile(bootstrapOpts.imagesConfigMapFile)
	if err != nil {
		return nil, err
	}
	obji, err := runtime.Decode(scheme.Codecs.UniversalDecoder(corev1.SchemeGroupVersion), data)
	if err != nil {
		return nil, err
	}
	cm, ok := obji.(*corev1.ConfigMap)
	if !ok {
		return nil, fmt.Errorf("expected *corev1.ConfigMap found %T", obji)
	}
	return []byte(cm.Data["images.json"]), nil
}
