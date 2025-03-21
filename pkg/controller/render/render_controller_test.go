package render

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/clarketm/json"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	configv1 "github.com/openshift/api/config/v1"
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

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/version"
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
	crcLister []*mcfgv1.ContainerRuntimeConfig
	mckLister []*mcfgv1.KubeletConfig

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
		i.Machineconfiguration().V1().ControllerConfigs(), i.Machineconfiguration().V1().ContainerRuntimeConfigs(), i.Machineconfiguration().V1().KubeletConfigs(), k8sfake.NewSimpleClientset(), f.client)

	c.mcpListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.crcListerSynced = alwaysReady
	c.mckListerSynced = alwaysReady
	c.eventRecorder = ctrlcommon.NamespacedEventRecorder(&record.FakeRecorder{})

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

	for _, m := range f.crcLister {
		i.Machineconfiguration().V1().ContainerRuntimeConfigs().Informer().GetIndexer().Add(m)
	}

	for _, m := range f.mckLister {
		i.Machineconfiguration().V1().KubeletConfigs().Informer().GetIndexer().Add(m)
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
				action.Matches("watch", "machineconfigs") ||
				action.Matches("list", "kubeletconfigs") ||
				action.Matches("watch", "kubeletconfigs") ||
				action.Matches("list", "containerruntimeconfigs") ||
				action.Matches("watch", "containerruntimeconfigs")) {
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

func (f *fixture) expectUpdateMachineConfigPool(pool *mcfgv1.MachineConfigPool) {
	f.actions = append(f.actions, core.NewRootUpdateAction(schema.GroupVersionResource{Resource: "machineconfigpools"}, pool))
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
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{daemonconsts.GeneratedByVersionAnnotationKey: version.Raw}, Name: name, UID: types.UID(utilrand.String(5))},
		Spec: mcfgv1.ControllerConfigSpec{
			Infra: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					EtcdDiscoveryDomain: fmt.Sprintf("%s.tt.testing", name),
				},
			},
			OSImageURL: "dummy",
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
	files := []ign3types.File{{
		Node: ign3types.Node{
			Path: "/dummy/0",
		},
	}, {
		Node: ign3types.Node{
			Path: "/dummy/1",
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []ign3types.File{files[1]}),
	}
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)
	crc := &mcfgv1.ContainerRuntimeConfig{}
	mck := &mcfgv1.KubeletConfig{}

	f.ccLister = append(f.ccLister, cc)
	f.crcLister = append(f.crcLister, crc)
	f.mckLister = append(f.mckLister, mck)
	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.mcLister = append(f.mcLister, mcs...)
	for idx := range mcs {
		f.objects = append(f.objects, mcs[idx])
	}

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	assert.NoError(t, err)

	mcpNew := mcp.DeepCopy()
	mcpNew.Spec.Configuration.Source = getMachineConfigRefs(mcs)
	mcpNew.Spec.Configuration.Name = gmc.Name

	f.expectCreateMachineConfigAction(gmc)
	f.expectUpdateMachineConfigPool(mcpNew)

	f.run(getKey(mcp, t))
}

// Testing that ignition validation in generateRenderedMachineConfig() correctly finds MCs that contain invalid ignconfigs.
// generateRenderedMachineConfig should return an error when one of the MCs in configs contains an invalid ignconfig.
func TestIgnValidationGenerateRenderedMachineConfig(t *testing.T) {
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []ign3types.File{{
		Node: ign3types.Node{
			Path: "/dummy/0",
		},
	}, {
		Node: ign3types.Node{
			Path: "/dummy/1",
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []ign3types.File{files[1]}),
	}
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	_, err := generateRenderedMachineConfig(mcp, mcs, cc)
	require.Nil(t, err)

	// verify that an invalid ignition config (here a config with content and an empty version,
	// will fail validation
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mcs[1].Spec.Config.Raw)
	require.Nil(t, err)
	ignCfg.Ignition.Version = ""
	rawIgnCfg, err := json.Marshal(ignCfg)
	require.Nil(t, err)
	mcs[1].Spec.Config.Raw = rawIgnCfg

	_, err = generateRenderedMachineConfig(mcp, mcs, cc)
	require.NotNil(t, err)

	// verify that a machine config with no ignition content will not fail validation
	emptyIgnCfg := ctrlcommon.NewIgnConfig()
	rawEmptyIgnCfg, err := json.Marshal(emptyIgnCfg)
	require.Nil(t, err)
	mcs[1].Spec.Config.Raw = rawEmptyIgnCfg
	mcs[1].Spec.KernelArguments = append(mcs[1].Spec.KernelArguments, "test1")
	_, err = generateRenderedMachineConfig(mcp, mcs, cc)
	require.Nil(t, err)

}

func TestUpdatesGeneratedMachineConfig(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []ign3types.File{{
		Node: ign3types.Node{
			Path:      "/dummy/0",
			Overwrite: helpers.BoolToPtr(false),
		},
	}, {
		Node: ign3types.Node{
			Path:      "/dummy/1",
			Overwrite: helpers.BoolToPtr(false),
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []ign3types.File{files[1]}),
	}
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	if err != nil {
		t.Fatal(err)
	}
	gmc.Spec.OSImageURL = "why-did-you-change-it"
	mcp.Spec.Configuration.Name = gmc.Name
	mcp.Status.Configuration.Name = gmc.Name

	crc := &mcfgv1.ContainerRuntimeConfig{}
	mck := &mcfgv1.KubeletConfig{}

	f.ccLister = append(f.ccLister, cc)
	f.crcLister = append(f.crcLister, crc)
	f.mckLister = append(f.mckLister, mck)
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

	mcpNew := mcp.DeepCopy()
	mcpNew.Spec.Configuration.Source = getMachineConfigRefs(mcs)

	f.expectGetMachineConfigAction(expmc)
	f.expectUpdateMachineConfigAction(expmc)
	f.expectUpdateMachineConfigPool(mcpNew)

	f.run(getKey(mcp, t))
}

func TestGenerateMachineConfigOverrideOSImageURL(t *testing.T) {
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy-test-1", []ign3types.File{}),
		helpers.NewMachineConfig("00-test-cluster-master-0", map[string]string{"node-role/master": ""}, "dummy-change", []ign3types.File{}),
	}

	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "dummy-change", gmc.Spec.OSImageURL)

	mcs = append(mcs, helpers.NewMachineConfig("00-test-cluster-master-1", map[string]string{"node-role/master": ""}, "dummy-change-2", []ign3types.File{}))

	gmc, err = generateAndValidateRenderedMachineConfig(gmc, mcp, mcs, cc)
	assert.NoError(t, err)
	assert.Equal(t, "dummy-change-2", gmc.Spec.OSImageURL)
}

func TestVersionSkew(t *testing.T) {
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy-test-1", []ign3types.File{}),
		helpers.NewMachineConfig("00-test-cluster-master-0", map[string]string{"node-role/master": ""}, "dummy-change", []ign3types.File{}),
	}

	cc := newControllerConfig(ctrlcommon.ControllerConfigName)
	cc.Annotations[daemonconsts.GeneratedByVersionAnnotationKey] = "different-version"
	_, err := generateRenderedMachineConfig(mcp, mcs, cc)
	require.NotNil(t, err)

	// Now the same thing without overriding the version
	cc = newControllerConfig(ctrlcommon.ControllerConfigName)
	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	require.Nil(t, err)
	require.NotNil(t, gmc)
}

func TestGenerateRenderedConfigOnLatestControllerVersionOnly(t *testing.T) {
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfigWithAnnotation("00-updated-conf", map[string]string{"node-role/master": ""}, map[string]string{ctrlcommon.GeneratedByControllerVersionAnnotationKey: "2"}, "dummy-test-1", []ign3types.File{}),
		helpers.NewMachineConfigWithAnnotation("00-old-conf", map[string]string{"node-role/master": ""}, map[string]string{ctrlcommon.GeneratedByControllerVersionAnnotationKey: "1"}, "dummy-change", []ign3types.File{}),
	}
	version.Hash = "2"
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)
	_, err := generateRenderedMachineConfig(mcp, mcs, cc)
	require.NotNil(t, err)

	mcs = []*mcfgv1.MachineConfig{
		helpers.NewMachineConfigWithAnnotation("00-updated-conf", map[string]string{"node-role/master": ""}, map[string]string{ctrlcommon.GeneratedByControllerVersionAnnotationKey: "2"}, "dummy-test-1", []ign3types.File{}),
		helpers.NewMachineConfigWithAnnotation("99-user-conf", map[string]string{"node-role/master": ""}, map[string]string{ctrlcommon.GeneratedByControllerVersionAnnotationKey: ""}, "user-data", []ign3types.File{}),
	}
	_, err = generateRenderedMachineConfig(mcp, mcs, cc)
	require.Nil(t, err)
}

func TestDoNothing(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []ign3types.File{{
		Node: ign3types.Node{
			Path:      "/dummy/0",
			Overwrite: helpers.BoolToPtr(false),
		},
	}, {
		Node: ign3types.Node{
			Path:      "/dummy/1",
			Overwrite: helpers.BoolToPtr(false),
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []ign3types.File{files[1]}),
	}
	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc)
	if err != nil {
		t.Fatal(err)
	}
	mcp.Spec.Configuration.Name = gmc.Name
	mcp.Status.Configuration.Name = gmc.Name

	crc := &mcfgv1.ContainerRuntimeConfig{}
	mck := &mcfgv1.KubeletConfig{}

	f.ccLister = append(f.ccLister, cc)
	f.crcLister = append(f.crcLister, crc)
	f.mckLister = append(f.mckLister, mck)
	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.mcLister = append(f.mcLister, mcs...)
	for idx := range mcs {
		f.objects = append(f.objects, mcs[idx])
	}
	f.mcLister = append(f.mcLister, gmc)
	f.objects = append(f.objects, gmc)

	mcpNew := mcp.DeepCopy()
	mcpNew.Spec.Configuration.Source = getMachineConfigRefs(mcs)

	f.expectGetMachineConfigAction(gmc)
	f.expectUpdateMachineConfigPool(mcpNew)

	f.run(getKey(mcp, t))
}

func TestGetMachineConfigsForPool(t *testing.T) {
	masterPool := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	files := []ign3types.File{{
		Node: ign3types.Node{
			Path: "/dummy/0",
		},
	}, {
		Node: ign3types.Node{
			Path: "/dummy/1",
		},
	}, {
		Node: ign3types.Node{
			Path: "/dummy/2",
		},
	}}
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{files[0]}),
		helpers.NewMachineConfig("05-extra-master", map[string]string{"node-role/master": ""}, "dummy://1", []ign3types.File{files[1]}),
		helpers.NewMachineConfig("00-test-cluster-worker", map[string]string{"node-role/worker": ""}, "dummy://2", []ign3types.File{files[2]}),
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
	mc := helpers.NewMachineConfig("00-test-cluster-worker", map[string]string{"node-role/worker": ""}, "dummy://2", []ign3types.File{})
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

func TestGenerateMachineConfigValidation(t *testing.T) {
	mcp := helpers.NewMachineConfigPool("test-cluster-master", helpers.MasterSelector, nil, "")
	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy-test-1", []ign3types.File{}),
		helpers.NewMachineConfig("00-test-cluster-master-0", map[string]string{"node-role/master": ""}, "dummy-change", []ign3types.File{}),
	}

	currentMC := helpers.NewMachineConfig("00-test-cluster-master", map[string]string{"node-role/master": ""}, "dummy-test-1", []ign3types.File{})
	currentMC.Spec.FIPS = false

	mcs[1].Spec.FIPS = true

	cc := newControllerConfig(ctrlcommon.ControllerConfigName)

	gmc, err := generateAndValidateRenderedMachineConfig(currentMC, mcp, mcs, cc)
	assert.Error(t, err)
	assert.Nil(t, gmc)
}

func TestGenerateRenderedMachineConfig(t *testing.T) {
	// Test that all other configs can also be set properly

	// we previously prevented OSImageURL from being overridden via
	// machineconfig, but now that we're doing layering, we want to
	// give that functionality back, so make sure we can override it
	machineConfigOSImageURL := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "osimageurl",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "overriddenURL",
		},
	}
	machineConfigKernelArgs1 := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kargs1",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			KernelArguments: []string{"karg-c"},
		},
	}
	machineConfigKernelArgs2 := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kargs2",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			KernelArguments: []string{"karg-c", "karg-b"},
		},
	}
	machineConfigKernelArgs3 := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kargs3",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			KernelArguments: []string{"karg-c", "karg-b", "karg-a"},
		},
	}
	machineConfigKernelType := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kerneltype",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			KernelType: ctrlcommon.KernelTypeRealtime,
		},
	}
	machineConfigExtensions1 := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "extension1",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Extensions: []string{"ext-c"},
		},
	}
	machineConfigExtensions2 := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "extension2",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Extensions: []string{"ext-c", "ext-b"},
		},
	}
	machineConfigExtensions3 := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "extension3",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Extensions: []string{"ext-c", "ext-b", "ext-a"},
		},
	}
	machineConfigIgnSSHUser := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Passwd: ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234"}},
			},
		},
	}, "ssh", ctrlcommon.MachineConfigPoolWorker)

	machineConfigIgnPasswdHashUser := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Passwd: ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				{Name: "core", PasswordHash: helpers.StrToPtr("testpass")},
			},
		},
	}, "passwd", ctrlcommon.MachineConfigPoolWorker)

	// we added some v3 specific logic for kargs, make sure we didn't break the v2 path
	machineConfigIgnV2Merge := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign2types.Config{
		Ignition: ign2types.Ignition{
			Version: ign2types.MaxVersion.String(),
		},
	}, "v2", ctrlcommon.MachineConfigPoolWorker)

	machineConfigIgn := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Passwd: ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}},
			},
		},
	}, "ssh", ctrlcommon.MachineConfigPoolWorker)

	machineConfigFIPS := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "fips",
			Labels: map[string]string{ctrlcommon.MachineConfigRoleLabel: ctrlcommon.MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			FIPS: true,
		},
	}

	emptyIgnConfig := runtime.RawExtension{
		Raw: helpers.MarshalOrDie(ign3types.Config{
			Ignition: ign3types.Ignition{
				Version: ign3types.MaxVersion.String(),
			},
		}),
	}

	expectedMachineConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      "overriddenURL",
			KernelArguments: []string{"karg-c", "karg-c", "karg-b", "karg-c", "karg-b", "karg-a"},
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ign3types.Config{
					Ignition: ign3types.Ignition{
						Version: ign3types.MaxVersion.String(),
					},
					Passwd: ign3types.Passwd{
						Users: []ign3types.PasswdUser{
							{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "1234"}, PasswordHash: helpers.StrToPtr("testpass")},
						},
					},
				}),
			},
			FIPS:       true,
			KernelType: ctrlcommon.KernelTypeRealtime,
			Extensions: []string{"ext-c", "ext-c", "ext-b", "ext-c", "ext-b", "ext-a"},
		},
	}

	testCases := []struct {
		name                 string
		inMachineConfigs     []*mcfgv1.MachineConfig
		expectedName         string
		expectedSpec         mcfgv1.MachineConfigSpec
		osImageURLOverridden bool
	}{
		{
			name: "Original order",
			inMachineConfigs: []*mcfgv1.MachineConfig{
				machineConfigOSImageURL,
				machineConfigKernelArgs1,
				machineConfigKernelArgs2,
				machineConfigKernelArgs3,
				machineConfigKernelType,
				machineConfigExtensions3,
				machineConfigExtensions2,
				machineConfigExtensions1,
				machineConfigIgn,
				machineConfigFIPS,
				machineConfigIgnPasswdHashUser,
				machineConfigIgnSSHUser,
				machineConfigIgnV2Merge,
			},
			expectedName:         "rendered-worker-b2628b52cbb66afc4bec5443cc1eebb6",
			expectedSpec:         expectedMachineConfig.Spec,
			osImageURLOverridden: true,
		},
		{
			name: "Alternate order",
			inMachineConfigs: []*mcfgv1.MachineConfig{
				machineConfigExtensions1,
				machineConfigExtensions2,
				machineConfigExtensions3,
				machineConfigFIPS,
				machineConfigIgn,
				machineConfigIgnPasswdHashUser,
				machineConfigIgnSSHUser,
				machineConfigIgnV2Merge,
				machineConfigKernelArgs1,
				machineConfigKernelArgs2,
				machineConfigKernelArgs3,
				machineConfigKernelType,
				machineConfigOSImageURL,
			},
			expectedName:         "rendered-worker-b2628b52cbb66afc4bec5443cc1eebb6",
			expectedSpec:         expectedMachineConfig.Spec,
			osImageURLOverridden: true,
		},
		{
			name: "Extensions",
			inMachineConfigs: []*mcfgv1.MachineConfig{
				machineConfigExtensions1,
				machineConfigExtensions2,
				machineConfigExtensions3,
			},
			expectedName: "rendered-worker-6468e56eb6d08372f691081a036faacb",
			expectedSpec: mcfgv1.MachineConfigSpec{
				Config:          emptyIgnConfig,
				Extensions:      []string{"ext-c", "ext-c", "ext-b", "ext-c", "ext-b", "ext-a"},
				KernelType:      ctrlcommon.KernelTypeDefault,
				KernelArguments: []string{},
			},
		},
		{
			name: "Extensions order does not matter",
			inMachineConfigs: []*mcfgv1.MachineConfig{
				machineConfigExtensions3,
				machineConfigExtensions2,
				machineConfigExtensions1,
			},
			expectedName: "rendered-worker-6468e56eb6d08372f691081a036faacb",
			expectedSpec: mcfgv1.MachineConfigSpec{
				Config:          emptyIgnConfig,
				Extensions:      []string{"ext-c", "ext-c", "ext-b", "ext-c", "ext-b", "ext-a"},
				KernelType:      ctrlcommon.KernelTypeDefault,
				KernelArguments: []string{},
			},
		},
		{
			name: "Kernel arguments",
			inMachineConfigs: []*mcfgv1.MachineConfig{
				machineConfigKernelArgs1,
				machineConfigKernelArgs2,
				machineConfigKernelArgs3,
			},
			expectedName: "rendered-worker-8b51f835df4c664d9d06ccf3d1be7412",
			expectedSpec: mcfgv1.MachineConfigSpec{
				Config:          emptyIgnConfig,
				KernelType:      ctrlcommon.KernelTypeDefault,
				KernelArguments: []string{"karg-c", "karg-c", "karg-b", "karg-c", "karg-b", "karg-a"},
				Extensions:      []string{},
			},
		},
		{
			name: "Kernel arguments order does not matter",
			inMachineConfigs: []*mcfgv1.MachineConfig{
				machineConfigKernelArgs3,
				machineConfigKernelArgs2,
				machineConfigKernelArgs1,
			},
			expectedName: "rendered-worker-8b51f835df4c664d9d06ccf3d1be7412",
			expectedSpec: mcfgv1.MachineConfigSpec{
				Config:          emptyIgnConfig,
				KernelType:      ctrlcommon.KernelTypeDefault,
				KernelArguments: []string{"karg-c", "karg-c", "karg-b", "karg-c", "karg-b", "karg-a"},
				Extensions:      []string{},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cconfig := newControllerConfig("controller-config")
			cconfig.Spec.OSImageURL = "testURL"
			cconfig.Spec.BaseOSContainerImage = "newformatURL"

			mcp := helpers.NewMachineConfigPool(ctrlcommon.MachineConfigPoolWorker, helpers.WorkerSelector, nil, "")

			expectedMachineConfig := &mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						ctrlcommon.GeneratedByControllerVersionAnnotationKey: "was-not-built-properly",
						ctrlcommon.ReleaseImageVersionAnnotationKey:          "",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "machineconfiguration.openshift.io/v1",
							Kind:               "MachineConfigPool",
							Name:               "worker",
							UID:                mcp.UID,
							Controller:         helpers.BoolToPtr(true),
							BlockOwnerDeletion: helpers.BoolToPtr(true),
						},
					},
				},
				Spec: testCase.expectedSpec,
			}

			if testCase.osImageURLOverridden {
				metav1.SetMetaDataAnnotation(&expectedMachineConfig.ObjectMeta, ctrlcommon.OSImageURLOverriddenKey, "true")
			} else {
				expectedMachineConfig.Spec.OSImageURL = cconfig.Spec.BaseOSContainerImage
			}

			renderedMachineConfig, err := generateRenderedMachineConfig(mcp, testCase.inMachineConfigs, cconfig)
			require.NoError(t, err)

			assert.Equal(t, renderedMachineConfig.Name, testCase.expectedName)

			expectedMachineConfig.Name = renderedMachineConfig.Name

			assert.Equal(t, *renderedMachineConfig, *expectedMachineConfig)
		})
	}
}
