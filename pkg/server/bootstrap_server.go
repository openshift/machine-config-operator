package server

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"

	yaml "github.com/ghodss/yaml"
	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/runtime"
	clientcmd "k8s.io/client-go/tools/clientcmd/api/v1"
	igntypes "github.com/coreos/ignition/v2/config/v3_2/types"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
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

// getMachineConfig fetches the machine config from the bootstrap server,
// based on the pool request.
// If a config cannot be found or parsed, it returns a nil conf, along with an error.
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
func (bsc *bootstrapServer) getMachineConfig(cr poolRequest) (*mcfgv1.MachineConfig, *igntypes.Config, error) {
	if cr.machineConfigPool != "master" {
		return nil, nil, fmt.Errorf("refusing to serve bootstrap configuration to pool %q", cr.machineConfigPool)
	}
	// 1. Read the Machine Config Pool object.
	fileName := path.Join(bsc.serverBaseDir, "machine-pools", cr.machineConfigPool+".yaml")
	glog.Infof("reading file %q", fileName)
	data, err := ioutil.ReadFile(fileName)
	if os.IsNotExist(err) {
		glog.Errorf("could not find file: %s", fileName)
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("server: could not read file %s, err: %v", fileName, err)
	}

	mp := new(mcfgv1.MachineConfigPool)
	err = yaml.Unmarshal(data, mp)
	if err != nil {
		return nil, nil, fmt.Errorf("server: could not unmarshal file %s, err: %v", fileName, err)
	}

	currConf := mp.Status.Configuration.Name

	// 2. Read the Machine Config object.
	fileName = path.Join(bsc.serverBaseDir, "machine-configs", currConf+".yaml")
	glog.Infof("reading file %q", fileName)
	data, err = ioutil.ReadFile(fileName)
	if os.IsNotExist(err) {
		glog.Errorf("could not find file: %s", fileName)
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("server: could not read file %s, err: %v", fileName, err)
	}

	mc := new(mcfgv1.MachineConfig)
	err = yaml.Unmarshal(data, mc)
	if err != nil {
		return nil, nil, fmt.Errorf("server: could not unmarshal file %s, err: %v", fileName, err)
	}
	ignConf, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing Ignition config failed with error: %v", err)
	}

	appenders := getAppenders(currConf, nil, bsc.kubeconfigFunc)
	for _, a := range appenders {
		if err := a(&ignConf, mc); err != nil {
			return nil, nil, err
		}
	}

	return mc, &ignConf, nil
}

// GetConfig fetches the machine conf(type - Ignition) based on the pool request
func (bsc *bootstrapServer) GetConfig(cr poolRequest) (*runtime.RawExtension, error) {
	_, ignConf, err := bsc.getMachineConfig(cr)
	if err != nil {
		return nil, err
	}
	// exit without error if the machine config file was not readable and resulted in no
	// ignition config being fetched
	if ignConf == nil {
		return nil, nil
	}

	rawConf, err := json.Marshal(ignConf)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: rawConf}, nil
}

// GetKernelArguments fetches the machine config kernel arguments based on the pool request
func (bsc *bootstrapServer) GetKernelArguments(cr poolRequest) ([]string, error) {
	mc, _, err := bsc.getMachineConfig(cr)
	if err != nil {
		return nil, err
	}
	// exit without error if the machine config file was not readable and resulted in
	// no machine config being fetched
	if mc == nil {
		return nil, nil
	}

	return mc.Spec.KernelArguments, nil
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
