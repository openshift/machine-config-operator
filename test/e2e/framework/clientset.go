package framework

import (
	"os"

	"github.com/golang/glog"
	clientconfigv1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	clientmachineconfigv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	clientapiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1beta1"
	clientcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	clientappsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type ClientSet struct {
	clientcorev1.CoreV1Interface
	clientappsv1.AppsV1Interface
	clientconfigv1.ConfigV1Interface
	clientmachineconfigv1.MachineconfigurationV1Interface
	clientapiextensionsv1beta1.ApiextensionsV1beta1Interface
}

// NewClientSet returns a *ClientBuilder with the given kubeconfig.
func NewClientSet(kubeconfig string) (*ClientSet) {
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
		panic(err)
	}

	clientSet := &ClientSet{}
	clientSet.CoreV1Interface = clientcorev1.NewForConfigOrDie(config)
	clientSet.ConfigV1Interface = clientconfigv1.NewForConfigOrDie(config)
	clientSet.MachineconfigurationV1Interface = clientmachineconfigv1.NewForConfigOrDie(config)
	clientSet.ApiextensionsV1beta1Interface = clientapiextensionsv1beta1.NewForConfigOrDie(config)
	clientSet.AppsV1Interface = clientappsv1.NewForConfigOrDie(config)

	return clientSet
}
