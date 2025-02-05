package kubeletconfig

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
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
	"k8s.io/klog/v2"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"k8s.io/utils/ptr"

	osev1 "github.com/openshift/api/config/v1"
	oseconfigfake "github.com/openshift/client-go/config/clientset/versioned/fake"
	oseinformersv1 "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/openshift/machine-config-operator/test/helpers"
)

var alwaysReady = func() bool { return true }

const (
	templateDir = "../../../templates"
)

type fixture struct {
	t *testing.T

	client    *fake.Clientset
	oseclient *oseconfigfake.Clientset

	ccLister        []*mcfgv1.ControllerConfig
	mcpLister       []*mcfgv1.MachineConfigPool
	mckLister       []*mcfgv1.KubeletConfig
	featLister      []*osev1.FeatureGate
	nodeLister      []*osev1.Node
	apiserverLister []*osev1.APIServer

	actions               []core.Action
	skipActionsValidation bool

	objects    []runtime.Object
	oseobjects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.oseobjects = []runtime.Object{}
	f.apiserverLister = []*osev1.APIServer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: ctrlcommon.APIServerInstanceName,
			},
		},
	}
	return f
}

func (f *fixture) validateActions() {
	if f.skipActionsValidation {
		f.t.Log("Skipping actions validation")
		return
	}
	actions := filterInformerActions(f.client.Actions())
	if len(f.actions) != len(actions) {
		f.t.Log("Expected Actions:")
		for i := range f.actions {
			f.t.Logf("\t%v %#v", f.actions[i].GetVerb(), f.actions[i])
		}
		f.t.Log("Seen Actions:")
		for i := range actions {
			f.t.Logf("\t%v %#v", actions[i].GetVerb(), actions[i])
		}
		f.t.Errorf("number of seen actions do not match expected actions count")
		return
	}
	for i, action := range actions {
		klog.Infof("  Action: %v", action)

		if len(f.actions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(f.actions), actions[i:])
			break
		}

		expectedAction := f.actions[i]
		checkAction(expectedAction, action, f.t, i)
	}

	if len(f.actions) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.actions)-len(actions), f.actions[len(actions):])
	}
}

func newFeatures(name string, enabled, disabled []string, labels map[string]string) *osev1.FeatureGate {
	if labels == nil {
		labels = map[string]string{}
	}
	return &osev1.FeatureGate{
		TypeMeta:   metav1.TypeMeta{APIVersion: osev1.GroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, UID: types.UID(utilrand.String(5))},
		Spec:       osev1.FeatureGateSpec{FeatureGateSelection: osev1.FeatureGateSelection{FeatureSet: ""}},
	}
}

func newControllerConfig(name string, platform osev1.PlatformType) *mcfgv1.ControllerConfig {
	cc := &mcfgv1.ControllerConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5))},
		Spec: mcfgv1.ControllerConfigSpec{
			Infra: &osev1.Infrastructure{
				Status: osev1.InfrastructureStatus{
					EtcdDiscoveryDomain: fmt.Sprintf("%s.tt.testing", name),
					PlatformStatus: &osev1.PlatformStatus{
						Type: platform,
					},
					APIServerURL:         fmt.Sprintf("https://api.%s.tt.testing:6443", name),
					APIServerInternalURL: fmt.Sprintf("https://api-int.%s.tt.testing:6443", name),
				},
			},
		},
		Status: mcfgv1.ControllerConfigStatus{
			Conditions: []mcfgv1.ControllerConfigStatusCondition{
				{
					Type:    mcfgv1.TemplateControllerCompleted,
					Status:  corev1.ConditionTrue,
					Message: "",
				},
				{
					Type:    mcfgv1.TemplateControllerRunning,
					Status:  corev1.ConditionFalse,
					Message: "",
				},
				{
					Type:    mcfgv1.TemplateControllerFailing,
					Status:  corev1.ConditionFalse,
					Message: "",
				},
			},
		},
	}
	return cc
}

func newKubeletConfig(name string, kubeconf *kubeletconfigv1beta1.KubeletConfiguration, selector *metav1.LabelSelector) *mcfgv1.KubeletConfig {
	kcRaw, err := EncodeKubeletConfig(kubeconf, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeJSON)
	if err != nil {
		panic(err)
	}

	return &mcfgv1.KubeletConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1, CreationTimestamp: metav1.Now()},
		Spec: mcfgv1.KubeletConfigSpec{
			LogLevel: ptr.To[int32](2),
			KubeletConfig: &runtime.RawExtension{
				Raw: kcRaw,
			},
			MachineConfigPoolSelector: selector,
		},
		Status: mcfgv1.KubeletConfigStatus{},
	}
}

func (f *fixture) newController(fgAccess featuregates.FeatureGateAccess) *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.oseclient = oseconfigfake.NewSimpleClientset(f.oseobjects...)

	i := informers.NewSharedInformerFactory(f.client, 0)
	featinformer := oseinformersv1.NewSharedInformerFactory(f.oseclient, 0)

	if fgAccess == nil {
		fgAccess = featuregates.NewHardcodedFeatureGateAccess(nil, nil)
	}

	c := New(templateDir,
		i.Machineconfiguration().V1().MachineConfigPools(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().KubeletConfigs(),
		featinformer.Config().V1().FeatureGates(),
		featinformer.Config().V1().Nodes(),
		featinformer.Config().V1().APIServers(),
		k8sfake.NewSimpleClientset(),
		f.client,
		f.oseclient,
		fgAccess,
	)
	c.mcpListerSynced = alwaysReady
	c.mckListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.featListerSynced = alwaysReady
	c.nodeConfigListerSynced = alwaysReady
	c.apiserverListerSynced = alwaysReady
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
	for _, c := range f.mckLister {
		i.Machineconfiguration().V1().KubeletConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.featLister {
		featinformer.Config().V1().FeatureGates().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.apiserverLister {
		featinformer.Config().V1().APIServers().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.nodeLister {
		featinformer.Config().V1().Nodes().Informer().GetIndexer().Add(c)
	}

	return c
}

func (f *fixture) run(mcpname string) {
	f.runController(mcpname, false)
}

func (f *fixture) runFeature(featname string, fgAccess featuregates.FeatureGateAccess) {
	f.runFeatureController(featname, false, fgAccess)
}

func (f *fixture) runNode(nodename string) {
	f.runNodeController(nodename, false)
}

func (f *fixture) runExpectError(mcpname string) {
	f.runController(mcpname, true)
}

func (f *fixture) runController(mcpname string, expectError bool) {
	c := f.newController(nil)

	err := c.syncHandler(mcpname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing kubeletconfigs: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing kubeletconfigs, got nil")
	}

	f.validateActions()
}

func (f *fixture) runFeatureController(featname string, expectError bool, fgAccess featuregates.FeatureGateAccess) {
	c := f.newController(fgAccess)

	err := c.syncFeatureHandler(featname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing kubeletconfigs: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing kubeletconfigs, got nil")
	}

	f.validateActions()
}

func (f *fixture) runNodeController(nodename string, expectError bool) {
	c := f.newController(nil)
	err := c.syncNodeConfigHandler(nodename)
	if !expectError && err != nil {
		f.t.Errorf("error syncing node configs: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing node configs, got nil")
	}

	f.validateActions()
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
				action.Matches("list", "kubeletconfigs") ||
				action.Matches("watch", "kubeletconfigs") ||
				action.Matches("list", "machineconfigs") ||
				action.Matches("watch", "machineconfigs")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

// filterOSEActions filters list and watch actions for testing resources.
// Since list and watch don't change resource state we can filter it to lower
// nose level in our tests.
func filterOSEActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		ret = append(ret, action)
	}
	return ret
}

// checkAction verifies that expected and actual actions are equal and both have
// same attached resources
func checkAction(expected, actual core.Action, t *testing.T, index int) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		if actual.GetVerb() == "patch" {
			actual := actual.(core.PatchAction)
			t.Errorf("Expected(index=%v)\n\t%#v\ngot\n\t%#v\npatch\t%#v", index, expected, actual, string(actual.GetPatch()))
		} else {
			t.Errorf("Expected(index=%v)\n\t%#v\ngot\n\t%#v", index, expected, actual)
		}
		return
	}

	if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
		t.Errorf("Action has wrong type. Expected: %t. Got: %t", expected, actual)
		return
	}

	switch a := actual.(type) {
	case core.CreateAction:
		e, _ := expected.(core.CreateAction)

		if a.GetVerb() != e.GetVerb() || a.GetResource().Resource != e.GetResource().Resource {
			t.Errorf("Action %s:%s has wrong Resource %s:%s", a.GetVerb(), e.GetVerb(), a.GetResource().Resource, e.GetResource().Resource)
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

		if !equality.Semantic.DeepEqual(expPatch, patch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expPatch, patch))
		}
	}
}

func (f *fixture) expectGetKubeletConfigAction(config *mcfgv1.KubeletConfig) {
	f.actions = append(f.actions, core.NewRootGetAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"}, config.Name))
}

func (f *fixture) expectCreateKubeletConfigAction(config *mcfgv1.KubeletConfig) {
	f.actions = append(f.actions, core.NewRootCreateAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"}, config))
}

func (f *fixture) expectGetMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootGetAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "machineconfigs"}, config.Name))
}

func (f *fixture) expectCreateMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootCreateAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "machineconfigs"}, config))
}

func (f *fixture) expectUpdateMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootUpdateAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "machineconfigs"}, config))
}

func (f *fixture) expectPatchKubeletConfig(config *mcfgv1.KubeletConfig, patch []byte) {
	f.actions = append(f.actions, core.NewRootPatchAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"}, config.Name, types.MergePatchType, patch))
}

func (f *fixture) expectUpdateKubeletConfig(config *mcfgv1.KubeletConfig) {
	f.actions = append(f.actions, core.NewRootUpdateSubresourceAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"}, "status", config))
}

func (f *fixture) expectUpdateKubeletConfigRoot(config *mcfgv1.KubeletConfig) {
	f.actions = append(f.actions, core.NewRootUpdateAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"}, config))
}

func (f *fixture) resetActions() {
	f.actions = []core.Action{}
	f.client.ClearActions()
}

var kcfgPatchBytes = []byte(`{"metadata":{"finalizers":["99-master-generated-kubelet"]}}`)

func TestKubeletConfigCreate(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, f.client, kc1)
			mcs := helpers.NewMachineConfig(kubeletConfigKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedKubeletConfigKeyDeprecated(mcp)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectUpdateKubeletConfigRoot(kc1)
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, kcfgPatchBytes)
			f.expectUpdateKubeletConfig(kc1)

			f.run(getKey(kc1, t))
		})
	}
}

func TestKubeletConfigMultiCreate(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			kcCount := 30
			for i := 0; i < kcCount; i++ {
				f.resetActions()

				poolName := fmt.Sprintf("subpool%v", i)
				poolLabelName := fmt.Sprintf("pools.operator.machineconfiguration.openshift.io/%s", poolName)
				labelSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, poolLabelName, "")

				mcp := helpers.NewMachineConfigPool(poolName, nil, labelSelector, "v0")
				mcp.ObjectMeta.Labels[poolLabelName] = ""

				kc := newKubeletConfig(poolName, &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, labelSelector)

				f.mcpLister = append(f.mcpLister, mcp)
				f.mckLister = append(f.mckLister, kc)
				f.objects = append(f.objects, kc)

				mcs := helpers.NewMachineConfig(generateManagedKey(kc, 1), labelSelector.MatchLabels, "dummy://", []ign3types.File{{}})
				mcsDeprecated := mcs.DeepCopy()
				mcsDeprecated.Name = getManagedKubeletConfigKeyDeprecated(mcp)

				expectedPatch := fmt.Sprintf("{\"metadata\":{\"finalizers\":[\"99-%v-generated-kubelet\"]}}", poolName)

				f.expectGetMachineConfigAction(mcs)
				f.expectGetMachineConfigAction(mcsDeprecated)
				f.expectGetMachineConfigAction(mcs)
				f.expectUpdateKubeletConfigRoot(kc)
				f.expectCreateMachineConfigAction(mcs)
				f.expectPatchKubeletConfig(kc, []byte(expectedPatch))
				f.expectUpdateKubeletConfig(kc)
				f.run(poolName)
			}
		})
	}
}

func generateManagedKey(kcfg *mcfgv1.KubeletConfig, generation uint64) string {
	return fmt.Sprintf("99-%s-generated-kubelet-%v", kcfg.Name, generation)
}

func TestKubeletConfigAutoSizingReserved(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			autoSizingReservedEnabled := true
			kc1 := &mcfgv1.KubeletConfig{
				TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
				ObjectMeta: metav1.ObjectMeta{Name: "kubulet-log", UID: types.UID(utilrand.String(5)), Generation: 1},
				Spec: mcfgv1.KubeletConfigSpec{
					AutoSizingReserved:        &autoSizingReservedEnabled,
					MachineConfigPoolSelector: metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""),
				},
				Status: mcfgv1.KubeletConfigStatus{},
			}
			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, f.client, kc1)
			mcs := helpers.NewMachineConfig(kubeletConfigKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedKubeletConfigKeyDeprecated(mcp)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectUpdateKubeletConfigRoot(kc1)
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, kcfgPatchBytes)
			f.expectUpdateKubeletConfig(kc1)

			f.run(getKey(kc1, t))
		})
	}
}

func TestKubeletConfiglogFile(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := &mcfgv1.KubeletConfig{
				TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
				ObjectMeta: metav1.ObjectMeta{Name: "kubulet-log", UID: types.UID(utilrand.String(5)), Generation: 1},
				Spec: mcfgv1.KubeletConfigSpec{
					LogLevel:                  ptr.To[int32](5),
					MachineConfigPoolSelector: metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""),
				},
				Status: mcfgv1.KubeletConfigStatus{},
			}
			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, f.client, kc1)
			mcs := helpers.NewMachineConfig(kubeletConfigKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedKubeletConfigKeyDeprecated(mcp)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectUpdateKubeletConfigRoot(kc1)
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, kcfgPatchBytes)
			f.expectUpdateKubeletConfig(kc1)

			f.run(getKey(kc1, t))
		})
	}
}

func TestKubeletConfigUpdates(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, f.client, kc1)
			mcs := helpers.NewMachineConfig(kubeletConfigKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedKubeletConfigKeyDeprecated(mcp)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectUpdateKubeletConfigRoot(kc1)
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, kcfgPatchBytes)
			f.expectUpdateKubeletConfig(kc1)

			c := f.newController(fgAccess)
			stopCh := make(chan struct{})

			err := c.syncHandler(getKey(kc1, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			f.validateActions()
			close(stopCh)

			// Perform Update
			f = newFixture(t)

			// Modify config
			kcUpdate := kc1.DeepCopy()
			kcDecoded, err := DecodeKubeletConfig(kcUpdate.Spec.KubeletConfig.Raw)
			if err != nil {
				t.Errorf("KubeletConfig could not be unmarshalled")
			}
			kcDecoded.MaxPods = 101
			kcRaw, err := EncodeKubeletConfig(kcDecoded, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeJSON)
			if err != nil {
				t.Errorf("KubeletConfig could not be marshalled")
			}
			kcUpdate.Spec.KubeletConfig.Raw = kcRaw

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, mcs, kcUpdate) // MachineConfig exists

			c = f.newController(fgAccess)
			stopCh = make(chan struct{})

			klog.Info("Applying update")

			// Apply update
			err = c.syncHandler(getKey(kcUpdate, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcs)
			f.expectUpdateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kcUpdate, kcfgPatchBytes)
			f.expectUpdateKubeletConfig(kcUpdate)

			f.validateActions()

			close(stopCh)
		})
	}
}

func TestKubeletConfigDenylistedOptions(t *testing.T) {
	failureTests := []struct {
		name   string
		config *kubeletconfigv1beta1.KubeletConfiguration
	}{
		{
			name: "test banned cgroupdriver",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				CgroupDriver: "some_value",
			},
		},
		{
			name: "test banned clusterdns",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				ClusterDNS: []string{"1.1.1.1"},
			},
		},
		{
			name: "test banned clusterdomain",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				ClusterDomain: "some_value",
			},
		},
		{
			name: "test banned staticpodpath",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				StaticPodPath: "some_value",
			},
		},
		{
			name: "user cannot supply features gates",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				FeatureGates: map[string]bool{
					"SomeFeatureGate": true,
				},
			},
		},
	}

	successTests := []struct {
		name   string
		config *kubeletconfigv1beta1.KubeletConfiguration
	}{
		{
			name: "test maxpods",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				MaxPods: 100,
			},
		},
	}

	// Failure Tests
	for _, test := range failureTests {
		kc := newKubeletConfig(test.name, test.config, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "", ""))
		err := validateUserKubeletConfig(kc)
		if err == nil {
			t.Errorf("%s: failed", test.name)
		}
	}

	// Successful Tests
	for _, test := range successTests {
		kc := newKubeletConfig(test.name, test.config, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "", ""))
		err := validateUserKubeletConfig(kc)
		if err != nil {
			t.Errorf("%s: failed with %v. should have succeeded", test.name, err)
		}
	}
}

func TestKubeletFeatureExists(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "Unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, f.client, kc1)
			mcs := helpers.NewMachineConfig(kubeletConfigKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedKubeletConfigKeyDeprecated(mcp)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)

			features := newFeatures("cluster", []string{"DynamicAuditing"}, []string{"ExpandPersistentVolumes"}, nil)
			f.featLister = append(f.featLister, features)

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcsDeprecated)
			f.expectGetMachineConfigAction(mcs)
			f.expectUpdateKubeletConfigRoot(kc1)
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, kcfgPatchBytes)
			f.expectUpdateKubeletConfig(kc1)

			f.run(getKey(kc1, t))
		})
	}
}

func getKey(config *mcfgv1.KubeletConfig, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(config)
	if err != nil {
		t.Errorf("Unexpected error getting key for config %v: %v", config.Name, err)
		return ""
	}
	return key
}

func getKeyFromFeatureGate(gate *osev1.FeatureGate, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(gate)
	if err != nil {
		t.Errorf("Unexpected error getting key for FeatureGate %v: %v", gate.Name, err)
		return ""
	}
	return key
}

func getKeyFromConfigNode(node *osev1.Node, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(node)
	if err != nil {
		t.Errorf("Unexpected error getting key for Config Node %v: %v", node.Name, err)
		return ""
	}
	return key
}

func TestCleanUpStatusConditions(t *testing.T) {
	type status struct {
		curtStatus   []mcfgv1.KubeletConfigCondition
		newStatus    mcfgv1.KubeletConfigCondition
		expectStatus []mcfgv1.KubeletConfigCondition
	}

	newDiffCondition := mcfgv1.KubeletConfigCondition{
		LastTransitionTime: metav1.Now(),
		Message:            "Status: new",
	}
	newSameCondition := mcfgv1.KubeletConfigCondition{
		LastTransitionTime: metav1.Now(),
		Message:            "same status",
	}
	conditionsTest := []status{
		// append new status to current conditions
		{
			curtStatus: []mcfgv1.KubeletConfigCondition{},
			newStatus:  newDiffCondition,
			expectStatus: []mcfgv1.KubeletConfigCondition{{
				LastTransitionTime: newDiffCondition.LastTransitionTime,
				Message:            newDiffCondition.Message,
			}},
		},
		// update only timestamp
		{
			curtStatus: []mcfgv1.KubeletConfigCondition{
				{
					Message: "same status",
				},
				{
					Message: "same status",
				},
			},
			newStatus: newSameCondition,
			expectStatus: []mcfgv1.KubeletConfigCondition{
				{
					Message: "same status",
				},
				newSameCondition,
			},
		},
		// append new condition keeps status limit to 3
		{
			curtStatus: []mcfgv1.KubeletConfigCondition{
				{
					Message: "status 1",
				},
				{
					Message: "status 2",
				},
				{
					Message: "status 3",
				},
			},
			newStatus: newDiffCondition,
			expectStatus: []mcfgv1.KubeletConfigCondition{
				{
					Message: "status 2",
				},
				{
					Message: "status 3",
				},
				newDiffCondition,
			},
		},
	}

	for _, tc := range conditionsTest {
		cleanUpStatusConditions(&tc.curtStatus, tc.newStatus)
		for i := 0; i < len(tc.expectStatus); i++ {
			if tc.curtStatus[i].Message != tc.expectStatus[i].Message {
				t.Fatal("expect: ", tc.expectStatus, "actual: ", tc.curtStatus[i])
			}
		}
	}
}

func TestKubeletConfigResync(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kc2 := newKubeletConfig("smaller-max-pods-2", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 200}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, f.client, kc1)
			mcs := helpers.NewMachineConfig(kubeletConfigKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedKubeletConfigKeyDeprecated(mcp)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)

			c := f.newController(fgAccess)
			err := c.syncHandler(getKey(kc1, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			f.mckLister = append(f.mckLister, kc2)
			f.objects = append(f.objects, kc2)

			c = f.newController(fgAccess)
			err = c.syncHandler(getKey(kc2, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			val := kc2.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "1", val)

			val = kc1.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)

			// resync kc1 and kc2
			c = f.newController(fgAccess)
			err = c.syncHandler(getKey(kc1, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}
			val = kc1.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)

			c = f.newController(fgAccess)
			err = c.syncHandler(getKey(kc2, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}
			val = kc2.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "1", val)
		})
	}
}

func TestAddAnnotationExistingKubeletConfig(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")

			kcMCKey := "99-master-generated-kubelet"
			kc1MCKey := "99-master-generated-kubelet-1"
			kc := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kc.Finalizers = []string{kcMCKey}
			kc1 := newKubeletConfig("smaller-max-pods-1", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 200}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kc1.SetAnnotations(map[string]string{ctrlcommon.MCNameSuffixAnnotationKey: "1"})
			kc1.Finalizers = []string{kc1MCKey}
			kcMC := helpers.NewMachineConfig(kcMCKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc, kc1)
			f.objects = append(f.objects, kc, kc1, kcMC)

			// kc created before kc1,
			// make sure kc does not have annotation machineconfiguration.openshift.io/mc-name-suffix before sync, kc1 has annotation machineconfiguration.openshift.io/mc-name-suffix
			_, ok := kc.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.False(t, ok)
			val := kc1.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "1", val)

			// no new machine config will be created
			f.expectGetMachineConfigAction(kcMC)
			f.expectGetMachineConfigAction(kcMC)
			f.expectUpdateKubeletConfigRoot(kc)
			f.expectUpdateMachineConfigAction(kcMC)
			f.expectPatchKubeletConfig(kc, []byte("{}"))
			f.expectUpdateKubeletConfig(kc)

			c := f.newController(fgAccess)
			err := c.syncHandler(getKey(kc, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			f.validateActions()

			// kc annotation machineconfiguration.openshift.io/mc-name-suffix after sync
			val = kc.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)
			kcFinalizers := kc.GetFinalizers()
			require.Equal(t, 1, len(kcFinalizers))
			require.Equal(t, kcMCKey, kcFinalizers[0])
		})
	}
}

// TestCleanUpDuplicatedMC test the function removes the MC from the MC list
// if the MC is of old GeneratedByControllerVersionAnnotationKey.
func TestCleanUpDuplicatedMC(t *testing.T) {
	v := version.Hash
	version.Hash = "3.2.0"
	versionDegrade := "3.1.0"
	defer func() {
		version.Hash = v
	}()
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgAccess)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)

			// test case: kubeletconfig kc1, two machine config was generated from it: 99-master-generated-kubelet, 99-master-generated-kubelet-1
			// action: upgrade, only 99-master-generated-kubelet-1 will update GeneratedByControllerVersionAnnotationKey
			// expect result: 99-master-generated-kubelet will be deleted
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kc1.SetAnnotations(map[string]string{
				ctrlcommon.MCNameSuffixAnnotationKey: "1",
			})
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)

			ctrl := f.newController(fgAccess)

			// machineconfig with wrong version needs to be removed
			machineConfigDegrade := mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "99-master-generated-kubelet", UID: types.UID(utilrand.String(5))},
			}
			machineConfigDegrade.Annotations = make(map[string]string)
			machineConfigDegrade.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = versionDegrade
			ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), &machineConfigDegrade, metav1.CreateOptions{})

			// MC will be upgraded
			machineConfigUpgrade := mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "99-master-generated-kubelet-1", UID: types.UID(utilrand.String(5))},
			}
			machineConfigUpgrade.Annotations = make(map[string]string)
			machineConfigUpgrade.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = versionDegrade
			ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), &machineConfigUpgrade, metav1.CreateOptions{})

			// machine config has no substring "generated-xxx" will stay
			machineConfigDegradeNotGen := mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "custom-kubelet", UID: types.UID(utilrand.String(5))},
			}
			machineConfigDegradeNotGen.Annotations = make(map[string]string)
			machineConfigDegradeNotGen.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = versionDegrade
			ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), &machineConfigDegradeNotGen, metav1.CreateOptions{})

			// before the upgrade, 3 machine config exist
			mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, mcList.Items, 3)

			if err := ctrl.syncHandler(getKey(kc1, t)); err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			// successful test: only custom and upgraded MCs stay
			mcList, err = ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
			require.NoError(t, err)
			assert.Equal(t, 2, len(mcList.Items))
			actual := make(map[string]mcfgv1.MachineConfig)
			for _, mc := range mcList.Items {
				require.GreaterOrEqual(t, len(mc.Annotations), 1)
				actual[mc.Name] = mc
			}
			_, ok := actual[machineConfigDegradeNotGen.Name]
			require.True(t, ok, "expect custom-kubelet in the list, but got false")
			_, ok = actual[machineConfigUpgrade.Name]
			require.True(t, ok, "expect 99-master-generated-kubelet-1 in the list, but got false")
		})
	}
}

// This test ensures that that the rendering works properly for all types of TLS security profiles
func TestKubeletConfigTLSRender(t *testing.T) {
	TestCases := []struct {
		name      string
		apiserver *osev1.APIServer
	}{
		{
			name:      "Empty API server object",
			apiserver: &osev1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName}},
		},
		{
			name:      "Empty TLS profile",
			apiserver: &osev1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName}},
		},
		{
			name: "Old TLS profile",
			apiserver: &osev1.APIServer{
				ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName},
				Spec: osev1.APIServerSpec{
					TLSSecurityProfile: &osev1.TLSSecurityProfile{Type: osev1.TLSProfileOldType},
				},
			},
		},
		{
			name: "Intermediate TLS profile",
			apiserver: &osev1.APIServer{
				ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName},
				Spec: osev1.APIServerSpec{
					TLSSecurityProfile: &osev1.TLSSecurityProfile{Type: osev1.TLSProfileIntermediateType},
				},
			},
		},
		{
			name: "Custom TLS profile",
			apiserver: &osev1.APIServer{
				ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName},
				Spec: osev1.APIServerSpec{
					TLSSecurityProfile: &osev1.TLSSecurityProfile{
						Type: osev1.TLSProfileCustomType,
						Custom: &osev1.CustomTLSProfile{
							TLSProfileSpec: osev1.TLSProfileSpec{
								Ciphers: []string{
									"TLS_AES_128_GCM_SHA256",
									"TLS_AES_256_GCM_SHA384",
									"TLS_CHACHA20_POLY1305_SHA256",
									"ECDHE-ECDSA-AES128-GCM-SHA256",
									"ECDHE-RSA-AES128-GCM-SHA256",
									"ECDHE-ECDSA-AES256-GCM-SHA384",
								},
								MinTLSVersion: osev1.VersionTLS12,
							},
						},
					},
				},
			},
		},
	}

	for _, testCase := range TestCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, osev1.AWSPlatformType)
			f.ccLister = append(f.ccLister, cc)

			fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
			ctrl := f.newController(fgAccess)

			kubeletConfig, err := generateOriginalKubeletConfigIgn(cc, ctrl.templatesDir, "master", testCase.apiserver)
			if err != nil {
				t.Errorf("could not generate kubelet config from templates %v", err)
			}
			contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletConfig.Contents.Source, kubeletConfig.Contents.Compression)
			require.NoError(t, err)
			originalKubeConfig, err := DecodeKubeletConfig(contents)
			require.NoError(t, err)

			// Generate expected profiles from helper function
			var expectedTLSCipherSuites []string
			var expectedTLSMinVersion string
			// Use an intermediate profile for the nil cases
			if testCase.apiserver == nil || testCase.apiserver.Spec.TLSSecurityProfile == nil {
				expectedTLSMinVersion, expectedTLSCipherSuites = ctrlcommon.GetSecurityProfileCiphers(&osev1.TLSSecurityProfile{Type: osev1.TLSProfileIntermediateType})
			} else {
				expectedTLSMinVersion, expectedTLSCipherSuites = ctrlcommon.GetSecurityProfileCiphers(testCase.apiserver.Spec.TLSSecurityProfile)
			}

			require.Equal(t, originalKubeConfig.TLSCipherSuites, expectedTLSCipherSuites)
			require.Equal(t, originalKubeConfig.TLSMinVersion, expectedTLSMinVersion)

		})
	}
}

// This test ensures that a user defined kubeletConfiguration with a TLS profile will override
// the TLS profile specified by the APIServer object
func TestKubeletConfigTLSOverride(t *testing.T) {

	f := newFixture(t)
	fgAccess := featuregates.NewHardcodedFeatureGateAccess([]osev1.FeatureGateName{"Example"}, nil)
	f.newController(fgAccess)
	cc := newControllerConfig(ctrlcommon.ControllerConfigName, osev1.AWSPlatformType)
	mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
	f.apiserverLister = []*osev1.APIServer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: ctrlcommon.APIServerInstanceName,
			},
			Spec: osev1.APIServerSpec{
				TLSSecurityProfile: &osev1.TLSSecurityProfile{Type: osev1.TLSProfileOldType},
			},
		},
	}

	f.ccLister = append(f.ccLister, cc)
	f.mcpLister = append(f.mcpLister, mcp)

	overrideTLSMinVersion, overrideTLSCiphers := ctrlcommon.GetSecurityProfileCiphers(&osev1.TLSSecurityProfile{Type: osev1.TLSProfileIntermediateType})
	userDefinedKC := newKubeletConfig("tls-override", &kubeletconfigv1beta1.KubeletConfiguration{TLSCipherSuites: overrideTLSCiphers, TLSMinVersion: overrideTLSMinVersion}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

	f.mckLister = append(f.mckLister, userDefinedKC)
	f.objects = append(f.objects, userDefinedKC)

	ctrl := f.newController(fgAccess)

	if err := ctrl.syncHandler(getKey(userDefinedKC, t)); err != nil {
		t.Errorf("syncHandler returned: %v", err)
	}

	// Grab the MachineConfig generated by the controller, and decode the content of the kubelet config file
	generatedConfig, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), "99-master-generated-kubelet", metav1.GetOptions{})
	require.NoError(t, err)

	generatedIgnConfig, err := ctrlcommon.ParseAndConvertConfig(generatedConfig.Spec.Config.Raw)
	require.NoError(t, err)
	var kubeletConfigFile ign3types.File
	for _, file := range generatedIgnConfig.Storage.Files {
		if file.Path == "/etc/kubernetes/kubelet.conf" {
			kubeletConfigFile = file
		}
	}
	require.NotEmpty(t, kubeletConfigFile)

	// Decode this to a native KubeletConfiguration object
	contents, err := ctrlcommon.DecodeIgnitionFileContents(kubeletConfigFile.Contents.Source, kubeletConfigFile.Contents.Compression)
	require.NoError(t, err)
	kc, err := DecodeKubeletConfig(contents)
	require.NoError(t, err)
	require.Equal(t, kc.TLSCipherSuites, overrideTLSCiphers)
	require.Equal(t, kc.TLSMinVersion, overrideTLSMinVersion)

}
