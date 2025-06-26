package fixtures

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientimagev1 "github.com/openshift/client-go/image/clientset/versioned/fake"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	fakeclientroutev1 "github.com/openshift/client-go/route/clientset/versioned/fake"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	fakeolmclientset "github.com/operator-framework/operator-lifecycle-manager/pkg/api/client/clientset/versioned/fake"
	fakepipelineoperatorclientset "github.com/tektoncd/operator/pkg/client/clientset/versioned/fake"
	faketektonclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

func GetEmptyClientsForTest(t *testing.T) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *testhelpers.Assertions) {
	kubeclient := fakecorev1client.NewSimpleClientset()
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	imageclient := fakeclientimagev1.NewSimpleClientset()
	return kubeclient, mcfgclient, testhelpers.Assert(t, kubeclient, mcfgclient, imageclient)
}

// Gets the kubeclient and mcfgclients needed for a test with the default Kube
// objects in them.
func GetClientsForTest(t *testing.T) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *fakepipelineoperatorclientset.Clientset, *fakeolmclientset.ClientSet, *faketektonclientset.Clientset, *ObjectsForTest, *testhelpers.Assertions) {
	return GetClientsForTestWithAdditionalObjects(t, []runtime.Object{}, []runtime.Object{})
}

// Gets the kubeclient and mcfgclient, adds any additional objects to them, and
// also returns the ObjectsForTest which are instantiated assuming the
// pool name "worker".
func GetClientsForTestWithAdditionalObjects(t *testing.T, addlKubeObjects, addlMcfgObjects []runtime.Object) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *fakepipelineoperatorclientset.Clientset, *fakeolmclientset.ClientSet, *faketektonclientset.Clientset, *ObjectsForTest, *testhelpers.Assertions) {
	obj := NewObjectsForTest("worker")

	mcfgObjects := append(addlMcfgObjects, obj.ToRuntimeObjects()...) //nolint:gocritic // It's not supposed to be assigned to the same slice.
	mcfgObjects = append(mcfgObjects, &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "machine-config-controller",
		},
	})

	addlKubeObjects = append(defaultKubeObjects(), addlKubeObjects...)

	kubeclient := fakecorev1client.NewSimpleClientset(addlKubeObjects...)
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset(mcfgObjects...)
	imageclient := fakeclientimagev1.NewSimpleClientset()
	routeclient := fakeclientroutev1.NewSimpleClientset()
	pipelineoperatorclient := fakepipelineoperatorclientset.NewSimpleClientset()
	olmclient := fakeolmclientset.NewSimpleClientset()
	tektonclient := faketektonclientset.NewSimpleClientset()

	return kubeclient, mcfgclient, imageclient, routeclient, pipelineoperatorclient, olmclient, tektonclient, &obj, testhelpers.Assert(t, kubeclient, mcfgclient, imageclient)
}
