package kubeletconfig

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/golang/glog"
	osev1 "github.com/openshift/api/config/v1"
	oseconfigfake "github.com/openshift/client-go/config/clientset/versioned/fake"
	oseinformersv1 "github.com/openshift/client-go/config/informers/externalversions"
	v1 "k8s.io/api/core/v1"
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
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/machine-config-operator/test/helpers"
)

var (
	alwaysReady = func() bool { return true }
)

const (
	templateDir = "../../../templates"
)

type fixture struct {
	t *testing.T

	client    *fake.Clientset
	oseclient *oseconfigfake.Clientset

	ccLister   []*mcfgv1.ControllerConfig
	mcpLister  []*mcfgv1.MachineConfigPool
	mckLister  []*mcfgv1.KubeletConfig
	featLister []*osev1.FeatureGate

	actions []core.Action

	objects    []runtime.Object
	oseobjects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.oseobjects = []runtime.Object{}
	return f
}

func (f *fixture) validateActions() {
	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		glog.Infof("  Action: %v", action)

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
				},
			},
		},
	}
	return cc
}

func newKubeletConfig(name string, kubeconf *kubeletconfigv1beta1.KubeletConfiguration, selector *metav1.LabelSelector) *mcfgv1.KubeletConfig {
	kcRaw, err := encodeKubeletConfig(kubeconf, kubeletconfigv1beta1.SchemeGroupVersion)
	if err != nil {
		panic(err)
	}

	return &mcfgv1.KubeletConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: mcfgv1.KubeletConfigSpec{
			KubeletConfig: &runtime.RawExtension{
				Raw: kcRaw,
			},
			MachineConfigPoolSelector: selector,
		},
		Status: mcfgv1.KubeletConfigStatus{},
	}
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.oseclient = oseconfigfake.NewSimpleClientset(f.oseobjects...)

	i := informers.NewSharedInformerFactory(f.client, 0)
	featinformer := oseinformersv1.NewSharedInformerFactory(f.oseclient, 0)

	c := New(templateDir,
		i.Machineconfiguration().V1().MachineConfigPools(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().KubeletConfigs(),
		featinformer.Config().V1().FeatureGates(),
		k8sfake.NewSimpleClientset(),
		f.client,
	)
	c.mcpListerSynced = alwaysReady
	c.mckListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.featListerSynced = alwaysReady
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

	return c
}

func (f *fixture) run(mcpname string) {
	f.runController(mcpname, false)
}

func (f *fixture) runFeature(featname string) {
	f.runFeatureController(featname, false)
}

func (f *fixture) runExpectError(mcpname string) {
	f.runController(mcpname, true)
}

func (f *fixture) runController(mcpname string, expectError bool) {
	c := f.newController()

	err := c.syncHandler(mcpname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing kubeletconfigs: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing kubeletconfigs, got nil")
	}

	f.validateActions()
}

func (f *fixture) runFeatureController(featname string, expectError bool) {
	c := f.newController()

	err := c.syncFeatureHandler(featname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing kubeletconfigs: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing kubeletconfigs, got nil")
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

		if !equality.Semantic.DeepEqual(expPatch, expPatch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expPatch, patch))
		}
	}
}

func (f *fixture) expectGetKubeletConfigAction(config *mcfgv1.KubeletConfig) {
	f.actions = append(f.actions, core.NewRootGetAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"}, config.Name))
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

func TestKubeletConfigCreate(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, nil)
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
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, []uint8{0x7b, 0x22, 0x6d, 0x65, 0x74, 0x61, 0x64, 0x61, 0x74, 0x61, 0x22, 0x3a, 0x7b, 0x22, 0x66, 0x69, 0x6e, 0x61, 0x6c, 0x69, 0x7a, 0x65, 0x72, 0x73, 0x22, 0x3a, 0x5b, 0x22, 0x39, 0x39, 0x2d, 0x6d, 0x61, 0x73, 0x74, 0x65, 0x72, 0x2d, 0x68, 0x35, 0x35, 0x32, 0x6d, 0x2d, 0x73, 0x6d, 0x61, 0x6c, 0x6c, 0x65, 0x72, 0x2d, 0x6d, 0x61, 0x78, 0x2d, 0x70, 0x6f, 0x64, 0x73, 0x2d, 0x6b, 0x75, 0x62, 0x65, 0x6c, 0x65, 0x74, 0x22, 0x5d, 0x7d, 0x7d})
			f.expectUpdateKubeletConfig(kc1)

			f.run(getKey(kc1, t))
		})
	}
}

func TestKubeletConfigUpdates(t *testing.T) {
	for _, platform := range []osev1.PlatformType{osev1.AWSPlatformType, osev1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, nil)
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
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, []uint8{0x7b, 0x22, 0x6d, 0x65, 0x74, 0x61, 0x64, 0x61, 0x74, 0x61, 0x22, 0x3a, 0x7b, 0x22, 0x66, 0x69, 0x6e, 0x61, 0x6c, 0x69, 0x7a, 0x65, 0x72, 0x73, 0x22, 0x3a, 0x5b, 0x22, 0x39, 0x39, 0x2d, 0x6d, 0x61, 0x73, 0x74, 0x65, 0x72, 0x2d, 0x68, 0x35, 0x35, 0x32, 0x6d, 0x2d, 0x73, 0x6d, 0x61, 0x6c, 0x6c, 0x65, 0x72, 0x2d, 0x6d, 0x61, 0x78, 0x2d, 0x70, 0x6f, 0x64, 0x73, 0x2d, 0x6b, 0x75, 0x62, 0x65, 0x6c, 0x65, 0x74, 0x22, 0x5d, 0x7d, 0x7d})
			f.expectUpdateKubeletConfig(kc1)

			c := f.newController()
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
			kcDecoded, err := decodeKubeletConfig(kcUpdate.Spec.KubeletConfig.Raw)
			if err != nil {
				t.Errorf("KubeletConfig could not be unmarshalled")
			}
			kcDecoded.MaxPods = 101
			kcRaw, err := encodeKubeletConfig(kcDecoded, kubeletconfigv1beta1.SchemeGroupVersion)
			if err != nil {
				t.Errorf("KubeletConfig could not be marshalled")
			}
			kcUpdate.Spec.KubeletConfig.Raw = kcRaw

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, mcs, kcUpdate) // MachineConfig exists

			c = f.newController()
			stopCh = make(chan struct{})

			glog.Info("Applying update")

			// Apply update
			err = c.syncHandler(getKey(kcUpdate, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcs)
			f.expectUpdateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kcUpdate, []uint8{0x7b, 0x22, 0x6d, 0x65, 0x74, 0x61, 0x64, 0x61, 0x74, 0x61, 0x22, 0x3a, 0x7b, 0x22, 0x66, 0x69, 0x6e, 0x61, 0x6c, 0x69, 0x7a, 0x65, 0x72, 0x73, 0x22, 0x3a, 0x5b, 0x22, 0x39, 0x39, 0x2d, 0x6d, 0x61, 0x73, 0x74, 0x65, 0x72, 0x2d, 0x6d, 0x77, 0x77, 0x74, 0x67, 0x2d, 0x6b, 0x75, 0x62, 0x65, 0x6c, 0x65, 0x74, 0x22, 0x5d, 0x7d, 0x7d})
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
			name: "test banned runtimerequesttimeout",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				RuntimeRequestTimeout: metav1.Duration{Duration: 1 * time.Minute},
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
		{
			name: "evictionSoft cannot be supplied without evictionSoftGracePeriod",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				EvictionSoft: map[string]string{
					"memory.available": "90%",
				},
			},
		},
		{
			name: "evictionSoft cannot be supplied without corresponding evictionSoftGracePeriod",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				EvictionSoft: map[string]string{
					"memory.available": "90%",
				},
				EvictionSoftGracePeriod: map[string]string{
					"nodefs.inodesFree": "1h",
				},
			},
		},
		{
			name: "kubeReserved cpu value cannot be negative",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					v1.ResourceCPU.String(): "-20",
				},
			},
		},
		{
			name: "systemReserved cpu value cannot be negative",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				SystemReserved: map[string]string{
					v1.ResourceCPU.String(): "-20",
				},
			},
		},
		{
			name: "kubeReserved memory value cannot be negative",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					v1.ResourceMemory.String(): "-20M",
				},
			},
		},
		{
			name: "systemReserved memory value cannot be negative",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				SystemReserved: map[string]string{
					v1.ResourceMemory.String(): "-20M",
				},
			},
		},
		{
			name: "kubeReserved cpu value fails to parse",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					v1.ResourceCPU.String(): "Peter Griffin",
				},
			},
		},
		{
			name: "systemReserved cpu value fails to parse",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				SystemReserved: map[string]string{
					v1.ResourceCPU.String(): "Stewie Griffin",
				},
			},
		},
		{
			name: "kubeReserved memory value fails to parse",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					v1.ResourceMemory.String(): "Brian Griffin",
				},
			},
		},
		{
			name: "systemReserved memory value fails to parse",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				SystemReserved: map[string]string{
					v1.ResourceMemory.String(): "Meg Griffin",
				},
			},
		},
		{
			name: "kubeReserved ephemeral-storage value fails to parse",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					v1.ResourceEphemeralStorage.String(): "Lois Griffin",
				},
			},
		},
		{
			name: "kubeReserved ephemeral-storage value cannot be negative",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				KubeReserved: map[string]string{
					v1.ResourceEphemeralStorage.String(): "-20M",
				},
			},
		},
		{
			name: "systemReserved ephemeral-storage value fails to parse",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				SystemReserved: map[string]string{
					v1.ResourceEphemeralStorage.String(): "Glenn Quagmire",
				},
			},
		},
		{
			name: "systemReserved ephemeral-storage value cannot be negative",
			config: &kubeletconfigv1beta1.KubeletConfiguration{
				SystemReserved: map[string]string{
					v1.ResourceEphemeralStorage.String(): "-20M",
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

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			kubeletConfigKey, _ := getManagedKubeletConfigKey(mcp, nil)
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
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, []uint8{0x7b, 0x22, 0x6d, 0x65, 0x74, 0x61, 0x64, 0x61, 0x74, 0x61, 0x22, 0x3a, 0x7b, 0x22, 0x66, 0x69, 0x6e, 0x61, 0x6c, 0x69, 0x7a, 0x65, 0x72, 0x73, 0x22, 0x3a, 0x5b, 0x22, 0x39, 0x39, 0x2d, 0x6d, 0x61, 0x73, 0x74, 0x65, 0x72, 0x2d, 0x68, 0x35, 0x35, 0x32, 0x6d, 0x2d, 0x73, 0x6d, 0x61, 0x6c, 0x6c, 0x65, 0x72, 0x2d, 0x6d, 0x61, 0x78, 0x2d, 0x70, 0x6f, 0x64, 0x73, 0x2d, 0x6b, 0x75, 0x62, 0x65, 0x6c, 0x65, 0x74, 0x22, 0x5d, 0x7d, 0x7d})
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
