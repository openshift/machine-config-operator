package containerruntimeconfig

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/golang/glog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vincent-petithory/dataurl"

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

	ign3types "github.com/coreos/ignition/v2/config/v3_1/types"
	apicfgv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	fakeconfigv1client "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1informer "github.com/openshift/client-go/config/informers/externalversions"
	fakeoperatorclient "github.com/openshift/client-go/operator/clientset/versioned/fake"
	operatorinformer "github.com/openshift/client-go/operator/informers/externalversions"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/machine-config-operator/test/helpers"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
	// This matches templates/*/container-registries.yaml, and will have to be updated with those files.  Ideally
	// it would be nice to extract this dynamically.
	templateRegistriesConfig = []byte("unqualified-search-registries = ['registry.access.redhat.com', 'docker.io']\n")
	templatePolicyJSON       = []byte("{\"default\":[{\"type\":\"reject\"}],\"transports\":{\"docker-daemon\":{\"\":[{\"type\":\"insecureAcceptAnything\"}]}}}\n")
)

const (
	templateDir = "../../../templates"
)

type fixture struct {
	t *testing.T

	client         *fake.Clientset
	imgClient      *fakeconfigv1client.Clientset
	operatorClient *fakeoperatorclient.Clientset

	ccLister   []*mcfgv1.ControllerConfig
	mcpLister  []*mcfgv1.MachineConfigPool
	mccrLister []*mcfgv1.ContainerRuntimeConfig
	imgLister  []*apicfgv1.Image
	cvLister   []*apicfgv1.ClusterVersion
	icspLister []*apioperatorsv1alpha1.ImageContentSourcePolicy

	actions []core.Action

	objects         []runtime.Object
	imgObjects      []runtime.Object
	operatorObjects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	return f
}

func (f *fixture) validateActions() {
	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		glog.Infof("Action: %v", action)

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

func newControllerConfig(name string, platform apicfgv1.PlatformType) *mcfgv1.ControllerConfig {
	cc := &mcfgv1.ControllerConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5))},
		Spec: mcfgv1.ControllerConfigSpec{
			Infra: &apicfgv1.Infrastructure{
				Status: apicfgv1.InfrastructureStatus{
					EtcdDiscoveryDomain: fmt.Sprintf("%s.tt.testing", name),
					PlatformStatus: &apicfgv1.PlatformStatus{
						Type: platform,
					},
				},
			},
			ReleaseImage: "release-reg.io/myuser/myimage:test",
		},
	}
	return cc
}

func newContainerRuntimeConfig(name string, ctrconf *mcfgv1.ContainerRuntimeConfiguration, selector *metav1.LabelSelector) *mcfgv1.ContainerRuntimeConfig {
	return &mcfgv1.ContainerRuntimeConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: mcfgv1.ContainerRuntimeConfigSpec{
			ContainerRuntimeConfig:    ctrconf,
			MachineConfigPoolSelector: selector,
		},
		Status: mcfgv1.ContainerRuntimeConfigStatus{},
	}
}

func newImageConfig(name string, regconf *apicfgv1.RegistrySources) *apicfgv1.Image {
	return &apicfgv1.Image{
		TypeMeta:   metav1.TypeMeta{APIVersion: apicfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: apicfgv1.ImageSpec{
			RegistrySources: *regconf,
		},
	}
}

func newICSP(name string, mirrors []apioperatorsv1alpha1.RepositoryDigestMirrors) *apioperatorsv1alpha1.ImageContentSourcePolicy {
	return &apioperatorsv1alpha1.ImageContentSourcePolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: apioperatorsv1alpha1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
			RepositoryDigestMirrors: mirrors,
		},
	}
}

func newClusterVersionConfig(name, desiredImage string) *apicfgv1.ClusterVersion {
	return &apicfgv1.ClusterVersion{
		TypeMeta:   metav1.TypeMeta{APIVersion: apicfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Status: apicfgv1.ClusterVersionStatus{
			Desired: apicfgv1.Release{
				Image: desiredImage,
			},
		},
	}
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.imgClient = fakeconfigv1client.NewSimpleClientset(f.imgObjects...)
	f.operatorClient = fakeoperatorclient.NewSimpleClientset(f.operatorObjects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	ci := configv1informer.NewSharedInformerFactory(f.imgClient, noResyncPeriodFunc())
	oi := operatorinformer.NewSharedInformerFactory(f.operatorClient, noResyncPeriodFunc())
	c := New(templateDir,
		i.Machineconfiguration().V1().MachineConfigPools(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().ContainerRuntimeConfigs(),
		ci.Config().V1().Images(),
		oi.Operator().V1alpha1().ImageContentSourcePolicies(),
		ci.Config().V1().ClusterVersions(),
		k8sfake.NewSimpleClientset(), f.client, f.imgClient)

	c.mcpListerSynced = alwaysReady
	c.mccrListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.imgListerSynced = alwaysReady
	c.icspListerSynced = alwaysReady
	c.clusterVersionListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	stopCh := make(chan struct{})
	defer close(stopCh)
	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)
	ci.Start(stopCh)
	ci.WaitForCacheSync(stopCh)
	oi.Start(stopCh)
	oi.WaitForCacheSync(stopCh)

	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mcpLister {
		i.Machineconfiguration().V1().MachineConfigPools().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mccrLister {
		i.Machineconfiguration().V1().ContainerRuntimeConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.imgLister {
		ci.Config().V1().Images().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.cvLister {
		ci.Config().V1().ClusterVersions().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.icspLister {
		oi.Operator().V1alpha1().ImageContentSourcePolicies().Informer().GetIndexer().Add(c)
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

	err := c.syncImgHandler(mcpname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing image config: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing image config, got nil")
	}

	err = c.syncHandler(mcpname)
	if !expectError && err != nil {
		f.t.Errorf("error syncing containerruntimeconfigs: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing containerruntimeconfigs, got nil")
	}

	f.validateActions()
}

// filterInformerActions filters list and watch actions for testing resources.
// Since list and watch don't change resource state we can filter it to lower
// noise level in our tests.
func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "machineconfigpools") ||
				action.Matches("watch", "machineconfigpools") ||
				action.Matches("list", "controllerconfigs") ||
				action.Matches("watch", "controllerconfigs") ||
				action.Matches("list", "containerruntimeconfigs") ||
				action.Matches("watch", "containerruntimeconfigs") ||
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

func (f *fixture) expectGetContainerRuntimeConfigAction(config *mcfgv1.ContainerRuntimeConfig) {
	f.actions = append(f.actions, core.NewRootGetAction(schema.GroupVersionResource{Resource: "containerruntimeconfigs"}, config.Name))
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

func (f *fixture) expectPatchContainerRuntimeConfig(config *mcfgv1.ContainerRuntimeConfig, patch []byte) {
	f.actions = append(f.actions, core.NewRootPatchAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "containerruntimeconfigs"}, config.Name, types.MergePatchType, patch))
}

func (f *fixture) expectUpdateContainerRuntimeConfig(config *mcfgv1.ContainerRuntimeConfig) {
	f.actions = append(f.actions, core.NewRootUpdateSubresourceAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "containerruntimeconfigs"}, "status", config))
}

func (f *fixture) verifyRegistriesConfigAndPolicyJSONContents(t *testing.T, mcName string, imgcfg *apicfgv1.Image, icsp *apioperatorsv1alpha1.ImageContentSourcePolicy, releaseImageReg string, verifyPolicyJSON, verifySearchRegsDropin bool) {
	icsps := []*apioperatorsv1alpha1.ImageContentSourcePolicy{}
	if icsp != nil {
		icsps = append(icsps, icsp)
	}
	updatedMC, err := f.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), mcName, metav1.GetOptions{})
	require.NoError(t, err)
	verifyRegistriesConfigAndPolicyJSONContents(t, updatedMC, mcName, imgcfg, icsps, releaseImageReg, verifyPolicyJSON, verifySearchRegsDropin)
}

func verifyRegistriesConfigAndPolicyJSONContents(t *testing.T, mc *mcfgv1.MachineConfig, mcName string, imgcfg *apicfgv1.Image, icsps []*apioperatorsv1alpha1.ImageContentSourcePolicy, releaseImageReg string, verifyPolicyJSON, verifySearchRegsDropin bool) {
	// This is not testing updateRegistriesConfig, which has its own tests; this verifies the created object contains the expected
	// configuration file.
	// First get the valid blocked registries to ensure we don't block the registry where the release image is from
	blockedRegistries, _ := getValidBlockedRegistries(releaseImageReg, &imgcfg.Spec)
	expectedRegistriesConf, err := updateRegistriesConfig(templateRegistriesConfig,
		imgcfg.Spec.RegistrySources.InsecureRegistries,
		blockedRegistries, icsps)
	require.NoError(t, err)
	assert.Equal(t, mcName, mc.ObjectMeta.Name)

	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	require.NoError(t, err)
	if verifyPolicyJSON && verifySearchRegsDropin {
		// If there is a change to the policy.json AND drop-in search registries file then there will be 3 files
		require.Len(t, ignCfg.Storage.Files, 3)
	} else if verifyPolicyJSON || verifySearchRegsDropin {
		// If there is a change to the policy.json file OR the drop-in search registries file then there will be 2 files
		require.Len(t, ignCfg.Storage.Files, 2)
	} else {
		require.Len(t, ignCfg.Storage.Files, 1)
	}
	regfile := ignCfg.Storage.Files[0]
	if regfile.Node.Path != registriesConfigPath {
		regfile = ignCfg.Storage.Files[1]
	}
	assert.Equal(t, registriesConfigPath, regfile.Node.Path)
	registriesConf, err := dataurl.DecodeString(*regfile.Contents.Source)
	require.NoError(t, err)
	assert.Equal(t, string(expectedRegistriesConf), string(registriesConf.Data))

	// Validate the policy.json contents if a change is expected from the tests
	if verifyPolicyJSON {
		expectedPolicyJSON, err := updatePolicyJSON(templatePolicyJSON,
			blockedRegistries,
			imgcfg.Spec.RegistrySources.AllowedRegistries)
		require.NoError(t, err)
		policyfile := ignCfg.Storage.Files[1]
		if policyfile.Node.Path != policyConfigPath {
			policyfile = ignCfg.Storage.Files[0]
		}
		assert.Equal(t, policyConfigPath, policyfile.Node.Path)
		policyJSON, err := dataurl.DecodeString(*policyfile.Contents.Source)
		require.NoError(t, err)
		assert.Equal(t, string(expectedPolicyJSON), string(policyJSON.Data))
	}

	// Validate the drop-in search registries file contents if a change is expected from the tests
	if verifySearchRegsDropin {
		expectedSearchRegsConf := updateSearchRegistriesConfig(imgcfg.Spec.RegistrySources.ContainerRuntimeSearchRegistries)
		dropinfile := ignCfg.Storage.Files[2]
		if dropinfile.Node.Path != searchRegDropInFilePath {
			dropinfile = ignCfg.Storage.Files[1]
		}
		assert.Equal(t, searchRegDropInFilePath, dropinfile.Node.Path)
		searchRegsConf, err := dataurl.DecodeString(*dropinfile.Contents.Source)
		require.NoError(t, err)
		assert.Equal(t, string(expectedSearchRegsConf[0].data), string(searchRegsConf.Data))
	}
}

// The patch bytes to expect when creating/updating a containerruntimeconfig
var ctrcfgPatchBytes = []uint8{0x7b, 0x22, 0x6d, 0x65, 0x74, 0x61, 0x64, 0x61, 0x74, 0x61, 0x22, 0x3a, 0x7b, 0x22, 0x66, 0x69, 0x6e, 0x61, 0x6c, 0x69, 0x7a, 0x65, 0x72, 0x73, 0x22, 0x3a, 0x5b, 0x22, 0x39, 0x39, 0x2d, 0x6d, 0x61, 0x73, 0x74, 0x65, 0x72, 0x2d, 0x73, 0x78, 0x32, 0x76, 0x72, 0x2d, 0x63, 0x6f, 0x6e, 0x74, 0x61, 0x69, 0x6e, 0x65, 0x72, 0x72, 0x75, 0x6e, 0x74, 0x69, 0x6d, 0x65, 0x22, 0x5d, 0x7d, 0x7d}

// TestContainerRuntimeConfigCreate ensures that a create happens when an existing containerruntime config is created.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestContainerRuntimeConfigCreate(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			ctrcfg1 := newContainerRuntimeConfig("set-log-level", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug", LogSizeMax: resource.MustParse("9k"), OverlaySize: resource.MustParse("3G")}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			ctrCfgKey, _ := getManagedKeyCtrCfg(mcp, nil)
			mcs1 := helpers.NewMachineConfig(getManagedKeyCtrCfgDeprecated(mcp), map[string]string{"node-role": "master"}, "dummy://", []ign3types.File{{}})
			mcs2 := mcs1.DeepCopy()
			mcs2.Name = ctrCfgKey

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mccrLister = append(f.mccrLister, ctrcfg1)
			f.objects = append(f.objects, ctrcfg1)

			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs1)
			f.expectGetMachineConfigAction(mcs1)
			f.expectUpdateContainerRuntimeConfig(ctrcfg1)
			f.expectCreateMachineConfigAction(mcs1)
			f.expectPatchContainerRuntimeConfig(ctrcfg1, ctrcfgPatchBytes)
			f.expectUpdateContainerRuntimeConfig(ctrcfg1)

			f.run(getKey(ctrcfg1, t))
		})
	}
}

// TestContainerRuntimeConfigUpdate ensures that an update happens when an existing containerruntime config is updated.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestContainerRuntimeConfigUpdate(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			ctrcfg1 := newContainerRuntimeConfig("set-log-level", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug", LogSizeMax: resource.MustParse("9k"), OverlaySize: resource.MustParse("3G")}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			keyCtrCfg, _ := getManagedKeyCtrCfg(mcp, nil)
			mcs := helpers.NewMachineConfig(getManagedKeyCtrCfgDeprecated(mcp), map[string]string{"node-role": "master"}, "dummy://", []ign3types.File{{}})
			mcsUpdate := mcs.DeepCopy()
			mcsUpdate.Name = keyCtrCfg

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mccrLister = append(f.mccrLister, ctrcfg1)
			f.objects = append(f.objects, ctrcfg1)

			f.expectGetMachineConfigAction(mcsUpdate)
			f.expectGetMachineConfigAction(mcs)
			f.expectGetMachineConfigAction(mcs)
			f.expectUpdateContainerRuntimeConfig(ctrcfg1)
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchContainerRuntimeConfig(ctrcfg1, ctrcfgPatchBytes)
			f.expectUpdateContainerRuntimeConfig(ctrcfg1)

			c := f.newController()
			stopCh := make(chan struct{})

			err := c.syncHandler(getKey(ctrcfg1, t))
			if err != nil {
				t.Errorf("syncHandler returned %v", err)
			}

			f.validateActions()
			close(stopCh)

			// Perform Update
			f = newFixture(t)

			// Modify config
			ctrcfgUpdate := ctrcfg1.DeepCopy()
			ctrcfgUpdate.Spec.ContainerRuntimeConfig.LogLevel = "warn"

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mccrLister = append(f.mccrLister, ctrcfgUpdate)
			f.objects = append(f.objects, mcsUpdate, ctrcfgUpdate)

			c = f.newController()
			stopCh = make(chan struct{})

			glog.Info("Applying update")

			// Apply update
			err = c.syncHandler(getKey(ctrcfgUpdate, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			f.expectGetMachineConfigAction(mcsUpdate)
			f.expectGetMachineConfigAction(mcsUpdate)
			f.expectUpdateContainerRuntimeConfig(ctrcfgUpdate)
			f.expectUpdateMachineConfigAction(mcsUpdate)
			f.expectPatchContainerRuntimeConfig(ctrcfgUpdate, ctrcfgPatchBytes)
			f.expectUpdateContainerRuntimeConfig(ctrcfgUpdate)

			f.validateActions()

			close(stopCh)
		})
	}
}

// TestImageConfigCreate ensures that a create happens when an image config is created.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestImageConfigCreate(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			imgcfg1 := newImageConfig("cluster", &apicfgv1.RegistrySources{InsecureRegistries: []string{"blah.io"}, AllowedRegistries: []string{"allow.io"}, ContainerRuntimeSearchRegistries: []string{"search-reg.io"}})
			cvcfg1 := newClusterVersionConfig("version", "test.io/myuser/myimage:test")
			keyReg1, _ := getManagedKeyReg(mcp, nil)
			keyReg2, _ := getManagedKeyReg(mcp2, nil)
			mcs1 := helpers.NewMachineConfig(keyReg1, map[string]string{"node-role": "master"}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(keyReg2, map[string]string{"node-role": "worker"}, "dummy://", []ign3types.File{{}})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.imgObjects = append(f.imgObjects, imgcfg1)

			f.expectGetMachineConfigAction(mcs1)
			f.expectGetMachineConfigAction(mcs1)
			f.expectGetMachineConfigAction(mcs1)
			f.expectCreateMachineConfigAction(mcs1)
			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2)
			f.expectCreateMachineConfigAction(mcs2)

			f.run("cluster")

			for _, mcName := range []string{mcs1.Name, mcs2.Name} {
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, cc.Spec.ReleaseImage, true, true)
			}
		})
	}
}

// TestImageConfigUpdate ensures that an update happens when an existing image config is updated.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestImageConfigUpdate(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			imgcfg1 := newImageConfig("cluster", &apicfgv1.RegistrySources{InsecureRegistries: []string{"blah.io"}, AllowedRegistries: []string{"allow.io"}, ContainerRuntimeSearchRegistries: []string{"search-reg.io"}})
			cvcfg1 := newClusterVersionConfig("version", "test.io/myuser/myimage:test")
			keyReg1, _ := getManagedKeyReg(mcp, nil)
			keyReg2, _ := getManagedKeyReg(mcp2, nil)
			mcs1 := helpers.NewMachineConfig(getManagedKeyRegDeprecated(mcp), map[string]string{"node-role": "master"}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(getManagedKeyRegDeprecated(mcp2), map[string]string{"node-role": "worker"}, "dummy://", []ign3types.File{{}})
			mcs1Update := mcs1.DeepCopy()
			mcs2Update := mcs2.DeepCopy()
			mcs1Update.Name = keyReg1
			mcs2Update.Name = keyReg2

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.imgObjects = append(f.imgObjects, imgcfg1)

			f.expectGetMachineConfigAction(mcs1Update)
			f.expectGetMachineConfigAction(mcs1)
			f.expectGetMachineConfigAction(mcs1)
			f.expectCreateMachineConfigAction(mcs1)
			f.expectGetMachineConfigAction(mcs2Update)
			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2)
			f.expectCreateMachineConfigAction(mcs2)

			c := f.newController()
			stopCh := make(chan struct{})

			err := c.syncImgHandler("cluster")
			if err != nil {
				t.Errorf("syncImgHandler returned %v", err)
			}

			f.validateActions()
			close(stopCh)

			for _, mcName := range []string{mcs1Update.Name, mcs2Update.Name} {
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, cc.Spec.ReleaseImage, true, true)
			}

			// Perform Update
			f = newFixture(t)

			// Modify image config
			imgcfgUpdate := imgcfg1.DeepCopy()
			imgcfgUpdate.Spec.RegistrySources.InsecureRegistries = []string{"test.io"}

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfgUpdate)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.imgObjects = append(f.imgObjects, imgcfgUpdate)
			f.objects = append(f.objects, mcs1Update, mcs2Update)

			c = f.newController()
			stopCh = make(chan struct{})

			glog.Info("Applying update")

			// Apply update
			err = c.syncImgHandler("")
			if err != nil {
				t.Errorf("syncImgHandler returned: %v", err)
			}

			f.expectGetMachineConfigAction(mcs1Update)
			f.expectGetMachineConfigAction(mcs1Update)
			f.expectUpdateMachineConfigAction(mcs1Update)
			f.expectGetMachineConfigAction(mcs2Update)
			f.expectGetMachineConfigAction(mcs2Update)
			f.expectUpdateMachineConfigAction(mcs2Update)

			f.validateActions()

			close(stopCh)

			for _, mcName := range []string{mcs1Update.Name, mcs2Update.Name} {
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfgUpdate, nil, cc.Spec.ReleaseImage, true, true)
			}
		})
	}
}

// TestICSPUpdate ensures that an update happens when an existing ICSP is updated.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestICSPUpdate(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			imgcfg1 := newImageConfig("cluster", &apicfgv1.RegistrySources{InsecureRegistries: []string{"blah.io"}})
			cvcfg1 := newClusterVersionConfig("version", "test.io/myuser/myimage:test")
			keyReg1, _ := getManagedKeyReg(mcp, nil)
			keyReg2, _ := getManagedKeyReg(mcp2, nil)
			mcs1 := helpers.NewMachineConfig(getManagedKeyRegDeprecated(mcp), map[string]string{"node-role": "master"}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(getManagedKeyRegDeprecated(mcp2), map[string]string{"node-role": "worker"}, "dummy://", []ign3types.File{{}})
			icsp := newICSP("built-in", []apioperatorsv1alpha1.RepositoryDigestMirrors{
				{Source: "built-in-source.example.com", Mirrors: []string{"built-in-mirror.example.com"}},
			})
			mcs1Update := mcs1.DeepCopy()
			mcs2Update := mcs2.DeepCopy()
			mcs1Update.Name = keyReg1
			mcs2Update.Name = keyReg2

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.icspLister = append(f.icspLister, icsp)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.imgObjects = append(f.imgObjects, imgcfg1)
			f.operatorObjects = append(f.operatorObjects, icsp)

			f.expectGetMachineConfigAction(mcs1Update)
			f.expectGetMachineConfigAction(mcs1)
			f.expectGetMachineConfigAction(mcs1)
			f.expectCreateMachineConfigAction(mcs1)
			f.expectGetMachineConfigAction(mcs2Update)
			f.expectGetMachineConfigAction(mcs2)
			f.expectGetMachineConfigAction(mcs2)
			f.expectCreateMachineConfigAction(mcs2)

			c := f.newController()
			stopCh := make(chan struct{})

			err := c.syncImgHandler("cluster")
			if err != nil {
				t.Errorf("syncImgHandler returned %v", err)
			}

			f.validateActions()
			close(stopCh)

			for _, mcName := range []string{mcs1Update.Name, mcs2Update.Name} {
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, icsp, cc.Spec.ReleaseImage, false, false)
			}

			// Perform Update
			f = newFixture(t)

			// Modify ICSP
			icspUpdate := icsp.DeepCopy()
			icspUpdate.Spec.RepositoryDigestMirrors = append(icspUpdate.Spec.RepositoryDigestMirrors, apioperatorsv1alpha1.RepositoryDigestMirrors{
				Source: "built-in-source.example.com", Mirrors: []string{"local-mirror.local"},
			})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.icspLister = append(f.icspLister, icspUpdate)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.objects = append(f.objects, mcs1Update, mcs2Update)
			f.imgObjects = append(f.imgObjects, imgcfg1)
			f.operatorObjects = append(f.operatorObjects, icspUpdate)

			c = f.newController()
			stopCh = make(chan struct{})

			glog.Info("Applying update")

			// Apply update
			err = c.syncImgHandler("")
			if err != nil {
				t.Errorf("syncImgHandler returned: %v", err)
			}

			f.expectGetMachineConfigAction(mcs1Update)
			f.expectGetMachineConfigAction(mcs1Update)
			f.expectUpdateMachineConfigAction(mcs1Update)
			f.expectGetMachineConfigAction(mcs2Update)
			f.expectGetMachineConfigAction(mcs2Update)
			f.expectUpdateMachineConfigAction(mcs2Update)

			f.validateActions()

			close(stopCh)

			for _, mcName := range []string{mcs1Update.Name, mcs2Update.Name} {
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, icspUpdate, cc.Spec.ReleaseImage, false, false)
			}
		})
	}
}

func TestRunImageBootstrap(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			pools := []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
				helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
			}
			icspRules := []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				newICSP("built-in", []apioperatorsv1alpha1.RepositoryDigestMirrors{
					{Source: "built-in-source.example.com", Mirrors: []string{"built-in-mirror.example.com"}},
					{Source: "built-in-source.example.com", Mirrors: []string{"local-mirror.local"}},
				}),
			}
			// Adding the release-image registry "release-reg.io" to the list of blocked registries to ensure that is it not added to
			// both registries.conf and policy.json as blocked
			imgCfg := newImageConfig("cluster", &apicfgv1.RegistrySources{InsecureRegistries: []string{"insecure-reg-1.io", "insecure-reg-2.io"}, BlockedRegistries: []string{"blocked-reg.io", "release-reg.io"}, ContainerRuntimeSearchRegistries: []string{"search-reg.io"}})

			mcs, err := RunImageBootstrap("../../../templates", cc, pools, icspRules, imgCfg)
			require.NoError(t, err)
			require.Len(t, mcs, len(pools))

			for i := range pools {
				keyReg, _ := getManagedKeyReg(pools[i], nil)
				verifyRegistriesConfigAndPolicyJSONContents(t, mcs[i], keyReg, imgCfg, icspRules, cc.Spec.ReleaseImage, true, true)
			}
		})
	}
}

// TestRegistriesValidation tests the validity of registries allowed to be listed
// under blocked registries
func TestRegistriesValidation(t *testing.T) {
	failureTests := []struct {
		name   string
		config *apicfgv1.RegistrySources
	}{
		{
			name: "adding registry used by payload to blocked registries",
			config: &apicfgv1.RegistrySources{
				BlockedRegistries:  []string{"blah.io", "docker.io"},
				InsecureRegistries: []string{"test.io"},
			},
		},
	}

	successTests := []struct {
		name   string
		config *apicfgv1.RegistrySources
	}{
		{
			name: "adding registry used by payload to blocked registries",
			config: &apicfgv1.RegistrySources{
				BlockedRegistries:  []string{"docker.io"},
				InsecureRegistries: []string{"blah.io"},
			},
		},
	}

	// Failure Tests
	for _, test := range failureTests {
		imgcfg := newImageConfig(test.name, test.config)
		cvcfg := newClusterVersionConfig("version", "blah.io/myuser/myimage:test")
		blocked, err := getValidBlockedRegistries(cvcfg.Status.Desired.Image, &imgcfg.Spec)
		if err == nil {
			t.Errorf("%s: failed", test.name)
		}
		for _, reg := range blocked {
			if reg == "blah.io" {
				t.Errorf("%s: failed to filter out registry being used by payload", test.name)
			}
		}
	}

	// Successful Tests
	for _, test := range successTests {
		imgcfg := newImageConfig(test.name, test.config)
		cvcfg := newClusterVersionConfig("version", "blah.io/myuser/myimage:test")
		blocked, err := getValidBlockedRegistries(cvcfg.Status.Desired.Image, &imgcfg.Spec)
		if err != nil {
			t.Errorf("%s: failed", test.name)
		}
		for _, reg := range blocked {
			if reg == "blah.io" {
				t.Errorf("%s: failed to filter out registry being used by payload", test.name)
			}
		}
	}
}

// TestContainerRuntimeConfigOptions tests the validity of allowed and not allowed values
// for the options in containerruntime config
func TestContainerRuntimeConfigOptions(t *testing.T) {
	failureTests := []struct {
		name   string
		config *mcfgv1.ContainerRuntimeConfiguration
	}{
		{
			name: "invalid value of pids limit",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				PidsLimit: 10,
			},
		},
		{
			name: "inalid value of max log size",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogSizeMax: resource.MustParse("3k"),
			},
		},
		{
			name: "inalid value of log level",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogLevel: "invalid",
			},
		},
	}

	successTests := []struct {
		name   string
		config *mcfgv1.ContainerRuntimeConfiguration
	}{
		{
			name: "valid pids limit",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				PidsLimit: 2048,
			},
		},
		{
			name: "valid max log size",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogSizeMax: resource.MustParse("10k"),
			},
		},
		{
			name: "valid log level",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogLevel: "debug",
			},
		},
	}

	// Failure Tests
	for _, test := range failureTests {
		ctrcfg := newContainerRuntimeConfig(test.name, test.config, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "", ""))
		err := validateUserContainerRuntimeConfig(ctrcfg)
		if err == nil {
			t.Errorf("%s: failed", test.name)
		}
	}

	// Successful Tests
	for _, test := range successTests {
		ctrcfg := newContainerRuntimeConfig(test.name, test.config, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "", ""))
		err := validateUserContainerRuntimeConfig(ctrcfg)
		if err != nil {
			t.Errorf("%s: failed with %v. should have succeeded", test.name, err)
		}
	}
}

func getKey(config *mcfgv1.ContainerRuntimeConfig, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(config)
	if err != nil {
		t.Errorf("Unexpected error getting key for config %v: %v", config.Name, err)
		return ""
	}
	return key
}
