package server

import (
	"fmt"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	rest "k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// inClusterConfig tells the client to grab the config from the cluster it
	// is running on, instead of using a config passed to it.
	inClusterConfig = ""
)

// ensure clusterServer implements the
// Server interface.
var _ = Server(&clusterServer{})

type clusterServer struct {

	// machineClient is used to interact with the
	// machine config, pool objects.
	machineClient v1.MachineconfigurationV1Interface

	// serverKubeConfPath is the path on the local machine from
	// where the kubeconfig should be read.
	serverKubeConfigPath string
}

// NewClusterServer is used to initialize the machine config
// server that will be used to fetch the requested machine pool
// objects from within the cluster.
// It accepts the kubeConfig which is not required when it's
// run from within the cluster(useful in testing).
func NewClusterServer(kubeConfig string) (Server, error) {
	restConfig, err := getClientConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("Failed to create Kubernetes rest client: %v", err)
	}

	mc := v1.NewForConfigOrDie(restConfig)
	return &clusterServer{
		machineClient:        mc,
		serverKubeConfigPath: kubeConfig,
	}, nil
}

// GetConfig fetches the machine config(type - Ignition) from the cluster,
// based on the pool request.
func (cs *clusterServer) GetConfig(cr poolRequest) (*ignv2_2types.Config, error) {
	mp, err := cs.machineClient.MachineConfigPools().Get(cr.machinePool, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not fetch pool. err: %v", err)
	}

	currConf := mp.Status.CurrentMachineConfig

	mc, err := cs.machineClient.MachineConfigs().Get(currConf, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not fetch config %s, err: %v", currConf, err)
	}

	err = execEtcdTemplates(&mc.Spec.Config, cr.etcdIndex)
	if err != nil {
		return nil, fmt.Errorf("server: could not template etcd. err: %v", err)
	}

	err = appendNodeAnnotations(&mc.Spec.Config, currConf)
	if err != nil {
		return nil, err
	}

	// append KubeConfig to Ignition.
	err = copyFileToIgnition(&mc.Spec.Config, defaultMachineKubeConfPath, cs.serverKubeConfigPath)
	if err != nil {
		return nil, err
	}

	return &mc.Spec.Config, nil
}

// getClientConfig returns a Kubernetes client Config.
func getClientConfig(path string) (*rest.Config, error) {
	if path != inClusterConfig {
		// build Config from a kubeconfig filepath
		return clientcmd.BuildConfigFromFlags("", path)
	}
	// uses pod's service account to get a Config
	return rest.InClusterConfig()
}
