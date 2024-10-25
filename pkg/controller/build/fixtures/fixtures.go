package fixtures

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

func GetEmptyClientsForTest(t *testing.T) (clientset.Interface, mcfgclientset.Interface, *testhelpers.Assertions) {
	kubeclient := fakecorev1client.NewSimpleClientset()
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	return kubeclient, mcfgclient, testhelpers.Assert(t, kubeclient, mcfgclient)
}

// Gets the kubeclient and mcfgclients needed for a test with the default Kube
// objects in them.
func GetClientsForTest(t *testing.T) (clientset.Interface, mcfgclientset.Interface, *ObjectsForTest, *testhelpers.Assertions) {
	return GetClientsForTestWithAdditionalObjects(t, []runtime.Object{}, []runtime.Object{})
}

// Gets the kubeclient and mcfgclient, adds any additional objects to them, and
// also returns the ObjectsForTest which are instantiated assuming the
// pool name "worker".
func GetClientsForTestWithAdditionalObjects(t *testing.T, addlKubeObjects, addlMcfgObjects []runtime.Object) (clientset.Interface, mcfgclientset.Interface, *ObjectsForTest, *testhelpers.Assertions) {
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

	return kubeclient, mcfgclient, &obj, testhelpers.Assert(t, kubeclient, mcfgclient)
}
