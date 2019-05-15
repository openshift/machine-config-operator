package server

import (
	"fmt"
	"io/ioutil"
	"net/url"
	"path/filepath"

	ignition "github.com/coreos/ignition/config/v2_2/types"
	"github.com/vincent-petithory/dataurl"
	yaml "github.com/ghodss/yaml"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	rest "k8s.io/client-go/rest"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"

	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
)

const (
	// inClusterConfig tells the client to grab the config from the cluster it
	// is running on, instead of using a config passed to it.
	inClusterConfig = ""

	bootstrapTokenDir = "/etc/mcs/bootstrap-token"
)

// ensure clusterServer implements the
// Server interface.
var _ = Server(&clusterServer{})

type clusterServer struct {
	// apiServerHost is the hostname of the API server
	apiServerHost string
	// rootCA is the root CA
	rootCA []byte

	// machineClient is used to interact with the
	// machine config, pool objects.
	machineClient v1.MachineconfigurationV1Interface

	kubeconfigFunc kubeconfigFunc
}

// NewClusterServer is used to initialize the machine config
// server that will be used to fetch the requested MachineConfigPool
// objects from within the cluster.
// It accepts the kubeConfig which is not required when it's
// run from within the cluster(useful in testing).
// It accepts the apiserverURL which is the location of the KubeAPIServer.
func NewClusterServer(kubeConfig, apiserverURL string) (Server, error) {
	restConfig, err := getClientConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("Failed to create Kubernetes rest client: %v", err)
	}

	rootCA, err := ioutil.ReadFile("/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		return nil, err
	}

	apiURL, err := url.Parse(apiserverURL)
	if err != nil {
		return nil, err
	}

	mc := v1.NewForConfigOrDie(restConfig)
	return &clusterServer{
		apiServerHost:  apiURL.Hostname(),
		rootCA:         rootCA,
		machineClient:  mc,
		kubeconfigFunc: func() ([]byte, []byte, error) { return kubeconfigFromSecret(bootstrapTokenDir, apiserverURL) },
	}, nil
}

// GetPointerConfig ends up as e.g. user-data in AWS.
func (cs *clusterServer) GetPointerConfig(cr poolRequest) (*ignition.Config, error) {
	return &ignition.Config{
		Ignition: ignition.Ignition{
			Version: ignition.MaxVersion.String(),
			Config: ignition.IgnitionConfig{
				Append: []ignition.ConfigReference{{
					Source: func() *url.URL {
						return &url.URL{
							Scheme: "https",
							Host:   fmt.Sprintf("%s:22623", cs.apiServerHost),
							Path:   fmt.Sprintf("/config/%s", cr.machineConfigPool),
						}
					}().String(),
				}},
			},
			Security: ignition.Security{
				TLS: ignition.TLS{
					CertificateAuthorities: []ignition.CaReference{{
						Source: dataurl.EncodeBytes([]byte(cs.rootCA)),
					}},
				},
			},
		},
	}, nil
}

// GetConfig fetches the machine config(type - Ignition) from the cluster,
// based on the pool request.
func (cs *clusterServer) GetConfig(cr poolRequest) (*ignition.Config, error) {
	mp, err := cs.machineClient.MachineConfigPools().Get(cr.machineConfigPool, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not fetch pool. err: %v", err)
	}

	currConf := mp.Status.Configuration.Name

	mc, err := cs.machineClient.MachineConfigs().Get(currConf, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not fetch config %s, err: %v", currConf, err)
	}

	appenders := getAppenders(cr, currConf, cs.kubeconfigFunc, mc.Spec.OSImageURL)
	for _, a := range appenders {
		if err := a(&mc.Spec.Config); err != nil {
			return nil, err
		}
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

func kubeconfigFromSecret(secertDir string, apiserverURL string) ([]byte, []byte, error) {
	caFile := filepath.Join(secertDir, corev1.ServiceAccountRootCAKey)
	tokenFile := filepath.Join(secertDir, corev1.ServiceAccountTokenKey)
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
				Server: apiserverURL,
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
