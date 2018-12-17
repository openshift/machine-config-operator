package server

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	yaml "github.com/ghodss/yaml"
	"github.com/golang/glog"
	clientcmd "k8s.io/client-go/tools/clientcmd/api/v1"

	"github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

type bootstrapConfig struct {

	// configBaseDir is the root, relative to which
	// the machine pool, configs will be picked
	configBaseDir string

	kubeconfigFunc kubeconfigFunc
}

// NewBootstrapServer initializes a new Server that loads
// its configuration from the local filesystem.
func NewBootstrapServer(dir, kubeconfig string) (*http.Server, error) {
	if _, err := os.Stat(kubeconfig); err != nil {
		return nil, fmt.Errorf("kubeconfig not found at location: %s", kubeconfig)
	}
	config := &bootstrapConfig{
		configBaseDir:  dir,
		kubeconfigFunc: func() ([]byte, []byte, error) { return kubeconfigFromFile(kubeconfig) },
	}

	return &http.Server{
		Handler: newHandler(config.getConfig),
	}, nil
}

// getConfig fetches the machine config(type - Ignition) from the bootstrap server,
// based on the pool request.
// It returns nil for conf, error if the config isn't found. It returns a formatted
// error if any other error is encountered during its operations.
//
// The method does the following:
//
// 1. Read the machine config pool by using the following path template:
// 		"<configBaseDir>/machine-pools/<machineConfigPoolName>.yaml"
//
// 2. Read the currentConfig field from the Status and read the config file
//    using the following path template:
// 		"<configBaseDir>/machine-configs/<currentConfig>.yaml"
//
// 3. Load the machine config.
// 4. Append the machine annotations file.
// 5. Append the KubeConfig file.
func (bsc *bootstrapConfig) getConfig(cr poolRequest) (*ignv2_2types.Config, error) {

	// 1. Read the Machine Config Pool object.
	fileName := path.Join(bsc.configBaseDir, "machine-pools", cr.machinePool+".yaml")
	glog.Infof("reading file %q", fileName)
	data, err := ioutil.ReadFile(fileName)
	if os.IsNotExist(err) {
		glog.Errorf("could not find file: %s", fileName)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("server: could not read file %s, err: %v", fileName, err)
	}

	mp := new(v1.MachineConfigPool)
	err = yaml.Unmarshal(data, mp)
	if err != nil {
		return nil, fmt.Errorf("server: could not unmarshal file %s, err: %v", fileName, err)
	}

	currConf := mp.Status.CurrentMachineConfig

	// 2. Read the Machine Config object.
	fileName = path.Join(bsc.configBaseDir, "machine-configs", currConf+".yaml")
	glog.Infof("reading file %q", fileName)
	data, err = ioutil.ReadFile(fileName)
	if os.IsNotExist(err) {
		glog.Errorf("could not find file: %s", fileName)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("server: could not read file %s, err: %v", fileName, err)
	}

	mc := new(v1.MachineConfig)
	err = yaml.Unmarshal(data, mc)
	if err != nil {
		return nil, fmt.Errorf("server: could not unmarshal file %s, err: %v", fileName, err)
	}

	appenders := getAppenders(cr, currConf, bsc.kubeconfigFunc)
	for _, a := range appenders {
		if err := a(&mc.Spec.Config); err != nil {
			return nil, err
		}
	}
	return &mc.Spec.Config, nil
}

func kubeconfigFromFile(path string) ([]byte, []byte, error) {
	kcData, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting kubeconfig from disk: %v", err)
	}

	kc := clientcmd.Config{}
	if err := yaml.Unmarshal(kcData, &kc); err != nil {
		return nil, nil, err
	}
	return kcData, kc.Clusters[0].Cluster.CertificateAuthorityData, nil
}
