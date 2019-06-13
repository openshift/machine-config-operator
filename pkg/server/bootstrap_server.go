package server

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	yaml "github.com/ghodss/yaml"
	"github.com/golang/glog"
	clientcmd "k8s.io/client-go/tools/clientcmd/api/v1"

	v1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

// ensure bootstrapServer implements the
// Server interface.
var _ = Server(&bootstrapServer{})

type bootstrapServer struct {

	// serverBaseDir is the root, relative to which
	// the MachineConfigPool configs will be picked
	serverBaseDir string

	kubeconfigFunc kubeconfigFunc
}

// NewBootstrapServer initializes a new Bootstrap server that implements
// the Server interface.
func NewBootstrapServer(dir, kubeconfig string) (Server, error) {
	if _, err := os.Stat(kubeconfig); err != nil {
		return nil, fmt.Errorf("kubeconfig not found at location: %s", kubeconfig)
	}
	return &bootstrapServer{
		serverBaseDir:  dir,
		kubeconfigFunc: func() ([]byte, []byte, error) { return kubeconfigFromFile(kubeconfig) },
	}, nil
}

// GetConfig fetches the machine config(type - Ignition) from the bootstrap server,
// based on the pool request.
// It returns nil for conf, error if the config isn't found. It returns a formatted
// error if any other error is encountered during its operations.
//
// The method does the following:
//
// 1. Read the machine config pool by using the following path template:
// 		"<serverBaseDir>/machine-pools/<machineConfigPoolName>.yaml"
//
// 2. Read the currentConfig field from the Status and read the config file
//    using the following path template:
// 		"<serverBaseDir>/machine-configs/<currentConfig>.yaml"
//
// 3. Load the machine config.
// 4. Append the machine annotations file.
// 5. Append the KubeConfig file.
func (bsc *bootstrapServer) GetConfig(cr poolRequest) (*igntypes.Config, error) {

	// 1. Read the Machine Config Pool object.
	fileName := path.Join(bsc.serverBaseDir, "machine-pools", cr.machineConfigPool+".yaml")
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

	currConf := mp.Status.Configuration.Name

	// 2. Read the Machine Config object.
	fileName = path.Join(bsc.serverBaseDir, "machine-configs", currConf+".yaml")
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

	appenders := getAppenders(currConf, bsc.kubeconfigFunc, mc.Spec.OSImageURL)
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
