package common

import (
	"os"

	"github.com/golang/glog"
	configv1 "github.com/openshift/api/config/v1"
	configclientset "github.com/openshift/client-go/config/clientset/versioned"
	apiext "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
)

// ClientBuilder can create a variety of kubernetes client interface
// with its embeded rest.Config.
type ClientBuilder struct {
	config *rest.Config
}

// MachineConfigClientOrDie returns the kubernetes client interface for machine config.
func (cb *ClientBuilder) MachineConfigClientOrDie(name string) mcfgclientset.Interface {
	return mcfgclientset.NewForConfigOrDie(rest.AddUserAgent(cb.config, name))
}

// KubeClientOrDie returns the kubernetes client interface for general kubernetes objects.
func (cb *ClientBuilder) KubeClientOrDie(name string) kubernetes.Interface {
	return kubernetes.NewForConfigOrDie(rest.AddUserAgent(cb.config, name))
}

// clusterOperatorsClient is a wrapper around the ClusterOperators client
// implementing the ClusterOperatorsClientInterface
type clusterOperatorsClient struct {
	innerConfigClient configclientset.Interface
}

// Create creates the cluster operator and returns it
func (c *clusterOperatorsClient) Create(co *configv1.ClusterOperator) (*configv1.ClusterOperator, error) {
	return c.innerConfigClient.ConfigV1().ClusterOperators().Create(co)
}

// UpdateStatus updates the status of the cluster operator and returns it
func (c *clusterOperatorsClient) UpdateStatus(co *configv1.ClusterOperator) (*configv1.ClusterOperator, error) {
	return c.innerConfigClient.ConfigV1().ClusterOperators().UpdateStatus(co)
}

// Get returns the cluster operator by name
func (c *clusterOperatorsClient) Get(name string, options metav1.GetOptions) (*configv1.ClusterOperator, error) {
	return c.innerConfigClient.ConfigV1().ClusterOperators().Get(name, options)
}

// ClusterOperatorsClientInterface is a controlled ClusterOperators client which is used
// by the operator and allows us to mock it in tests.
type ClusterOperatorsClientInterface interface {
	Create(*configv1.ClusterOperator) (*configv1.ClusterOperator, error)
	UpdateStatus(*configv1.ClusterOperator) (*configv1.ClusterOperator, error)
	Get(name string, options metav1.GetOptions) (*configv1.ClusterOperator, error)
}

// ClusterOperatorsClientOrDie returns the controlleed ClusterOperators client used by the operator
func (cb *ClientBuilder) ClusterOperatorsClientOrDie(name string) ClusterOperatorsClientInterface {
	cc := configclientset.NewForConfigOrDie(rest.AddUserAgent(cb.config, name))
	return &clusterOperatorsClient{innerConfigClient: cc}
}

// ConfigClientOrDie returns the kubernetes client interface for security related kubernetes objects
// such as pod security policy, security context.
func (cb *ClientBuilder) ConfigClientOrDie(name string) configclientset.Interface {
	return configclientset.NewForConfigOrDie(rest.AddUserAgent(cb.config, name))
}

// APIExtClientOrDie returns the kubernetes client interface for extended kubernetes objects.
func (cb *ClientBuilder) APIExtClientOrDie(name string) apiext.Interface {
	return apiext.NewForConfigOrDie(rest.AddUserAgent(cb.config, name))
}

// NewClientBuilder returns a *ClientBuilder with the given kubeconfig.
func NewClientBuilder(kubeconfig string) (*ClientBuilder, error) {
	var config *rest.Config
	var err error

	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}

	if kubeconfig != "" {
		glog.V(4).Infof("Loading kube client config from path %q", kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		glog.V(4).Infof("Using in-cluster kube client config")
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, err
	}

	return &ClientBuilder{
		config: config,
	}, nil
}
