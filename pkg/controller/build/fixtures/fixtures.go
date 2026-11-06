package fixtures

import (
	"fmt"
	"sync"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientimagev1 "github.com/openshift/client-go/image/clientset/versioned/fake"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	fakeclientroutev1 "github.com/openshift/client-go/route/clientset/versioned/fake"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func GetEmptyClientsForTest(t *testing.T) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *testhelpers.Assertions) {
	kubeclient := fakecorev1client.NewClientset()
	mcfgclient := fakeclientmachineconfigv1.NewClientset()
	installMachineOSBuildStatusSubresourceReactor(mcfgclient)
	imageclient := fakeclientimagev1.NewClientset()
	return kubeclient, mcfgclient, testhelpers.Assert(t, kubeclient, mcfgclient, imageclient)
}

// Gets the kubeclient and mcfgclients needed for a test with the default Kube
// objects in them.
func GetClientsForTest(t *testing.T) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *ObjectsForTest, *testhelpers.Assertions) {
	return GetClientsForTestWithAdditionalObjects(t, []runtime.Object{}, []runtime.Object{})
}

// Gets the kubeclient and mcfgclient, adds any additional objects to them, and
// also returns the ObjectsForTest which are instantiated assuming the
// pool name "worker".
func GetClientsForTestWithAdditionalObjects(t *testing.T, addlKubeObjects, addlMcfgObjects []runtime.Object) (*fakecorev1client.Clientset, *fakeclientmachineconfigv1.Clientset, *fakeclientimagev1.Clientset, *fakeclientroutev1.Clientset, *ObjectsForTest, *testhelpers.Assertions) {
	obj := NewObjectsForTest("worker")

	mcfgObjects := append(addlMcfgObjects, obj.ToRuntimeObjects()...) //nolint:gocritic // It's not supposed to be assigned to the same slice.
	mcfgObjects = append(mcfgObjects, &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "machine-config-controller",
		},
	})

	addlKubeObjects = append(defaultKubeObjects(), addlKubeObjects...)

	kubeclient := fakecorev1client.NewClientset(addlKubeObjects...)
	mcfgclient := fakeclientmachineconfigv1.NewClientset(mcfgObjects...)
	installMachineOSBuildStatusSubresourceReactor(mcfgclient)
	imageclient := fakeclientimagev1.NewClientset()
	routeclient := fakeclientroutev1.NewClientset()

	return kubeclient, mcfgclient, imageclient, routeclient, &obj, testhelpers.Assert(t, kubeclient, mcfgclient, imageclient)
}

// installMachineOSBuildStatusSubresourceReactor makes the fake client model
// the API server's status-subresource behavior. The generated fake otherwise
// replaces the entire object for both Update and UpdateStatus, allowing a
// stale metadata update to erase a status written by the build controller.
func installMachineOSBuildStatusSubresourceReactor(client *fakeclientmachineconfigv1.Clientset) {
	var updateLock sync.Mutex

	client.PrependReactor("update", "machineosbuilds", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updateAction, ok := action.(k8stesting.UpdateAction)
		if !ok || (action.GetSubresource() != "" && action.GetSubresource() != "status") {
			return false, nil, nil
		}

		incoming, ok := updateAction.GetObject().(*mcfgv1.MachineOSBuild)
		if !ok {
			return true, nil, fmt.Errorf("expected MachineOSBuild in update action, got %T", updateAction.GetObject())
		}

		// ObjectTracker protects individual operations, but the read/merge/write
		// sequence must also be atomic to match a single API server update.
		updateLock.Lock()
		defer updateLock.Unlock()

		storedObject, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), incoming.Name)
		if err != nil {
			return true, nil, err
		}
		stored, ok := storedObject.(*mcfgv1.MachineOSBuild)
		if !ok {
			return true, nil, fmt.Errorf("expected stored MachineOSBuild, got %T", storedObject)
		}

		updated := incoming.DeepCopy()
		if action.GetSubresource() == "status" {
			updated = stored.DeepCopy()
			updated.Status = incoming.DeepCopy().Status
		} else {
			updated.Status = stored.DeepCopy().Status
		}

		updateOptions := metav1.UpdateOptions{}
		if actionWithOptions, ok := action.(interface {
			GetUpdateOptions() metav1.UpdateOptions
		}); ok {
			updateOptions = actionWithOptions.GetUpdateOptions()
		}
		if err := client.Tracker().Update(action.GetResource(), updated, action.GetNamespace(), updateOptions); err != nil {
			return true, nil, err
		}
		returned, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), incoming.Name)
		if err != nil {
			return true, nil, err
		}
		return true, returned, nil
	})
}
