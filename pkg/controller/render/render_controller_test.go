package render

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	igntypes "github.com/coreos/ignition/v2/config/v3_0/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/diff"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/machine-config-operator/test/helpers"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client *fake.Clientset

	mcpLister []*mcfgv1.MachineConfigPool
	mcLister  []*mcfgv1.MachineConfig
	ccLister  []*mcfgv1.ControllerConfig

	actions []core.Action

	objects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	return f
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())

	c := New(i.Machineconfiguration().V1().MachineConfigPools(), i.Machineconfiguration().V1().MachineConfigs(),
		i.Machineconfiguration().V1().ControllerConfigs(), k8sfake.NewSimpleClientset(), f.client)

	c.mcpListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	stopCh := make(chan struct{})
	defer close(stopCh)
	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)

	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mcpLister {
		i.Machineconfiguration().V1().MachineConfigPools().Informer().GetIndexer().Add(c)
	}
	for _, m := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(m)
	}

	for _, m := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(m)
	}

	return c
}

func (f *fixture) run(mcpname string) {
	f.runController(mcpname, false)
}

func (f *fixture) runExpectError(mcpname string) {
	f.runController(mcpname, true)
}

func (f *fixture) runController(mcpname string, expectError bool) {
	c := f.newController()

	err := c.syncHandler(mcpname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing machineconfigpool: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing machineconfigpool, got nil")
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
			(action.Matches("list", "machineconfigpools") ||
				action.Matches("watch", "machineconfigpools") ||
				action.Matches("list", "controllerconfigs") ||
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

func (f *fixture) expectPatchMachineConfigAction(config *mcfgv1.MachineConfig, patch []byte) {
	f.actions = append(f.actions, core.NewRootPatchAction(schema.GroupVersionResource{Resource: "machineconfigs"}, config.Name, types.MergePatchType, patch))
}

func (f *fixture) expectUpdateMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootUpdateAction(schema.GroupVersionResource{Resource: "machineconfigs"}, config))
}

func (f *fixture) expectUpdateMachineConfigPoolSpec(pool *mcfgv1.MachineConfigPool) {
	f.actions = append(f.actions, core.NewRootUpdateSubresourceAction(schema.GroupVersionResource{Resource: "machineconfigpools"}, "spec", pool))
}

func (f *fixture) expectUpdateMachineConfigPoolStatus(pool *mcfgv1.MachineConfigPool) {
	f.actions = append(f.actions, core.NewRootUpdateSubresourceAction(schema.GroupVersionResource{Resource: "machineconfigpools"}, "status", pool))
}

func newControllerConfig(name string) *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5))},
		Spec: mcfgv1.ControllerConfigSpec{
			EtcdDiscoveryDomain: fmt.Sprintf("%s.tt.testing", name),
			OSImageURL:          "dummy",
		},
		Status: mcfgv1.ControllerConfigStatus{
			Conditions: []mcfgv1.ControllerConfigStatusCondition{
				{
					Type:   mcfgv1.TemplateControllerCompleted,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

func TestCreatesGeneratedMachineConfig(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []igntypes.File{{
		Node: igntypes.Node{
			Path: "/dummy/0",
		},
	}, {
		Node: igntypes.Node{
			Path: "/dummy/1",
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []igntypes.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []igntypes.File{files[1]}),
	}
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	f.ccLister = append(f.ccLister, cc)
	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.mcLister = append(f.mcLister, mcs...)
	for idx := range mcs {
		f.objects = append(f.objects, mcs[idx])
	}
}

// Testing that ignition validation in generateRenderedMachineConfig() correctly finds MCs that contain invalid ignconfigs.
// generateRenderedMachineConfig should return an error when one of the MCs in configs contains an invalid ignconfig.
func TestIgnValidationGenerateRenderedMachineConfig(t *testing.T) {
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []igntypes.File{{
		Node: igntypes.Node{
			Path: "/dummy/0",
		},
	}, {
		Node: igntypes.Node{
			Path: "/dummy/1",
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []igntypes.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []igntypes.File{files[1]}),
	}
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	_, err := generateRenderedMachineConfig(mcp, mcs, cc)
	require.Nil(t, err)

	// verify that an invalid igntion config (here a config with content and an empty version,
	// will fail validation
	// mcs[1].Spec.Config.Ignition.Version = ""
	// _, err = generateRenderedMachineConfig(mcp, mcs, cc)
	// require.NotNil(t, err)

	// verify that a machine config with no ignition content will not fail validation
	mcs[1].Spec.Config = igntypes.Config{}
	mcs[1].Spec.KernelArguments = append(mcs[1].Spec.KernelArguments, "test1")
	_, err = generateRenderedMachineConfig(mcp, mcs, cc)
	require.Nil(t, err)

}

func TestUpdatesGeneratedMachineConfig(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []igntypes.File{{
		Node: igntypes.Node{
			Path: "/dummy/0",
		},
	}, {
		Node: igntypes.Node{
			Path: "/dummy/1",
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []igntypes.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []igntypes.File{files[1]}),
	}
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	if err != nil {
		t.Fatal(err)
	}
	gmc.Spec.OSImageURL = "why-did-you-change-it"
	mcp.Spec.Configuration.Name = gmc.Name
	mcp.Status.Configuration.Name = gmc.Name

	f.ccLister = append(f.ccLister, cc)

	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.mcLister = append(f.mcLister, mcs...)
	for idx := range mcs {
		f.objects = append(f.objects, mcs[idx])
	}
	f.mcLister = append(f.mcLister, gmc)
	f.objects = append(f.objects, gmc)

	expmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	if err != nil {
		t.Fatal(err)
	}

	f.expectGetMachineConfigAction(expmc)
	f.expectUpdateMachineConfigAction(expmc)

	f.run(getKey(mcp, t))
}

func TestGenerateMachineConfigNoOverrideOSImageURL(t *testing.T) {
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy-test-1", []igntypes.File{}),
		helpers.NewMachineConfig("00-test-cluster-master-0", map[string]string{"node-role/master": ""}, "dummy-change", []igntypes.File{}),
	}

	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "dummy", gmc.Spec.OSImageURL)
}

func TestDoNothing(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []igntypes.File{{
		Node: igntypes.Node{
			Path: "/dummy/0",
		},
	}, {
		Node: igntypes.Node{
			Path: "/dummy/1",
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []igntypes.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []igntypes.File{files[1]}),
	}
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	if err != nil {
		t.Fatal(err)
	}
	mcp.Spec.Configuration.Name = gmc.Name
	mcp.Status.Configuration.Name = gmc.Name

	f.ccLister = append(f.ccLister, cc)

	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.mcLister = append(f.mcLister, mcs...)
	for idx := range mcs {
		f.objects = append(f.objects, mcs[idx])
	}
	f.mcLister = append(f.mcLister, gmc)
	f.objects = append(f.objects, gmc)

	f.expectGetMachineConfigAction(gmc)

	f.run(getKey(mcp, t))
}

func TestGetMachineConfigsForPool(t *testing.T) {
	masterPool := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []igntypes.File{{
		Node: igntypes.Node{
			Path: "/dummy/0",
		},
	}, {
		Node: igntypes.Node{
			Path: "/dummy/1",
		},
	}, {
		Node: igntypes.Node{
			Path: "/dummy/2",
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []igntypes.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []igntypes.File{files[1]}),
		helpers.NewMachineConfig("00-test-cluster-worker", map[string]string{"node-role/worker": ""}, "dummy://2", []igntypes.File{files[2]}),
	}
	masterConfigs, err := getMachineConfigsForPool(masterPool, mcs)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	// check that only the master MCs were selected
	if len(masterConfigs) != 2 {
		t.Fatalf("expected to select 2 configs for pool master got: %v", len(masterConfigs))
	}

	// search for a worker config in an array of MCs with no worker configs
	workerPool := helpers.NewMachineConfigPool("test-cluster-worker", helpers.WorkerSelector, nil, "")
	_, err = getMachineConfigsForPool(workerPool, mcs[:2])
	if err == nil {
		t.Fatalf("expected error, no worker configs found")
	}
}

func getKey(config *mcfgv1.MachineConfigPool, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(config)
	if err != nil {
		t.Errorf("Unexpected error getting key for config %v: %v", config.Name, err)
		return ""
	}
	return key
}

func TestMachineConfigsNoBailWithoutPool(t *testing.T) {
	f := newFixture(t)
	mc := helpers.NewMachineConfig("00-test-cluster-worker", map[string]string{"node-role/worker": ""}, "dummy://2", []igntypes.File{})
	oref := metav1.NewControllerRef(newControllerConfig("test"), mcfgv1.SchemeGroupVersion.WithKind("ControllerConfig"))
	mc.SetOwnerReferences([]metav1.OwnerReference{*oref})
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.WorkerSelector, nil, "")
	f.mcpLister = append(f.mcpLister, mcp)
	c := f.newController()
	queue := []*mcfgv1.MachineConfigPool{}
	c.enqueueMachineConfigPool = func(mcp *mcfgv1.MachineConfigPool) {
		queue = append(queue, mcp)
	}
	c.addMachineConfig(mc)
	c.updateMachineConfig(mc, mc)
	c.deleteMachineConfig(mc)
	require.Len(t, queue, 3)
}
