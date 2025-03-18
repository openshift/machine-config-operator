package framework

import (
	"fmt"
	"os"

	clientbuildv1 "github.com/openshift/client-go/build/clientset/versioned/typed/build/v1"
	clientconfigv1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	clientimagev1 "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	clientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1"
	clientmachineconfigv1alpha1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/typed/machineconfiguration/v1alpha1"
	clientoperatorsv1alpha1 "github.com/openshift/client-go/operator/clientset/versioned/typed/operator/v1alpha1"
	clientapiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/client-go/kubernetes"
	clientset "k8s.io/client-go/kubernetes"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	batchv1client "k8s.io/client-go/kubernetes/typed/batch/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

type ClientSet struct {
	corev1client.CoreV1Interface
	batchv1client.BatchV1Interface
	appsv1client.AppsV1Interface
	clientconfigv1.ConfigV1Interface
	clientmachineconfigv1.MachineconfigurationV1Interface
	clientapiextensionsv1.ApiextensionsV1Interface
	clientoperatorsv1alpha1.OperatorV1alpha1Interface
	clientbuildv1.BuildV1Interface
	clientimagev1.ImageV1Interface
	clientmachineconfigv1alpha1.MachineconfigurationV1alpha1Interface
	kubeconfig string
	config     *rest.Config
	kubeclient clientset.Interface
	mcfgclient mcfgclientset.Interface
}

// Returns a copy of the config so that additional clients may be instantiated
// from it. By making a copy, callers are free to modify the config as needed.
func (cs *ClientSet) GetRestConfig() *rest.Config {
	return rest.CopyConfig(cs.config)
}

func (cs *ClientSet) GetKubeconfig() (string, error) {
	if cs.kubeconfig != "" {
		return cs.kubeconfig, nil
	}

	return "", fmt.Errorf("no kubeconfig found; are you running a custom config or in-cluster?")
}

func (cs *ClientSet) GetKubeclient() clientset.Interface {
	return cs.kubeclient
}

func (cs *ClientSet) GetMcfgclient() mcfgclientset.Interface {
	return cs.mcfgclient
}

// NewClientSet returns a *ClientBuilder with the given kubeconfig.
func NewClientSet(kubeconfig string) *ClientSet {
	var config *rest.Config
	var err error

	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}

	if kubeconfig != "" {
		klog.V(4).Infof("Loading kube client config from path %q", kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		klog.V(4).Infof("Using in-cluster kube client config")
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		panic(err)
	}

	cs := NewClientSetFromConfig(config)
	cs.kubeconfig = kubeconfig
	cs.config = config
	return cs
}

// NewClientSetFromConfig returns a *ClientBuilder with the given rest config.
func NewClientSetFromConfig(config *rest.Config) *ClientSet {
	kubeclient := kubernetes.NewForConfigOrDie(config)
	mcfgclient := mcfgclientset.NewForConfigOrDie(config)

	return &ClientSet{
		CoreV1Interface:                       kubeclient.CoreV1(),
		BatchV1Interface:                      kubeclient.BatchV1(),
		AppsV1Interface:                       kubeclient.AppsV1(),
		ConfigV1Interface:                     clientconfigv1.NewForConfigOrDie(config),
		MachineconfigurationV1Interface:       mcfgclient.MachineconfigurationV1(),
		ApiextensionsV1Interface:              clientapiextensionsv1.NewForConfigOrDie(config),
		OperatorV1alpha1Interface:             clientoperatorsv1alpha1.NewForConfigOrDie(config),
		BuildV1Interface:                      clientbuildv1.NewForConfigOrDie(config),
		ImageV1Interface:                      clientimagev1.NewForConfigOrDie(config),
		MachineconfigurationV1alpha1Interface: mcfgclient.MachineconfigurationV1alpha1(),
		config:                                config,
		kubeclient:                            kubeclient,
		mcfgclient:                            mcfgclient,
	}
}
