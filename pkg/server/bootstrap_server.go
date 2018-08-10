package server

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	yaml "gopkg.in/yaml.v2"
)

// ensure bootstrapServer implements the
// Server interface.
var _ = Server(&bootstrapServer{})

type bootstrapServer struct {

	// serverBaseDir is the root, relative to which
	// the machine pool, configs will be picked
	serverBaseDir string

	// serverKubeConfigPath is the path on the local machine from
	// where the kubeconfig should be read.
	serverKubeConfigPath string
}

// NewBootstrapServer initializes a new Bootstrap server that implements
// the Server interface.
func NewBootstrapServer(dir, kubeConf string) (Server, error) {
	if _, err := os.Stat(kubeConf); err != nil {
		return nil, fmt.Errorf("kubeconfig not found at location: %s", kubeConf)
	}
	return &bootstrapServer{
		serverBaseDir:        dir,
		serverKubeConfigPath: kubeConf,
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
// 4. Execute the etcd template function based on the etcd_index, if passed in the request .
// 5. Append the machine annotations file.
// 6. Append the KubeConfig file.
func (bsc *bootstrapServer) GetConfig(cr poolRequest) (*ignv2_2types.Config, error) {

	// 1. Read the Machine Config Pool object.
	fileName := path.Join(bsc.serverBaseDir, "machine-pools", cr.machinePool+".yaml")
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
	fileName = path.Join(bsc.serverBaseDir, "machine-configs", currConf+".yaml")
	data, err = ioutil.ReadFile(fileName)
	if os.IsNotExist(err) {
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

	// execute etcd templating.
	err = execEtcdTemplates(&mc.Spec.Config, cr.etcdIndex)
	if err != nil {
		return nil, fmt.Errorf("server: could not template etcd. err: %v", err)
	}

	// append machine annotations file.
	err = appendNodeAnnotations(&mc.Spec.Config, currConf)
	if err != nil {
		return nil, err
	}

	// append KubeConfig to Ignition.
	err = copyFileToIgnition(&mc.Spec.Config, defaultMachineKubeConfPath, bsc.serverKubeConfigPath)
	if err != nil {
		return nil, err
	}

	return &mc.Spec.Config, nil
}
