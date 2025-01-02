package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path"

	yaml "github.com/ghodss/yaml"
	"k8s.io/apimachinery/pkg/runtime"
	clientcmd "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/klog/v2"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// ensure bootstrapServer implements the
// Server interface.
var _ = Server(&bootstrapServer{})

type bootstrapServer struct {

	// serverBaseDir is the root, relative to which
	// the MachineConfigPool configs will be picked
	serverBaseDir string

	kubeconfigFunc kubeconfigFunc

	certs []string
}

// NewBootstrapServer initializes a new Bootstrap server that implements
// the Server interface.
func NewBootstrapServer(dir, kubeconfig string, ircerts []string) (Server, error) {
	if _, err := os.Stat(kubeconfig); err != nil {
		return nil, fmt.Errorf("kubeconfig not found at location: %s", kubeconfig)
	}
	return &bootstrapServer{
		serverBaseDir:  dir,
		kubeconfigFunc: func() ([]byte, []byte, error) { return kubeconfigFromFile(kubeconfig) },
		certs:          ircerts,
	}, nil
}

// GetConfig fetches the machine config(type - Ignition) from the bootstrap server,
// based on the pool request.
// If a config cannot be found or parsed, it returns a nil conf, along with an error.
//
// The method does the following:
//
//  1. Read the machine config pool by using the following path template:
//     "<serverBaseDir>/machine-pools/<machineConfigPoolName>.yaml"
//
//  2. Read the currentConfig field from the Status and read the config file
//     using the following path template:
//     "<serverBaseDir>/machine-configs/<currentConfig>.yaml"
//
// 3. Load the machine config.
// 4. Append the machine annotations file.
// 5. Append the KubeConfig file.
const yamlExt = ".yaml"

func (bsc *bootstrapServer) GetConfig(cr poolRequest) (*runtime.RawExtension, error) {
	if cr.machineConfigPool != "master" && cr.machineConfigPool != "arbiter" {
		return nil, fmt.Errorf("refusing to serve bootstrap configuration to pool %q", cr.machineConfigPool)
	}
	// 1. Read the Machine Config Pool object.
	fileName := path.Join(bsc.serverBaseDir, "machine-pools", cr.machineConfigPool+yamlExt)
	klog.Infof("reading file %q", fileName)
	data, err := os.ReadFile(fileName)
	if os.IsNotExist(err) {
		klog.Errorf("could not find file: %s", fileName)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("server: could not read file %s, err: %w", fileName, err)
	}

	mp := new(mcfgv1.MachineConfigPool)
	err = yaml.Unmarshal(data, mp)
	if err != nil {
		return nil, fmt.Errorf("server: could not unmarshal file %s, err: %w", fileName, err)
	}

	currConf := mp.Status.Configuration.Name

	// 2. Read the Machine Config object.
	fileName = path.Join(bsc.serverBaseDir, "machine-configs", currConf+yamlExt)
	klog.Infof("reading file %q", fileName)
	data, err = os.ReadFile(fileName)
	if os.IsNotExist(err) {
		klog.Errorf("could not find file: %s", fileName)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("server: could not read file %s, err: %w", fileName, err)
	}

	mc := new(mcfgv1.MachineConfig)
	err = yaml.Unmarshal(data, mc)
	if err != nil {
		return nil, fmt.Errorf("server: could not unmarshal file %s, err: %w", fileName, err)
	}
	ignConf, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing Ignition config failed with error: %w", err)
	}

	// strip the kargs out if we're going back to a version that doesn't support it
	if err := MigrateKernelArgsIfNecessary(&ignConf, mc, cr.version); err != nil {
		return nil, fmt.Errorf("failed to migrate kernel args %w", err)
	}

	fileName = path.Join(bsc.serverBaseDir, "controller-config", "machine-config-controller.yaml")
	klog.Infof("reading file %q", fileName)
	data, err = os.ReadFile(fileName)
	if os.IsNotExist(err) {
		klog.Errorf("could not find file: %s", fileName)
		return nil, fmt.Errorf("could not find controller config on initial bootstrap")
	}
	if err != nil {
		klog.Errorf("could not read file: %s", fileName)
		return nil, fmt.Errorf("server: could not read file %s, err: %w", fileName, err)
	}
	klog.Infof("got controllerConfig: %s", string(data))

	cc := new(mcfgv1.ControllerConfig)
	err = yaml.Unmarshal(data, cc)
	if err != nil {
		klog.Errorf("could not unmarshal file: %s", fileName)
		return nil, fmt.Errorf("server: could not unmarshal file %s, err: %w", fileName, err)
	}

	addDataAndMaybeAppendToIgnition(caBundleFilePath, cc.Spec.KubeAPIServerServingCAData, &ignConf)
	addDataAndMaybeAppendToIgnition(cloudProviderCAPath, cc.Spec.CloudProviderCAData, &ignConf)
	appenders := getAppenders(currConf, nil, bsc.kubeconfigFunc, bsc.certs, bsc.serverBaseDir)
	for _, a := range appenders {
		if err := a(&ignConf, mc); err != nil {
			return nil, err
		}
	}

	rawConf, err := json.Marshal(ignConf)
	if err != nil {
		klog.Errorf("could not marshal ignConf %v", err)
		return nil, err
	}
	klog.Infof("got ignconf %s", rawConf)
	return &runtime.RawExtension{Raw: rawConf}, nil
}

func kubeconfigFromFile(path string) ([]byte, []byte, error) {
	kcData, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting kubeconfig from disk: %w", err)
	}

	kc := clientcmd.Config{}
	if err := yaml.Unmarshal(kcData, &kc); err != nil {
		return nil, nil, err
	}
	return kcData, kc.Clusters[0].Cluster.CertificateAuthorityData, nil
}
