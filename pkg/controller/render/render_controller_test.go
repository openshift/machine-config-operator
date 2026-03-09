package render

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/clarketm/json"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	apicfgv1 "github.com/openshift/api/config/v1"
	configv1 "github.com/openshift/api/config/v1"
	mcopfake "github.com/openshift/client-go/operator/clientset/versioned/fake"
	operatorinformer "github.com/openshift/client-go/operator/informers/externalversions"
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

	client  *fake.Clientset
	oclient *mcopfake.Clientset

	mcpLister []*mcfgv1.MachineConfigPool
	mcLister  []*mcfgv1.MachineConfig
	ccLister  []*mcfgv1.ControllerConfig
	crcLister []*mcfgv1.ContainerRuntimeConfig
	mckLister []*mcfgv1.KubeletConfig

	actions []core.Action

	objects   []runtime.Object
	oObjects  []runtime.Object
	fgHandler ctrlcommon.FeatureGatesHandler
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.oObjects = []runtime.Object{}
	f.fgHandler = ctrlcommon.NewFeatureGatesHardcodedHandler(
		[]apicfgv1.FeatureGateName{},
		[]apicfgv1.FeatureGateName{},
	)
	return f
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.oclient = mcopfake.NewSimpleClientset(f.oObjects...)
	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	oi := operatorinformer.NewSharedInformerFactory(f.oclient, noResyncPeriodFunc())

	c := New(i.Machineconfiguration().V1().MachineConfigPools(), i.Machineconfiguration().V1().MachineConfigs(),
		i.Machineconfiguration().V1().ControllerConfigs(), i.Machineconfiguration().V1().ContainerRuntimeConfigs(),
		i.Machineconfiguration().V1().KubeletConfigs(), oi.Operator().V1().MachineConfigurations(),
		i.Machineconfiguration().V1alpha1().OSImageStreams(), k8sfake.NewSimpleClientset(), f.client, f.fgHandler)

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
				a.GetVerb(), a.GetResource().Resource, diff.Diff(expObject, object))
		}
	case core.UpdateAction:
		e, _ := expected.(core.UpdateAction)
		expObject := e.GetObject()
		object := a.GetObject()

		if !equality.Semantic.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.Diff(expObject, object))
		}
	case core.PatchAction:
		e, _ := expected.(core.PatchAction)
		expPatch := e.GetPatch()
		patch := a.GetPatch()

		if !equality.Semantic.DeepEqual(expPatch, expPatch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.Diff(expPatch, patch))
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

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
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

	_, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
	require.Nil(t, err)

	// verify that an invalid ignition config (here a config with content and an empty version,
	// will fail validation
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mcs[1].Spec.Config.Raw)
	require.Nil(t, err)
	ignCfg.Ignition.Version = ""
	rawIgnCfg, err := json.Marshal(ignCfg)
	require.Nil(t, err)
	mcs[1].Spec.Config.Raw = rawIgnCfg

	_, err = generateRenderedMachineConfig(mcp, mcs, cc, nil)
	require.NotNil(t, err)

	// verify that a machine config with no ignition content will not fail validation
	emptyIgnCfg := ctrlcommon.NewIgnConfig()
	rawEmptyIgnCfg, err := json.Marshal(emptyIgnCfg)
	require.Nil(t, err)
	mcs[1].Spec.Config.Raw = rawEmptyIgnCfg
	mcs[1].Spec.KernelArguments = append(mcs[1].Spec.KernelArguments, "test1")
	_, err = generateRenderedMachineConfig(mcp, mcs, cc, nil)
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

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
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

	expmc, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
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

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "dummy-change", gmc.Spec.OSImageURL)

	mcs = append(mcs, helpers.NewMachineConfig("00-test-cluster-master-1", map[string]string{"node-role/master": ""}, "dummy-change-2", []ign3types.File{}))

	gmc, err = generateAndValidateRenderedMachineConfig(gmc, mcp, mcs, cc, nil, nil)
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
	_, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
	require.NotNil(t, err)

	// Now the same thing without overriding the version
	cc = newControllerConfig(ctrlcommon.ControllerConfigName)
	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
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
	_, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
	require.NotNil(t, err)

	mcs = []*mcfgv1.MachineConfig{
		helpers.NewMachineConfigWithAnnotation("00-updated-conf", map[string]string{"node-role/master": ""}, map[string]string{ctrlcommon.GeneratedByControllerVersionAnnotationKey: "2"}, "dummy-test-1", []ign3types.File{}),
		helpers.NewMachineConfigWithAnnotation("99-user-conf", map[string]string{"node-role/master": ""}, map[string]string{ctrlcommon.GeneratedByControllerVersionAnnotationKey: ""}, "user-data", []ign3types.File{}),
	}
	_, err = generateRenderedMachineConfig(mcp, mcs, cc, nil)
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

	gmc, err := generateRenderedMachineConfig(mcp, mcs, cc, nil)
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
	require.Len(t, queue, 2)
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

	gmc, err := generateAndValidateRenderedMachineConfig(currentMC, mcp, mcs, cc, nil, nil)
	assert.Error(t, err)
	assert.Nil(t, gmc)
}

func TestIsCustomPool(t *testing.T) {
	tests := []struct {
		name     string
		poolName string
		expected bool
	}{
		{
			name:     "master pool is not custom",
			poolName: "master",
			expected: false,
		},
		{
			name:     "worker pool is not custom",
			poolName: "worker",
			expected: false,
		},
		{
			name:     "arbiter pool is not custom",
			poolName: "arbiter",
			expected: false,
		},
		{
			name:     "infra pool is custom",
			poolName: "infra",
			expected: true,
		},
		{
			name:     "custom-role pool is custom",
			poolName: "custom-role",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := helpers.NewMachineConfigPool(tt.poolName, helpers.WorkerSelector, nil, "")
			result := ctrlcommon.IsCustomPool(pool)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetOSImageStreamNameForPool(t *testing.T) {
	tests := []struct {
		name           string
		pool           *mcfgv1.MachineConfigPool
		workerPool     *mcfgv1.MachineConfigPool
		expectedStream string
	}{
		{
			name: "custom pool with explicit stream uses explicit stream",
			pool: func() *mcfgv1.MachineConfigPool {
				p := helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, "")
				p.Spec.OSImageStream.Name = "rhel-10"
				return p
			}(),
			workerPool: func() *mcfgv1.MachineConfigPool {
				p := helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, "")
				p.Spec.OSImageStream.Name = "rhel-9"
				return p
			}(),
			expectedStream: "rhel-10",
		},
		{
			name: "custom pool without stream inherits from worker",
			pool: helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, ""),
			workerPool: func() *mcfgv1.MachineConfigPool {
				p := helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, "")
				p.Spec.OSImageStream.Name = "rhel-9"
				return p
			}(),
			expectedStream: "rhel-9",
		},
		{
			name:           "custom pool without stream, worker has no stream, uses default",
			pool:           helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, ""),
			workerPool:     helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, ""),
			expectedStream: "",
		},
		{
			name:           "custom pool without stream, worker doesn't exist, uses default",
			pool:           helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, ""),
			workerPool:     nil,
			expectedStream: "",
		},
		{
			name: "standard pool (master) with explicit stream uses explicit stream",
			pool: func() *mcfgv1.MachineConfigPool {
				p := helpers.NewMachineConfigPool("master", helpers.MasterSelector, nil, "")
				p.Spec.OSImageStream.Name = "rhel-10"
				return p
			}(),
			workerPool: func() *mcfgv1.MachineConfigPool {
				p := helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, "")
				p.Spec.OSImageStream.Name = "rhel-9"
				return p
			}(),
			expectedStream: "rhel-10",
		},
		{
			name:           "standard pool (worker) without stream uses default",
			pool:           helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, ""),
			workerPool:     nil,
			expectedStream: "",
		},
		{
			name: "standard pool (arbiter) without stream uses default",
			pool: helpers.NewMachineConfigPool("arbiter", &metav1.LabelSelector{
				MatchLabels: map[string]string{"node-role/arbiter": ""},
			}, nil, ""),
			workerPool:     nil,
			expectedStream: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)

			// Add worker pool to lister if provided
			if tt.workerPool != nil {
				f.mcpLister = append(f.mcpLister, tt.workerPool)
				f.objects = append(f.objects, tt.workerPool)
			}

			c := f.newController()

			result := c.getOSImageStreamNameForPool(tt.pool)
			assert.Equal(t, tt.expectedStream, result, "Expected stream name %q, got %q", tt.expectedStream, result)
		})
	}
}

func TestGetOSImageStreamNameForPoolBootstrap(t *testing.T) {
	tests := []struct {
		name           string
		pool           *mcfgv1.MachineConfigPool
		pools          []*mcfgv1.MachineConfigPool
		expectedStream string
	}{
		{
			name: "custom pool with explicit stream uses explicit stream",
			pool: func() *mcfgv1.MachineConfigPool {
				p := helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, "")
				p.Spec.OSImageStream.Name = "rhel-10"
				return p
			}(),
			pools: []*mcfgv1.MachineConfigPool{
				func() *mcfgv1.MachineConfigPool {
					p := helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, "")
					p.Spec.OSImageStream.Name = "rhel-9"
					return p
				}(),
			},
			expectedStream: "rhel-10",
		},
		{
			name: "custom pool without stream inherits from worker",
			pool: helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, ""),
			pools: []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", helpers.MasterSelector, nil, ""),
				func() *mcfgv1.MachineConfigPool {
					p := helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, "")
					p.Spec.OSImageStream.Name = "rhel-9"
					return p
				}(),
			},
			expectedStream: "rhel-9",
		},
		{
			name: "custom pool without stream, worker has no stream, uses default",
			pool: helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, ""),
			pools: []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, ""),
			},
			expectedStream: "",
		},
		{
			name: "custom pool without stream, no worker pool exists, uses default",
			pool: helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, ""),
			pools: []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", helpers.MasterSelector, nil, ""),
			},
			expectedStream: "",
		},
		{
			name: "standard pool (master) with explicit stream uses explicit stream",
			pool: func() *mcfgv1.MachineConfigPool {
				p := helpers.NewMachineConfigPool("master", helpers.MasterSelector, nil, "")
				p.Spec.OSImageStream.Name = "rhel-10"
				return p
			}(),
			pools: []*mcfgv1.MachineConfigPool{
				func() *mcfgv1.MachineConfigPool {
					p := helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, "")
					p.Spec.OSImageStream.Name = "rhel-9"
					return p
				}(),
			},
			expectedStream: "rhel-10",
		},
		{
			name: "standard pool (worker) without stream uses default",
			pool: helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, ""),
			pools: []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", helpers.MasterSelector, nil, ""),
			},
			expectedStream: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getOSImageStreamNameForPoolBootstrap(tt.pool, tt.pools)
			assert.Equal(t, tt.expectedStream, result, "Expected stream name %q, got %q", tt.expectedStream, result)
		})
	}
}

func TestWorkerPoolOSImageStreamChangeEnqueuesCustomPools(t *testing.T) {
	f := newFixture(t)

	// Create worker pool with osImageStream
	workerPool := helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, "")
	workerPool.Spec.OSImageStream.Name = "rhel-9"

	// Create custom pools
	infraPool := helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, "")
	customPool := helpers.NewMachineConfigPool("custom", helpers.InfraSelector, nil, "")

	// Add pools to lister
	f.mcpLister = append(f.mcpLister, workerPool, infraPool, customPool)
	f.objects = append(f.objects, workerPool, infraPool, customPool)

	c := f.newController()

	// Track which pools get enqueued
	enqueuedPools := []string{}
	c.enqueueMachineConfigPool = func(pool *mcfgv1.MachineConfigPool) {
		enqueuedPools = append(enqueuedPools, pool.Name)
	}

	// Update worker pool with new osImageStream
	oldWorkerPool := workerPool.DeepCopy()
	newWorkerPool := workerPool.DeepCopy()
	newWorkerPool.Spec.OSImageStream.Name = "rhel-10"

	c.updateMachineConfigPool(oldWorkerPool, newWorkerPool)

	// Verify worker pool was enqueued (normal behavior)
	assert.Contains(t, enqueuedPools, "worker", "Worker pool should be enqueued")

	// Verify custom pools were also enqueued
	assert.Contains(t, enqueuedPools, "infra", "Infra pool should be enqueued when worker osImageStream changes")
	assert.Contains(t, enqueuedPools, "custom", "Custom pool should be enqueued when worker osImageStream changes")

	// Verify master pool is NOT enqueued (we only have worker and custom pools)
	assert.NotContains(t, enqueuedPools, "master", "Master pool should not be enqueued")
}

func TestWorkerPoolOtherChangeDoesNotEnqueueCustomPools(t *testing.T) {
	f := newFixture(t)

	// Create worker pool
	workerPool := helpers.NewMachineConfigPool("worker", helpers.WorkerSelector, nil, "")
	workerPool.Spec.OSImageStream.Name = "rhel-9"

	// Create custom pool
	infraPool := helpers.NewMachineConfigPool("infra", helpers.InfraSelector, nil, "")

	// Add pools to lister
	f.mcpLister = append(f.mcpLister, workerPool, infraPool)
	f.objects = append(f.objects, workerPool, infraPool)

	c := f.newController()

	// Track which pools get enqueued
	enqueuedPools := []string{}
	c.enqueueMachineConfigPool = func(pool *mcfgv1.MachineConfigPool) {
		enqueuedPools = append(enqueuedPools, pool.Name)
	}

	// Update worker pool with SAME osImageStream but different field (e.g., paused)
	oldWorkerPool := workerPool.DeepCopy()
	newWorkerPool := workerPool.DeepCopy()
	newWorkerPool.Spec.Paused = true

	c.updateMachineConfigPool(oldWorkerPool, newWorkerPool)

	// Verify only worker pool was enqueued, NOT custom pools
	assert.Contains(t, enqueuedPools, "worker", "Worker pool should be enqueued")
	assert.NotContains(t, enqueuedPools, "infra", "Infra pool should NOT be enqueued when osImageStream unchanged")
	assert.Len(t, enqueuedPools, 1, "Only worker pool should be enqueued")
}
