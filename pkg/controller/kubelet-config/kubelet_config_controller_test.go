package kubeletconfig

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/golang/glog"

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
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

const (
	templateDir = "../../../templates"
)

type fixture struct {
	t *testing.T

	client *fake.Clientset

	ccLister  []*mcfgv1.ControllerConfig
	mcpLister []*mcfgv1.MachineConfigPool
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

func newMachineConfig(name string, labels map[string]string, osurl string, files []ignv2_2types.File) *mcfgv1.MachineConfig {
	if labels == nil {
		labels = map[string]string{}
	}
	return &mcfgv1.MachineConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, UID: types.UID(utilrand.String(5))},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: osurl,
			Config:     ignv2_2types.Config{Storage: ignv2_2types.Storage{Files: files}},
		},
	}
}

func newControllerConfig(name string) *mcfgv1.ControllerConfig {
	cc := &mcfgv1.ControllerConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5))},
		Spec: mcfgv1.ControllerConfigSpec{
			EtcdDiscoveryDomain: fmt.Sprintf("%s.tt.testing", name),
		},
	}
	return cc
}

func newMachineConfigPool(name string, labels map[string]string, selector *metav1.LabelSelector, currentMachineConfig string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, UID: types.UID(utilrand.String(5))},
		Spec: mcfgv1.MachineConfigPoolSpec{
			MachineConfigSelector: selector,
		},
		Status: mcfgv1.MachineConfigPoolStatus{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{ObjectReference: corev1.ObjectReference{Name: currentMachineConfig}},
		},
	}
}

func newKubeletConfig(name string, kubeconf *kubeletconfigv1beta1.KubeletConfiguration, selector *metav1.LabelSelector) *mcfgv1.KubeletConfig {
	return &mcfgv1.KubeletConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: mcfgv1.KubeletConfigSpec{
			KubeletConfig:             kubeconf,
			MachineConfigPoolSelector: selector,
		},
		Status: mcfgv1.KubeletConfigStatus{},
	}
}

func (f *fixture) newController() (*Controller, informers.SharedInformerFactory) {
	f.client = fake.NewSimpleClientset(f.objects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	c := New(templateDir,
		i.Machineconfiguration().V1().MachineConfigPools(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().KubeletConfigs(),
		k8sfake.NewSimpleClientset(), f.client)

	c.mcpListerSynced = alwaysReady
	c.mckListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mcpLister {
		i.Machineconfiguration().V1().MachineConfigPools().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mckLister {
		i.Machineconfiguration().V1().KubeletConfigs().Informer().GetIndexer().Add(c)
	}

	return c, i
}

func (f *fixture) run(mcpname string) {
	f.runController(mcpname, true, false)
}

func (f *fixture) runExpectError(mcpname string) {
	f.runController(mcpname, true, true)
}

func (f *fixture) runController(mcpname string, startInformers bool, expectError bool) {
	c, i := f.newController()
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		i.Start(stopCh)
	}

	err := c.syncHandler(mcpname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing kubeletconfigs: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing kubeletconfigs, got nil")
	}

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
	f.actions = append(f.actions, core.NewRootGetAction(schema.GroupVersionResource{Resource: "kubeletconfigs"}, config.Name))
}

func (f *fixture) expectGetMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootGetAction(schema.GroupVersionResource{Resource: "machineconfigs"}, config.Name))
}

func (f *fixture) expectCreateMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootCreateAction(schema.GroupVersionResource{Resource: "machineconfigs"}, config))
}

func (f *fixture) expectPatchKubeletConfig(config *mcfgv1.KubeletConfig, patch []byte) {
	f.actions = append(f.actions, core.NewRootPatchAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"}, config.Name, patch))
}

func (f *fixture) expectUpdateKubeletConfig(config *mcfgv1.KubeletConfig) {
	f.actions = append(f.actions, core.NewRootUpdateSubresourceAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "kubeletconfigs"}, "status", config))
}

func TestKubeletConfigCreate(t *testing.T) {
	for _, platform := range []string{"aws", "none", "unrecognized"} {
		t.Run(platform, func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig("test-cluster")
			cc.Spec.Platform = platform
			mcp := newMachineConfigPool("master", map[string]string{"kubeletType": "small-pods"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role", "master"), "v0")
			mcp2 := newMachineConfigPool("worker", map[string]string{"kubeletType": "large-pods"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role", "worker"), "v0")
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "kubeletType", "small-pods"))
			mcs := newMachineConfig(getManagedKey(mcp, kc1), map[string]string{"node-role": "master"}, "dummy://", []ignv2_2types.File{{}})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)

			f.expectGetMachineConfigAction(mcs)
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchKubeletConfig(kc1, []uint8{0x7b, 0x22, 0x6d, 0x65, 0x74, 0x61, 0x64, 0x61, 0x74, 0x61, 0x22, 0x3a, 0x7b, 0x22, 0x66, 0x69, 0x6e, 0x61, 0x6c, 0x69, 0x7a, 0x65, 0x72, 0x73, 0x22, 0x3a, 0x5b, 0x22, 0x39, 0x39, 0x2d, 0x6d, 0x61, 0x73, 0x74, 0x65, 0x72, 0x2d, 0x68, 0x35, 0x35, 0x32, 0x6d, 0x2d, 0x73, 0x6d, 0x61, 0x6c, 0x6c, 0x65, 0x72, 0x2d, 0x6d, 0x61, 0x78, 0x2d, 0x70, 0x6f, 0x64, 0x73, 0x2d, 0x6b, 0x75, 0x62, 0x65, 0x6c, 0x65, 0x74, 0x22, 0x5d, 0x7d, 0x7d})
			f.expectUpdateKubeletConfig(kc1)

			f.run(getKey(kc1, t))
		})
	}
}

func TestKubeletConfigBlacklistedOptions(t *testing.T) {
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

func getKey(config *mcfgv1.KubeletConfig, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(config)
	if err != nil {
		t.Errorf("Unexpected error getting key for config %v: %v", config.Name, err)
		return ""
	}
	return key
}
