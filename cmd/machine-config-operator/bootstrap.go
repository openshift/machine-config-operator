package main

import (
	"encoding/json"
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
		etcdCAFile          string
		rootCAFile          string
		pullSecretFile      string
		configFile          string
		oscontentImage      string
		imagesConfigMapFile string
		mccImage            string
		mcsImage            string
		mcdImage            string
		etcdImage           string
		setupEtcdEnvImage   string
		destinationDir      string
	}
)

func init() {
	rootCmd.AddCommand(bootstrapCmd)
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.etcdCAFile, "etcd-ca", "/etc/ssl/etcd/ca.crt", "path to etcd CA certificate")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.rootCAFile, "root-ca", "/etc/ssl/kubernetes/ca.crt", "path to root CA certificate")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.pullSecretFile, "pull-secret", "/assets/manifests/pull.json", "path to secret manifest that contains pull secret.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.destinationDir, "dest-dir", "", "The destination directory where MCO writes the manifests.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.imagesConfigMapFile, "images-json-configmap", "", "ConfigMap that contains images.json for MCO.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mccImage, "machine-config-controller-image", "", "Image for Machine Config Controller. (this overrides the image from --images-json-configmap)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcsImage, "machine-config-server-image", "", "Image for Machine Config Server. (this overrides the image from --images-json-configmap)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcdImage, "machine-config-daemon-image", "", "Image for Machine Config Daemon. (this overrides the image from --images-json-configmap)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.oscontentImage, "machine-config-oscontent-image", "", "Image for osImageURL")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.etcdImage, "etcd-image", "", "Image for Etcd. (this overrides the image from --images-json-configmap)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.setupEtcdEnvImage, "setup-etcd-env-image", "", "Image for Setup Etcd Environment. (this overrides the image from --images-json-configmap)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.configFile, "config-file", "", "ClusterConfig ConfigMap file.")
}

func runBootstrapCmd(cmd *cobra.Command, args []string) {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v", version.Version)

	if bootstrapOpts.destinationDir == "" {
		glog.Fatal("--dest-dir cannot be empty")
	}

	if bootstrapOpts.configFile == "" {
		glog.Fatal("--config-file cannot be empty")
	}

	imgs := operator.DefaultImages()
	if bootstrapOpts.imagesConfigMapFile != "" {
		imgsRaw, err := rawImagesFromConfigMapOnDisk(bootstrapOpts.imagesConfigMapFile)
		if err != nil {
			glog.Fatal(err)
		}
		if err := json.Unmarshal([]byte(imgsRaw), &imgs); err != nil {
			glog.Fatal(err)
		}
	}
	if bootstrapOpts.mccImage != "" {
		imgs.MachineConfigController = bootstrapOpts.mccImage
	}
	if bootstrapOpts.mcsImage != "" {
		imgs.MachineConfigServer = bootstrapOpts.mcsImage
	}
	if bootstrapOpts.mcdImage != "" {
		imgs.MachineConfigDaemon = bootstrapOpts.mcdImage
	}
	if bootstrapOpts.etcdImage != "" {
		imgs.Etcd = bootstrapOpts.etcdImage
	}
	if bootstrapOpts.setupEtcdEnvImage != "" {
		imgs.SetupEtcdEnv = bootstrapOpts.setupEtcdEnvImage
	}
	if bootstrapOpts.oscontentImage != "" {
		imgs.MachineOSContent = bootstrapOpts.oscontentImage
	}

	if err := operator.RenderBootstrap(
		bootstrapOpts.configFile,
		bootstrapOpts.etcdCAFile, bootstrapOpts.rootCAFile, bootstrapOpts.pullSecretFile,
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
