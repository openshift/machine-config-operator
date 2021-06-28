package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"

	yaml "github.com/ghodss/yaml"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	rest "k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	igntypes "github.com/coreos/ignition/v2/config/v3_2/types"

	v1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

const (
	// inClusterConfig tells the client to grab the config from the cluster it
	// is running on, instead of using a config passed to it.
	inClusterConfig = ""

	//nolint:gosec
	bootstrapTokenDir = "/etc/mcs/bootstrap-token"
)

// ensure clusterServer implements the
// Server interface.
var _ = Server(&clusterServer{})

type clusterServer struct {
	// machineClient is used to interact with the
	// machine config, pool objects.
	machineClient v1.MachineconfigurationV1Interface

	kubeconfigFunc kubeconfigFunc
}

// NewClusterServer is used to initialize the machine config
// server that will be used to fetch the requested MachineConfigPool
// objects from within the cluster.
// It accepts a kubeConfig, which is not required when it's
// run from within a cluster(useful in testing).
// It accepts the apiserverURL which is the location of the KubeAPIServer.
func NewClusterServer(kubeConfig, apiserverURL string) (Server, error) {
	restConfig, err := getClientConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("Failed to create Kubernetes rest client: %v", err)
	}

	mc := v1.NewForConfigOrDie(restConfig)
	return &clusterServer{
		machineClient:  mc,
		kubeconfigFunc: func() ([]byte, []byte, error) { return kubeconfigFromSecret(bootstrapTokenDir, apiserverURL) },
	}, nil
}

// getMachineConfig fetches the machine config from the cluster,
// based on the pool request.
func (cs *clusterServer) getMachineConfig(cr poolRequest) (*mcfgv1.MachineConfig, *igntypes.Config, error) {
	mp, err := cs.machineClient.MachineConfigPools().Get(context.TODO(), cr.machineConfigPool, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("could not fetch pool. err: %v", err)
	}

	// For new nodes, we roll out the latest if at least one node has successfully updated.
	// This avoids deadlocks in situations where the old configuration broke somehow
	// (e.g. pull secret expired)
	// and also avoids provisioning a new node, only to update it not long thereafter.
	var currConf string
	if mp.Status.UpdatedMachineCount > 0 {
		currConf = mp.Spec.Configuration.Name
	} else {
		currConf = mp.Status.Configuration.Name
	}

	mc, err := cs.machineClient.MachineConfigs().Get(context.TODO(), currConf, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("could not fetch config %s, err: %v", currConf, err)
	}
	ignConf, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing Ignition config failed with error: %v", err)
	}

	appenders := getAppenders(currConf, cr.version, cs.kubeconfigFunc)
	for _, a := range appenders {
		if err := a(&ignConf, mc); err != nil {
			return nil, nil, err
		}
	}

	return mc, &ignConf, nil
}

// GetConfig fetches the machine config(type - Ignition) from the cluster,
// based on the pool request.
func (cs *clusterServer) GetConfig(cr poolRequest) (*runtime.RawExtension, error) {
	_, ignConf, err := cs.getMachineConfig(cr)
	if err != nil {
		return nil, err
	}
	// shouldn't be possible, but do this to avoid chance of nil pointer dereference
	if ignConf == nil {
		return nil, fmt.Errorf("fetched empty Ignition config")
	}

	rawConf, err := json.Marshal(*ignConf)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: rawConf}, nil
}

// GetKernelArguments fetches the machine config kernel arguments from the cluster,
// based on the pool request.
func (cs *clusterServer) GetKernelArguments(cr poolRequest) ([]string, error) {
	mc, _, err := cs.getMachineConfig(cr)
	if err != nil {
		return nil, err
	}

	return mc.Spec.KernelArguments, nil
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

// kubeconfigFromSecret creates a kubeconfig with the certificate
// and token files in secretDir
func kubeconfigFromSecret(secretDir, apiserverURL string) ([]byte, []byte, error) {
	caFile := filepath.Join(secretDir, corev1.ServiceAccountRootCAKey)
	tokenFile := filepath.Join(secretDir, corev1.ServiceAccountTokenKey)
	caData, err := ioutil.ReadFile(caFile)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to read %s: %v", caFile, err)
	}
	token, err := ioutil.ReadFile(tokenFile)
	if err != nil {
		return nil, nil, fmt.Errorf("Failed to read %s: %v", tokenFile, err)
	}

	kubeconfig := clientcmdv1.Config{
		Clusters: []clientcmdv1.NamedCluster{{
			Name: "local",
			Cluster: clientcmdv1.Cluster{
				Server:                   apiserverURL,
				CertificateAuthorityData: caData,
			}},
		},
		AuthInfos: []clientcmdv1.NamedAuthInfo{{
			Name: "kubelet",
			AuthInfo: clientcmdv1.AuthInfo{
				Token: string(token),
			},
		}},
		Contexts: []clientcmdv1.NamedContext{{
			Name: "kubelet",
			Context: clientcmdv1.Context{
				Cluster:  "local",
				AuthInfo: "kubelet",
			},
		}},
		CurrentContext: "kubelet",
	}
	kcData, err := yaml.Marshal(kubeconfig)
	if err != nil {
		return nil, nil, err
	}
	return kcData, caData, nil
}
