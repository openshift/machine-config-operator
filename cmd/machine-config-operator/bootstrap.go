package main

import (
	"encoding/json"
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
		etcdCAFile          string
		rootCAFile          string
		pullSecretFile      string
		configFile          string
		osimgConfigMapFile  string
		imagesConfigMapFile string
		mccImage            string
		mcsImage            string
		mcdImage            string
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
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mccImage, "machine-config-controller-image", "", "Image for Machine Config Controller. (this cannot be set if --images-json-configmap is set)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcsImage, "machine-config-server-image", "", "Image for Machine Config Server. (this cannot be set if --images-json-configmap is set)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.mcdImage, "machine-config-daemon-image", "", "Image for Machine Config Daemon. (this cannot be set if --images-json-configmap is set)")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.configFile, "config-file", "", "ClusterConfig ConfigMap file.")
	bootstrapCmd.PersistentFlags().StringVar(&bootstrapOpts.osimgConfigMapFile, "osimg-config-file", "", "ConfigMap containing osImageURL")
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

	if bootstrapOpts.imagesConfigMapFile != "" &&
		(bootstrapOpts.mccImage != "" ||
			bootstrapOpts.mcsImage != "" ||
			bootstrapOpts.mcdImage != "") {
		glog.Fatal("both --images-json-configmap and --machine-config-{controller,server,daemon}-image flags cannot be set")
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
	} else {
		imgs.MachineConfigController = bootstrapOpts.mccImage
		imgs.MachineConfigServer = bootstrapOpts.mcsImage
		imgs.MachineConfigDaemon = bootstrapOpts.mcdImage
	}

	if err := operator.RenderBootstrap(
		bootstrapOpts.configFile,
		bootstrapOpts.etcdCAFile, bootstrapOpts.rootCAFile, bootstrapOpts.pullSecretFile,
		imgs, bootstrapOpts.osimgConfigMapFile,
		bootstrapOpts.destinationDir,
	); err != nil {
		glog.Fatalf("error rendering bootstrap manifests: %v", err)
	}
}

func rawImagesFromConfigMapOnDisk(file string) ([]byte, error) {
	cm, err := operator.DecodeConfigMap(file)
	if err != nil {
		return nil, err
	}
	return []byte(cm.Data["images.json"]), nil
}
