package framework

import (
	"fmt"
	"os"

	"github.com/golang/glog"
	fakeclientbuildv1 "github.com/openshift/client-go/build/clientset/versioned/fake"
	clientbuildv1 "github.com/openshift/client-go/build/clientset/versioned/typed/build/v1"
	fakeclientconfigv1 "github.com/openshift/client-go/config/clientset/versioned/fake"
	clientconfigv1 "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	fakeclientimagev1 "github.com/openshift/client-go/image/clientset/versioned/fake"
	clientimagev1 "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	fakeclientoperatorsv1alpha1 "github.com/openshift/client-go/operator/clientset/versioned/fake"
	clientoperatorsv1alpha1 "github.com/openshift/client-go/operator/clientset/versioned/typed/operator/v1alpha1"
	fakeclientmachineconfigv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	clientmachineconfigv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/typed/machineconfiguration.openshift.io/v1"
	fakeclientapiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	clientapiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeclientset "k8s.io/client-go/kubernetes"
	fakekubeclientset "k8s.io/client-go/kubernetes/fake"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type ClientSet struct {
	corev1client.CoreV1Interface
	appsv1client.AppsV1Interface
	clientconfigv1.ConfigV1Interface
	clientmachineconfigv1.MachineconfigurationV1Interface
	clientapiextensionsv1.ApiextensionsV1Interface
	clientoperatorsv1alpha1.OperatorV1alpha1Interface
	clientbuildv1.BuildV1Interface
	clientimagev1.ImageV1Interface
	kubeconfig    string
	kubeClientSet kubeclientset.Interface
}

func (cs *ClientSet) GetKubeconfig() (string, error) {
	if cs.kubeconfig != "" {
		return cs.kubeconfig, nil
	}

	return "", fmt.Errorf("no kubeconfig found; are you running a custom config or in-cluster?")
}

// Returns the full Kubernetes clientset.
func (cs *ClientSet) GetKubeClientset() kubeclientset.Interface {
	return cs.kubeClientSet
}

// NewClientSet returns a *ClientBuilder with the given kubeconfig.
func NewClientSet(kubeconfig string) *ClientSet {
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

	cs := NewClientSetFromConfig(config)
	cs.kubeconfig = kubeconfig
	return cs
}

// NewClientSetFromConfig returns a *ClientBuilder with the given rest config.
func NewClientSetFromConfig(config *rest.Config) *ClientSet {
	kubeClientSet := kubeclientset.NewForConfigOrDie(config)

	return &ClientSet{
		CoreV1Interface:                 kubeClientSet.CoreV1(),
		AppsV1Interface:                 kubeClientSet.AppsV1(),
		ConfigV1Interface:               clientconfigv1.NewForConfigOrDie(config),
		MachineconfigurationV1Interface: clientmachineconfigv1.NewForConfigOrDie(config),
		ApiextensionsV1Interface:        clientapiextensionsv1.NewForConfigOrDie(config),
		OperatorV1alpha1Interface:       clientoperatorsv1alpha1.NewForConfigOrDie(config),
		BuildV1Interface:                clientbuildv1.NewForConfigOrDie(config),
		ImageV1Interface:                clientimagev1.NewForConfigOrDie(config),
		kubeClientSet:                   kubeClientSet,
	}
}

// Creates a ClientSet with all fake clients. Accepts multiple (optional) runtime.Object params which will be created in all of the individual clients.
func NewFakeClientSet(objects ...runtime.Object) *ClientSet {
	fakeKubeClientSet := fakekubeclientset.NewSimpleClientset(objects...)

	return &ClientSet{
		CoreV1Interface:                 fakeKubeClientSet.CoreV1(),
		AppsV1Interface:                 fakeKubeClientSet.AppsV1(),
		ConfigV1Interface:               fakeclientconfigv1.NewSimpleClientset(objects...).ConfigV1(),
		MachineconfigurationV1Interface: fakeclientmachineconfigv1.NewSimpleClientset(objects...).MachineconfigurationV1(),
		ApiextensionsV1Interface:        fakeclientapiextensionsv1.NewSimpleClientset(objects...).ApiextensionsV1(),
		OperatorV1alphaInterface:        fakeclientoperatorsv1alpha1.NewSimpleClientset(objects...).OperatorV1alpha1(),
		BuildV1Interface:                fakeclientbuildv1.NewSimpleClientset(objects...).BuildV1(),
		ImageV1Interface:                fakeclientimagev1.NewSimpleClientset(objects...).ImageV1(),
		kubeClientSet:                   fakeKubeClientSet,
	}
}
