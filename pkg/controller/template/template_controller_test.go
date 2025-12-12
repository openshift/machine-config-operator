package template

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/clarketm/json"
	configv1 "github.com/openshift/api/config/v1"
	fakeconfigv1client "github.com/openshift/client-go/config/clientset/versioned/fake"
	oseconfigfake "github.com/openshift/client-go/config/clientset/versioned/fake"

	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/diff"
	coreinformersv1 "k8s.io/client-go/informers"

	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client          *fake.Clientset
	kubeclient      *k8sfake.Clientset
	oseclient       *oseconfigfake.Clientset
	apiserverclient *oseconfigfake.Clientset

	ccLister []*mcfgv1.ControllerConfig
	mcLister []*mcfgv1.MachineConfig

	kubeactions []core.Action
	actions     []core.Action

	kubeobjects []runtime.Object
	objects     []runtime.Object
	oseobjects  []runtime.Object
	apiservers  []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.kubeobjects = []runtime.Object{}
	f.oseobjects = []runtime.Object{}
	return f
}

func newControllerConfig(name string) *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec: mcfgv1.ControllerConfigSpec{
			ClusterDNSIP: "10.3.0.1/16",
			Infra: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					EtcdDiscoveryDomain: fmt.Sprintf("%s.openshift.testing", name),
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.LibvirtPlatformType,
					},
					APIServerURL:         fmt.Sprintf("https://api.%s.tt.testing:6443", name),
					APIServerInternalURL: fmt.Sprintf("https://api-int.%s.tt.testing:6443", name),
				},
			},
			PullSecret: &corev1.ObjectReference{
				Namespace: "default",
				Name:      "coreos-pull-secret",
			},
		},
	}
}

func newPullSecret(name string, contents []byte) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: metav1.NamespaceDefault},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: contents},
	}
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)

	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)
	f.oseclient = oseconfigfake.NewSimpleClientset(f.oseobjects...)
	f.apiserverclient = fakeconfigv1client.NewSimpleClientset(f.apiservers...)

	cinformer := coreinformersv1.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())
	apiserverinformer := configinformers.NewSharedInformerFactory(f.apiserverclient, noResyncPeriodFunc())

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	c := New(templateDir,
		i.Machineconfiguration().V1().ControllerConfigs(), i.Machineconfiguration().V1().MachineConfigs(), cinformer.Core().V1().Secrets(),
		apiserverinformer.Config().V1().APIServers(), f.kubeclient, f.client)

	c.ccListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.apiserverListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	stopCh := make(chan struct{})
	defer close(stopCh)
	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)

	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}

	for _, m := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(m)
	}

	return c
}

func (f *fixture) run(ccname string) {
	f.runController(ccname, false)
}

func (f *fixture) runExpectError(ccname string) {
	f.runController(ccname, true)
}

func (f *fixture) runController(ccname string, expectError bool) {
	c := f.newController()

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

	k8sActions := filterInformerActions(f.kubeclient.Actions())
	for i, action := range k8sActions {
		if len(f.kubeactions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(k8sActions)-len(f.kubeactions), k8sActions[i:])
			break
		}

		expectedAction := f.kubeactions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.kubeactions) > len(k8sActions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.kubeactions)-len(k8sActions), f.kubeactions[len(k8sActions):])
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
		filterTimeFromControllerStatus(object, expObject)
		if !equality.Semantic.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.Diff(expObject, object))
		}
	case core.UpdateAction:
		e, _ := expected.(core.UpdateAction)
		expObject := e.GetObject()
		object := a.GetObject()

		filterTimeFromControllerStatus(object, expObject)
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

func filterTimeFromControllerStatus(objs ...runtime.Object) {
	for i, o := range objs {
		if _, ok := o.(*mcfgv1.ControllerConfig); ok {
			cfg := objs[i].(*mcfgv1.ControllerConfig)
			for j := range cfg.Status.Conditions {
				cfg.Status.Conditions[j].LastTransitionTime = metav1.Time{}
			}
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

func (f *fixture) expectGetSecretAction(secret *corev1.Secret) {
	f.kubeactions = append(f.kubeactions, core.NewGetAction(schema.GroupVersionResource{Resource: "secrets"}, secret.Namespace, secret.Name))
}

func (f *fixture) expectUpdateControllerConfigStatus(status *mcfgv1.ControllerConfig) {
	f.actions = append(f.actions, core.NewRootUpdateSubresourceAction(schema.GroupVersionResource{Resource: "controllerconfigs"}, "status", status))
}

func TestCreatesMachineConfigs(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig("test-cluster")
	ps := newPullSecret("coreos-pull-secret", []byte(`{"dummy": "dummy"}`))

	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)
	f.kubeobjects = append(f.kubeobjects, ps)

	expMCs, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy": "dummy"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	rcc := cc.DeepCopy()
	rcc.Status.ObservedGeneration = 1
	rcc.Status.Conditions = []mcfgv1.ControllerConfigStatusCondition{{Type: mcfgv1.TemplateControllerRunning, Status: corev1.ConditionTrue, Message: "syncing towards (1) generation using controller version v0.0.0-was-not-built-properly"}}
	f.expectUpdateControllerConfigStatus(rcc)
	f.expectGetSecretAction(ps)

	for idx := range expMCs {
		f.expectGetMachineConfigAction(expMCs[idx])
		f.expectCreateMachineConfigAction(expMCs[idx])
	}
	ccc := cc.DeepCopy()
	ccc.Status.ObservedGeneration = 1
	ccc.Status.Conditions = []mcfgv1.ControllerConfigStatusCondition{
		{Type: mcfgv1.TemplateControllerCompleted, Status: corev1.ConditionTrue, Message: "sync completed towards (1) generation using controller version v0.0.0-was-not-built-properly"},
		{Type: mcfgv1.TemplateControllerRunning, Status: corev1.ConditionFalse},
		{Type: mcfgv1.TemplateControllerFailing, Status: corev1.ConditionFalse},
	}
	f.expectUpdateControllerConfigStatus(ccc)

	f.run(getKey(cc, t))
}

func TestDoNothing(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig("test-cluster")
	ps := newPullSecret("coreos-pull-secret", []byte(`{"dummy": "dummy"}`))

	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy": "dummy"}`), nil)
	if err != nil {
		t.Fatal(err)
	}

	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)
	f.kubeobjects = append(f.kubeobjects, ps)
	f.mcLister = append(f.mcLister, mcs...)
	for idx := range mcs {
		f.objects = append(f.objects, mcs[idx])
	}

	rcc := cc.DeepCopy()
	rcc.Status.ObservedGeneration = 1
	rcc.Status.Conditions = []mcfgv1.ControllerConfigStatusCondition{{Type: mcfgv1.TemplateControllerRunning, Status: corev1.ConditionTrue, Message: "syncing towards (1) generation using controller version v0.0.0-was-not-built-properly"}}
	f.expectUpdateControllerConfigStatus(rcc)
	f.expectGetSecretAction(ps)
	for idx := range mcs {
		f.expectGetMachineConfigAction(mcs[idx])
	}
	ccc := cc.DeepCopy()
	ccc.Status.ObservedGeneration = 1
	ccc.Status.Conditions = []mcfgv1.ControllerConfigStatusCondition{
		{Type: mcfgv1.TemplateControllerCompleted, Status: corev1.ConditionTrue, Message: "sync completed towards (1) generation using controller version v0.0.0-was-not-built-properly"},
		{Type: mcfgv1.TemplateControllerRunning, Status: corev1.ConditionFalse},
		{Type: mcfgv1.TemplateControllerFailing, Status: corev1.ConditionFalse},
	}
	f.expectUpdateControllerConfigStatus(ccc)

	f.run(getKey(cc, t))
}

func TestRecreateMachineConfig(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig("test-cluster")
	ps := newPullSecret("coreos-pull-secret", []byte(`{"dummy": "dummy"}`))

	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy": "dummy"}`), nil)
	if err != nil {
		t.Fatal(err)
	}

	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)
	f.kubeobjects = append(f.kubeobjects, ps)
	for idx := 0; idx < len(mcs)-1; idx++ {
		f.mcLister = append(f.mcLister, mcs[idx])
		f.objects = append(f.objects, mcs[idx])
	}

	rcc := cc.DeepCopy()
	rcc.Status.ObservedGeneration = 1
	rcc.Status.Conditions = []mcfgv1.ControllerConfigStatusCondition{{Type: mcfgv1.TemplateControllerRunning, Status: corev1.ConditionTrue, Message: "syncing towards (1) generation using controller version v0.0.0-was-not-built-properly"}}
	f.expectUpdateControllerConfigStatus(rcc)
	f.expectGetSecretAction(ps)

	for idx := range mcs {
		f.expectGetMachineConfigAction(mcs[idx])
	}
	f.expectCreateMachineConfigAction(mcs[len(mcs)-1])
	ccc := cc.DeepCopy()
	ccc.Status.ObservedGeneration = 1
	ccc.Status.Conditions = []mcfgv1.ControllerConfigStatusCondition{
		{Type: mcfgv1.TemplateControllerCompleted, Status: corev1.ConditionTrue, Message: "sync completed towards (1) generation using controller version v0.0.0-was-not-built-properly"},
		{Type: mcfgv1.TemplateControllerRunning, Status: corev1.ConditionFalse},
		{Type: mcfgv1.TemplateControllerFailing, Status: corev1.ConditionFalse},
	}
	f.expectUpdateControllerConfigStatus(ccc)
	f.run(getKey(cc, t))
}

func TestUpdateMachineConfig(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig("test-cluster")
	ps := newPullSecret("coreos-pull-secret", []byte(`{"dummy": "dummy"}`))

	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy": "dummy"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	//update machineconfig
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newRawIgnCfg, err := json.Marshal(newIgnCfg)
	if err != nil {
		t.Fatal(err)
	}
	mcs[len(mcs)-1].Spec.Config.Raw = newRawIgnCfg

	f.ccLister = append(f.ccLister, cc)
	f.kubeobjects = append(f.kubeobjects, ps)
	f.objects = append(f.objects, cc)
	for idx := range mcs {
		f.mcLister = append(f.mcLister, mcs[idx])
		f.objects = append(f.objects, mcs[idx])
	}

	expmcs, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy": "dummy"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	rcc := cc.DeepCopy()
	rcc.Status.ObservedGeneration = 1
	rcc.Status.Conditions = []mcfgv1.ControllerConfigStatusCondition{{Type: mcfgv1.TemplateControllerRunning, Status: corev1.ConditionTrue, Message: "syncing towards (1) generation using controller version v0.0.0-was-not-built-properly"}}
	f.expectUpdateControllerConfigStatus(rcc)
	f.expectGetSecretAction(ps)
	for idx := range expmcs {
		f.expectGetMachineConfigAction(expmcs[idx])
	}
	f.expectUpdateMachineConfigAction(expmcs[len(expmcs)-1])
	ccc := cc.DeepCopy()
	ccc.Status.ObservedGeneration = 1
	ccc.Status.Conditions = []mcfgv1.ControllerConfigStatusCondition{
		{Type: mcfgv1.TemplateControllerCompleted, Status: corev1.ConditionTrue, Message: "sync completed towards (1) generation using controller version v0.0.0-was-not-built-properly"},
		{Type: mcfgv1.TemplateControllerRunning, Status: corev1.ConditionFalse},
		{Type: mcfgv1.TemplateControllerFailing, Status: corev1.ConditionFalse},
	}
	f.expectUpdateControllerConfigStatus(ccc)
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

func TestKubeletAutoNodeSizingEnabled(t *testing.T) {
	cc := newControllerConfig("test-cluster")
	ps := []byte(`{"dummy": "dummy"}`)

	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc, ps, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Find machine configs that should contain the auto-node-sizing file
	// The file should be in all role-based machine configs (master, worker)
	autoSizingFileFound := false
	for _, mc := range mcs {
		ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
		if err != nil {
			t.Fatalf("Failed to parse ignition config for %s: %v", mc.Name, err)
		}

		// Look for the auto-node-sizing file
		for _, file := range ignCfg.Storage.Files {
			if file.Path == ctrlcommon.NodeSizingEnabledEnvPath {
				autoSizingFileFound = true

				// Decode the file contents
				contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
				if err != nil {
					t.Fatalf("Failed to decode auto-node-sizing file contents: %v", err)
				}

				contentsStr := string(contents)

				// Verify NODE_SIZING_ENABLED based on node role
				// Master nodes should have NODE_SIZING_ENABLED=false
				// Other nodes (worker, etc.) should have NODE_SIZING_ENABLED=true
				isMasterNode := strings.Contains(mc.Name, "master")
				if isMasterNode {
					if !strings.Contains(contentsStr, "NODE_SIZING_ENABLED=false") {
						t.Errorf("Expected NODE_SIZING_ENABLED=false in %s, got: %s", mc.Name, contentsStr)
					}
				} else {
					if !strings.Contains(contentsStr, "NODE_SIZING_ENABLED=true") {
						t.Errorf("Expected NODE_SIZING_ENABLED=true in %s, got: %s", mc.Name, contentsStr)
					}
				}

				// Verify other expected values
				if !strings.Contains(contentsStr, "SYSTEM_RESERVED_MEMORY=1Gi") {
					t.Errorf("Expected SYSTEM_RESERVED_MEMORY=1Gi in %s, got: %s", mc.Name, contentsStr)
				}

				if !strings.Contains(contentsStr, "SYSTEM_RESERVED_CPU=500m") {
					t.Errorf("Expected SYSTEM_RESERVED_CPU=500m in %s, got: %s", mc.Name, contentsStr)
				}

				if !strings.Contains(contentsStr, "SYSTEM_RESERVED_ES=1Gi") {
					t.Errorf("Expected SYSTEM_RESERVED_ES=1Gi in %s, got: %s", mc.Name, contentsStr)
				}
			}
		}
	}

	if !autoSizingFileFound {
		t.Errorf("Expected to find %s file in at least one machine config", ctrlcommon.NodeSizingEnabledEnvPath)
	}
}

func TestKubeletAutoNodeSizingDisabledForHypershift(t *testing.T) {
	cc := newControllerConfig("test-cluster")
	// Set ControlPlaneTopology to External to simulate Hypershift
	cc.Spec.Infra.Status.ControlPlaneTopology = configv1.ExternalTopologyMode
	ps := []byte(`{"dummy": "dummy"}`)

	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc, ps, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Find machine configs that should contain the auto-node-sizing file
	autoSizingFileFound := false
	for _, mc := range mcs {
		ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
		if err != nil {
			t.Fatalf("Failed to parse ignition config for %s: %v", mc.Name, err)
		}

		// Look for the auto-node-sizing file
		for _, file := range ignCfg.Storage.Files {
			if file.Path == ctrlcommon.NodeSizingEnabledEnvPath {
				autoSizingFileFound = true

				// Decode the file contents
				contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
				if err != nil {
					t.Fatalf("Failed to decode auto-node-sizing file contents: %v", err)
				}

				contentsStr := string(contents)

				// Verify NODE_SIZING_ENABLED=false is present for Hypershift
				if !strings.Contains(contentsStr, "NODE_SIZING_ENABLED=false") {
					t.Errorf("Expected NODE_SIZING_ENABLED=false for Hypershift in %s, got: %s", mc.Name, contentsStr)
				}

				// Verify other expected values are still present
				if !strings.Contains(contentsStr, "SYSTEM_RESERVED_MEMORY=1Gi") {
					t.Errorf("Expected SYSTEM_RESERVED_MEMORY=1Gi in %s, got: %s", mc.Name, contentsStr)
				}

				if !strings.Contains(contentsStr, "SYSTEM_RESERVED_CPU=500m") {
					t.Errorf("Expected SYSTEM_RESERVED_CPU=500m in %s, got: %s", mc.Name, contentsStr)
				}

				if !strings.Contains(contentsStr, "SYSTEM_RESERVED_ES=1Gi") {
					t.Errorf("Expected SYSTEM_RESERVED_ES=1Gi in %s, got: %s", mc.Name, contentsStr)
				}
			}
		}
	}

	if !autoSizingFileFound {
		t.Errorf("Expected to find %s file in at least one machine config", ctrlcommon.NodeSizingEnabledEnvPath)
	}
}
