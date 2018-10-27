package template

import (
	"reflect"
	"testing"
	"time"

	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/diff"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client *fake.Clientset

	ccLister []*mcfgv1.ControllerConfig
	mcLister []*mcfgv1.MachineConfig

	actions []core.Action

	objects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	return f
}

func newControllerConfig(name string) *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
		Spec: mcfgv1.ControllerConfigSpec{
			ClusterDNSIP: "10.3.0.1/16",
			ClusterName:  name,
			BaseDomain:   "openshift.testing",
			Platform:     "libvirt",
		},
	}
}

func (f *fixture) newController() (*Controller, informers.SharedInformerFactory) {
	f.client = fake.NewSimpleClientset(f.objects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	c := New(templateDir,
		i.Machineconfiguration().V1().ControllerConfigs(), i.Machineconfiguration().V1().MachineConfigs(),
		k8sfake.NewSimpleClientset(), f.client)

	c.ccListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}

	for _, m := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(m)
	}

	return c, i
}

func (f *fixture) run(ccname string) {
	f.runController(ccname, true, false)
}

func (f *fixture) runExpectError(ccname string) {
	f.runController(ccname, true, true)
}

func (f *fixture) runController(ccname string, startInformers bool, expectError bool) {
	c, i := f.newController()
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		i.Start(stopCh)
	}

	err := c.syncHandler(ccname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing controllerconfig: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing controllerconfig, got nil")
	}

	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		if len(f.actions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(f.actions), actions[i:])
			break
		}

		expectedAction := f.actions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.actions) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.actions)-len(actions), f.actions[len(actions):])
	}
}

// checkAction verifies that expected and actual actions are equal and both have
// same attached resources
func checkAction(expected, actual core.Action, t *testing.T) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
		return
	}

	if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
		t.Errorf("Action has wrong type. Expected: %t. Got: %t", expected, actual)
		return
	}

	switch a := actual.(type) {
	case core.CreateAction:
		e, _ := expected.(core.CreateAction)
		expObject := e.GetObject()
		object := a.GetObject()

		if !equality.Semantic.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expObject, object))
		}
	case core.UpdateAction:
		e, _ := expected.(core.UpdateAction)
		expObject := e.GetObject()
		object := a.GetObject()

		if !equality.Semantic.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expObject, object))
		}
	case core.PatchAction:
		e, _ := expected.(core.PatchAction)
		expPatch := e.GetPatch()
		patch := a.GetPatch()

		if !equality.Semantic.DeepEqual(expPatch, expPatch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expPatch, patch))
		}
	}
}

// filterInformerActions filters list and watch actions for testing resources.
// Since list and watch don't change resource state we can filter it to lower
// nose level in our tests.
func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "controllerconfigs") ||
				action.Matches("watch", "controllerconfigs") ||
				action.Matches("list", "machineconfigs") ||
				action.Matches("watch", "machineconfigs")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func (f *fixture) expectGetMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootGetAction(schema.GroupVersionResource{Resource: "machineconfigs"}, config.Name))
}

func (f *fixture) expectCreateMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootCreateAction(schema.GroupVersionResource{Resource: "machineconfigs"}, config))
}

func (f *fixture) expectUpdateMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootUpdateAction(schema.GroupVersionResource{Resource: "machineconfigs"}, config))
}

func TestCreatesMachineConfigs(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig("test-cluster")

	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)

	expMCs, err := getMachineConfigsForControllerConfig(templateDir, cc)
	if err != nil {
		t.Fatal(err)
	}

	for idx := range expMCs {
		f.expectGetMachineConfigAction(expMCs[idx])
		f.expectCreateMachineConfigAction(expMCs[idx])
	}

	f.run(getKey(cc, t))
}

func TestDoNothing(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig("test-cluster")
	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc)
	if err != nil {
		t.Fatal(err)
	}

	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)
	f.mcLister = append(f.mcLister, mcs...)
	for idx := range mcs {
		f.objects = append(f.objects, mcs[idx])
	}

	for idx := range mcs {
		f.expectGetMachineConfigAction(mcs[idx])
	}

	f.run(getKey(cc, t))
}

func TestRecreateMachineConfig(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig("test-cluster")
	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc)
	if err != nil {
		t.Fatal(err)
	}

	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)
	for idx := 0; idx < len(mcs)-1; idx++ {
		f.mcLister = append(f.mcLister, mcs[idx])
		f.objects = append(f.objects, mcs[idx])
	}

	for idx := range mcs {
		f.expectGetMachineConfigAction(mcs[idx])
	}
	f.expectCreateMachineConfigAction(mcs[len(mcs)-1])

	f.run(getKey(cc, t))
}

func TestUpdateMachineConfig(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig("test-cluster")
	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc)
	if err != nil {
		t.Fatal(err)
	}
	//update machineconfig
	mcs[len(mcs)-1].Spec.Config = ignv2_2types.Config{}

	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)
	for idx := range mcs {
		f.mcLister = append(f.mcLister, mcs[idx])
		f.objects = append(f.objects, mcs[idx])
	}

	expmcs, err := getMachineConfigsForControllerConfig(templateDir, cc)
	if err != nil {
		t.Fatal(err)
	}
	for idx := range expmcs {
		f.expectGetMachineConfigAction(expmcs[idx])
	}
	f.expectUpdateMachineConfigAction(expmcs[len(expmcs)-1])

	f.run(getKey(cc, t))
}

func getKey(config *mcfgv1.ControllerConfig, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(config)
	if err != nil {
		t.Errorf("Unexpected error getting key for config %v: %v", config.Name, err)
		return ""
	}
	return key
}
