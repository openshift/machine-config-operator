package containerruntimeconfig

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/clarketm/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/klog/v2"

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

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	apicfgv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	fakeconfigv1client "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1informer "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	fakeoperatorclient "github.com/openshift/client-go/operator/clientset/versioned/fake"
	operatorinformer "github.com/openshift/client-go/operator/informers/externalversions"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/version"
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

	ccLister                 []*mcfgv1.ControllerConfig
	mcpLister                []*mcfgv1.MachineConfigPool
	mccrLister               []*mcfgv1.ContainerRuntimeConfig
	imgLister                []*apicfgv1.Image
	cvLister                 []*apicfgv1.ClusterVersion
	icspLister               []*apioperatorsv1alpha1.ImageContentSourcePolicy
	idmsLister               []*apicfgv1.ImageDigestMirrorSet
	itmsLister               []*apicfgv1.ImageTagMirrorSet
	clusterImagePolicyLister []*apicfgv1.ClusterImagePolicy
	imagePolicyLister        []*apicfgv1.ImagePolicy

	actions               []core.Action
	skipActionsValidation bool

	fgHandler ctrlcommon.FeatureGatesHandler

	objects         []runtime.Object
	imgObjects      []runtime.Object
	operatorObjects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.fgHandler = ctrlcommon.NewFeatureGatesHardcodedHandler(
		[]apicfgv1.FeatureGateName{features.FeatureGateSigstoreImageVerification},
		[]apicfgv1.FeatureGateName{},
	)
	return f
}

func (f *fixture) validateActions() {
	if f.skipActionsValidation {
		f.t.Log("Skipping actions validation")
		return
	}
	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		klog.Infof("Action: %v", action)

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

func (f *fixture) resetActions() {
	f.actions = []core.Action{}
	f.client.ClearActions()
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
					APIServerURL:         fmt.Sprintf("https://api.%s.tt.testing:6443", name),
					APIServerInternalURL: fmt.Sprintf("https://api-int.%s.tt.testing:6443", name),
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
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1, CreationTimestamp: metav1.Now()},
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

func newIDMS(name string, mirrors []apicfgv1.ImageDigestMirrors) *apicfgv1.ImageDigestMirrorSet {
	return &apicfgv1.ImageDigestMirrorSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: apioperatorsv1alpha1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: apicfgv1.ImageDigestMirrorSetSpec{
			ImageDigestMirrors: mirrors,
		},
	}
}

func newITMS(name string, mirrors []apicfgv1.ImageTagMirrors) *apicfgv1.ImageTagMirrorSet {
	return &apicfgv1.ImageTagMirrorSet{
		TypeMeta:   metav1.TypeMeta{APIVersion: apioperatorsv1alpha1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: apicfgv1.ImageTagMirrorSetSpec{
			ImageTagMirrors: mirrors,
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

func newClusterImagePolicyWithPublicKey(name string, scopes []string, keyData []byte) *apicfgv1.ClusterImagePolicy {
	imgScopes := []apicfgv1.ImageScope{}
	for _, scope := range scopes {
		imgScopes = append(imgScopes, apicfgv1.ImageScope(scope))
	}
	return &apicfgv1.ClusterImagePolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: apicfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: apicfgv1.ClusterImagePolicySpec{
			Scopes: imgScopes,
			Policy: apicfgv1.Policy{
				RootOfTrust: apicfgv1.PolicyRootOfTrust{
					PolicyType: apicfgv1.PublicKeyRootOfTrust,
					PublicKey: &apicfgv1.PublicKey{
						KeyData: keyData,
					},
				},
			},
		},
	}
}

func newImagePolicyWithPublicKey(name, namespace string, scopes []string, keyData []byte) *apicfgv1.ImagePolicy {
	imgScopes := []apicfgv1.ImageScope{}
	for _, scope := range scopes {
		imgScopes = append(imgScopes, apicfgv1.ImageScope(scope))
	}
	return &apicfgv1.ImagePolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: apicfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(utilrand.String(5)), Generation: 1},
		Spec: apicfgv1.ImagePolicySpec{
			Scopes: imgScopes,
			Policy: apicfgv1.Policy{
				RootOfTrust: apicfgv1.PolicyRootOfTrust{
					PolicyType: apicfgv1.PublicKeyRootOfTrust,
					PublicKey: &apicfgv1.PublicKey{
						KeyData: keyData,
					},
				},
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
		i.Machineconfiguration().V1alpha1().OSImageStreams(),
		ci.Config().V1().Images(),
		ci.Config().V1().ImageDigestMirrorSets(),
		ci.Config().V1().ImageTagMirrorSets(),
		ci,
		oi.Operator().V1alpha1().ImageContentSourcePolicies(),
		ci.Config().V1().ClusterVersions(),
		k8sfake.NewSimpleClientset(), f.client, f.imgClient,
		f.fgHandler,
	)

	c.mcpListerSynced = alwaysReady
	c.mccrListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.imgListerSynced = alwaysReady
	c.icspListerSynced = alwaysReady
	c.idmsListerSynced = alwaysReady
	c.itmsListerSynced = alwaysReady
	c.clusterImagePolicyListerSynced = alwaysReady
	c.imagePolicyListerSynced = alwaysReady
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
	for _, c := range f.idmsLister {
		ci.Config().V1().ImageDigestMirrorSets().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.itmsLister {
		ci.Config().V1().ImageTagMirrorSets().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.clusterImagePolicyLister {
		ci.Config().V1().ClusterImagePolicies().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.imagePolicyLister {
		ci.Config().V1().ImagePolicies().Informer().GetIndexer().Add(c)
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
	if !c.addedPolicyObservers {
		c.addImagePolicyObservers()
		c.addedPolicyObservers = true
	}
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
func checkAction(expected, actual core.Action, t *testing.T, index int) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
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
				a.GetVerb(), a.GetResource().Resource, diff.Diff(expObject, object))
		}
	case core.PatchAction:
		e, _ := expected.(core.PatchAction)
		expPatch := e.GetPatch()
		patch := a.GetPatch()

		if !equality.Semantic.DeepEqual(expPatch, patch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.Diff(expPatch, patch))
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

func (f *fixture) expectDeleteMachineConfigAction(config *mcfgv1.MachineConfig) {
	f.actions = append(f.actions, core.NewRootDeleteAction(schema.GroupVersionResource{Resource: "machineconfigs"}, config.Name))
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

func (f *fixture) expectUpdateContainerRuntimeConfigRoot(config *mcfgv1.ContainerRuntimeConfig) {
	f.actions = append(f.actions, core.NewRootUpdateAction(schema.GroupVersionResource{Version: "v1", Group: "machineconfiguration.openshift.io", Resource: "containerruntimeconfigs"}, config))
}

type registriesConfigAndPolicyVerifyOptions struct {
	verifyPolicyJSON                    bool
	verifySearchRegsDropin              bool
	verifyImagePoliciesRegistriesConfig bool
	verifyNamedpacedPolicyJSONs         bool
	numberOfImagePolicyNamespaces       int
}

func (f *fixture) verifyRegistriesConfigAndPolicyJSONContents(t *testing.T, mcName string, imgcfg *apicfgv1.Image, icsp *apioperatorsv1alpha1.ImageContentSourcePolicy, idms *apicfgv1.ImageDigestMirrorSet, itms *apicfgv1.ImageTagMirrorSet, clusterImagePolicy *apicfgv1.ClusterImagePolicy, imagePolicy *apicfgv1.ImagePolicy, releaseImageReg string, opts registriesConfigAndPolicyVerifyOptions) {
	icsps := []*apioperatorsv1alpha1.ImageContentSourcePolicy{}
	if icsp != nil {
		icsps = append(icsps, icsp)
	}
	idmss := []*apicfgv1.ImageDigestMirrorSet{}
	if idms != nil {
		idmss = append(idmss, idms)
	}
	itmss := []*apicfgv1.ImageTagMirrorSet{}
	if itms != nil {
		itmss = append(itmss, itms)
	}
	clusterImagePolicies := []*apicfgv1.ClusterImagePolicy{}
	if clusterImagePolicy != nil {
		clusterImagePolicies = append(clusterImagePolicies, clusterImagePolicy)
	}
	imagePolicies := []*apicfgv1.ImagePolicy{}
	if imagePolicy != nil {
		imagePolicies = append(imagePolicies, imagePolicy)
	}
	updatedMC, err := f.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), mcName, metav1.GetOptions{})
	require.NoError(t, err)
	verifyRegistriesConfigAndPolicyJSONContents(t, updatedMC, mcName, imgcfg, icsps, idmss, itmss, clusterImagePolicies, imagePolicies, releaseImageReg, opts)
}

func verifyRegistriesConfigAndPolicyJSONContents(t *testing.T, mc *mcfgv1.MachineConfig, mcName string, imgcfg *apicfgv1.Image, icsps []*apioperatorsv1alpha1.ImageContentSourcePolicy, idmss []*apicfgv1.ImageDigestMirrorSet, itmss []*apicfgv1.ImageTagMirrorSet, clusterImagePolicies []*apicfgv1.ClusterImagePolicy, imagePolicies []*apicfgv1.ImagePolicy, releaseImageReg string, opts registriesConfigAndPolicyVerifyOptions) {
	// This is not testing updateRegistriesConfig, which has its own tests; this verifies the created object contains the expected
	// configuration file.
	// First get the valid blocked registries to ensure we don't block the registry where the release image is from
	registriesBlocked, policyBlocked, allowed, _ := getValidBlockedAndAllowedRegistries(releaseImageReg, &imgcfg.Spec, icsps, idmss)
	expectedRegistriesConf, err := updateRegistriesConfig(templateRegistriesConfig,
		imgcfg.Spec.RegistrySources.InsecureRegistries,
		registriesBlocked, icsps, idmss, itmss)
	require.NoError(t, err)
	assert.Equal(t, mcName, mc.ObjectMeta.Name)

	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	require.NoError(t, err)

	switch {
	case opts.verifyImagePoliciesRegistriesConfig && opts.verifyPolicyJSON && opts.verifySearchRegsDropin && opts.verifyNamedpacedPolicyJSONs:
		require.Len(t, ignCfg.Storage.Files, 4+opts.numberOfImagePolicyNamespaces)

	case opts.verifyImagePoliciesRegistriesConfig && opts.verifyPolicyJSON && opts.verifySearchRegsDropin:
		require.Len(t, ignCfg.Storage.Files, 4)

	case opts.verifyPolicyJSON && opts.verifySearchRegsDropin:
		// If there is a change to the policy.json AND drop-in search registries file then there will be 3 files
		require.Len(t, ignCfg.Storage.Files, 3)

	case opts.verifyPolicyJSON || opts.verifySearchRegsDropin:
		// If there is a change to the policy.json file OR the drop-in search registries file then there will be 2 files
		require.Len(t, ignCfg.Storage.Files, 2)

	default:
		require.Len(t, ignCfg.Storage.Files, 1)
	}

	regfile := ignCfg.Storage.Files[0]
	if regfile.Node.Path != registriesConfigPath {
		regfile = ignCfg.Storage.Files[1]
	}
	assert.Equal(t, registriesConfigPath, regfile.Node.Path)
	registriesConf, err := ctrlcommon.DecodeIgnitionFileContents(regfile.Contents.Source, regfile.Contents.Compression)
	require.NoError(t, err)
	assert.Equal(t, string(expectedRegistriesConf), string(registriesConf))

	clusterScopePolicies, scopeNamespacePolicies, err := getValidScopePolicies(clusterImagePolicies, imagePolicies, nil)
	require.NoError(t, err)

	// Validate the policy.json contents if a change is expected from the tests
	if opts.verifyPolicyJSON {
		allowed = append(allowed, imgcfg.Spec.RegistrySources.AllowedRegistries...)
		expectedPolicyJSON, err := updatePolicyJSON(templatePolicyJSON,
			policyBlocked,
			allowed, releaseImageReg, clusterScopePolicies)
		require.NoError(t, err)
		policyfile := ignCfg.Storage.Files[1]
		if policyfile.Node.Path != policyConfigPath {
			policyfile = ignCfg.Storage.Files[0]
		}
		assert.Equal(t, policyConfigPath, policyfile.Node.Path)
		policyJSON, err := ctrlcommon.DecodeIgnitionFileContents(policyfile.Contents.Source, policyfile.Contents.Compression)
		require.NoError(t, err)
		assert.Equal(t, string(expectedPolicyJSON), string(policyJSON))
	}

	// Validate the drop-in search registries file contents if a change is expected from the tests
	if opts.verifySearchRegsDropin {
		expectedSearchRegsConf := updateSearchRegistriesConfig(imgcfg.Spec.RegistrySources.ContainerRuntimeSearchRegistries)
		foundFile := false
		for _, dropinfile := range ignCfg.Storage.Files {
			if dropinfile.Node.Path == searchRegDropInFilePath {
				foundFile = true
				searchRegsConf, err := ctrlcommon.DecodeIgnitionFileContents(dropinfile.Contents.Source, dropinfile.Contents.Compression)
				require.NoError(t, err)
				assert.Equal(t, string(expectedSearchRegsConf[0].data), string(searchRegsConf))
			}
		}
		if !foundFile {
			t.Errorf("Expected to find drop-in search registries file in machineconfig %s", mcName)
		}
	}

	if opts.verifyImagePoliciesRegistriesConfig {
		expectedRegistriesConfd, err := generateSigstoreRegistriesdConfig(clusterScopePolicies, scopeNamespacePolicies, expectedRegistriesConf)
		require.NoError(t, err)
		foundFile := false

		for _, f := range ignCfg.Storage.Files {
			if f.Node.Path == sigstoreRegistriesConfigFilePath {
				foundFile = true
				require.Equal(t, sigstoreRegistriesConfigFilePath, f.Node.Path)
				registriesYaml, err := ctrlcommon.DecodeIgnitionFileContents(f.Contents.Source, f.Contents.Compression)
				require.NoError(t, err)
				assert.Equal(t, string(expectedRegistriesConfd), string(registriesYaml))
			}
		}
		if !foundFile {
			t.Errorf("Expected to find sigstore registries config file in machineconfig %s", mcName)
		}
	}

	if opts.verifyNamedpacedPolicyJSONs {
		clusterOverridePolicyJSON, err := updatePolicyJSON(templatePolicyJSON,
			policyBlocked,
			allowed, releaseImageReg, clusterScopePolicies)
		require.NoError(t, err)
		expectedNamedpacedPolicyJSONs, err := updateNamespacedPolicyJSONs(clusterOverridePolicyJSON, policyBlocked, allowed, scopeNamespacePolicies)
		require.NoError(t, err)

		foundFile := false
		gotNamespacedPolicyJSONs := make(map[string][]byte)
		for _, f := range ignCfg.Storage.Files {
			if filepath.Dir(f.Node.Path) == constants.CrioPoliciesDir {
				foundFile = true
				policyJSON, err := ctrlcommon.DecodeIgnitionFileContents(f.Contents.Source, f.Contents.Compression)
				require.NoError(t, err)
				namespaceFromPath := strings.TrimSuffix(filepath.Base(f.Node.Path), ".json")
				gotNamespacedPolicyJSONs[namespaceFromPath] = policyJSON
			}
		}
		if !foundFile {
			t.Errorf("Expected to find namespaced policy JSON files under %s directory in machineconfig %s", constants.CrioPoliciesDir, mcName)
		}
		require.Equal(t, expectedNamedpacedPolicyJSONs, gotNamespacedPolicyJSONs)
	}
}

// The patch bytes to expect when creating/updating a containerruntimeconfig
var ctrcfgPatchBytes = []byte("{\"metadata\":{\"finalizers\":[\"99-master-generated-containerruntime\"]}}")

// TestContainerRuntimeConfigCreate ensures that a create happens when an existing containerruntime config is created.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestContainerRuntimeConfigCreate(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()

			nine := resource.MustParse("9k")
			three := resource.MustParse("3G")

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			ctrcfg1 := newContainerRuntimeConfig("set-log-level", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug", LogSizeMax: &nine, OverlaySize: &three}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			ctrCfgKey, _ := getManagedKeyCtrCfg(mcp, f.client, ctrcfg1)
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
			f.expectUpdateContainerRuntimeConfigRoot(ctrcfg1)
			f.expectCreateMachineConfigAction(mcs1)
			f.expectPatchContainerRuntimeConfig(ctrcfg1, ctrcfgPatchBytes)
			f.expectGetMachineConfigAction(mcs2)
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
			f.newController()

			nine := resource.MustParse("9k")
			three := resource.MustParse("3G")

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			ctrcfg1 := newContainerRuntimeConfig("set-log-level", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug", LogSizeMax: &nine, OverlaySize: &three}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			keyCtrCfg, _ := getManagedKeyCtrCfg(mcp, f.client, ctrcfg1)
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
			f.expectUpdateContainerRuntimeConfigRoot(ctrcfg1)
			f.expectCreateMachineConfigAction(mcs)
			f.expectPatchContainerRuntimeConfig(ctrcfg1, ctrcfgPatchBytes)
			f.expectGetMachineConfigAction(mcs)
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

			klog.Info("Applying update")

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
			f.expectGetMachineConfigAction(mcsUpdate)
			f.expectUpdateContainerRuntimeConfig(ctrcfgUpdate)

			f.validateActions()

			close(stopCh)
		})
	}
}

// TestImageConfigCreate ensures that a create happens when an image config is created.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestImageConfigCreate(t *testing.T) {
	verifyOpts := registriesConfigAndPolicyVerifyOptions{
		verifyPolicyJSON:                    true,
		verifySearchRegsDropin:              true,
		verifyImagePoliciesRegistriesConfig: false,
	}

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, nil, nil, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
			}
		})
	}
}

// TestImageConfigUpdate ensures that an update happens when an existing image config is updated.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestImageConfigUpdate(t *testing.T) {
	verifyOpts := registriesConfigAndPolicyVerifyOptions{
		verifyPolicyJSON:                    true,
		verifySearchRegsDropin:              true,
		verifyImagePoliciesRegistriesConfig: false,
	}

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, nil, nil, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
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

			klog.Info("Applying update")

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfgUpdate, nil, nil, nil, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
			}
		})
	}
}

// TestICSPUpdate ensures that an update happens when an existing ICSP is updated.
// It tests that the necessary get, create, and update steps happen in the correct order.
func TestICSPUpdate(t *testing.T) {
	verifyOpts := registriesConfigAndPolicyVerifyOptions{
		verifyPolicyJSON:                    false,
		verifySearchRegsDropin:              false,
		verifyImagePoliciesRegistriesConfig: false,
	}

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, icsp, nil, nil, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
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

			klog.Info("Applying update")

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, icspUpdate, nil, nil, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
			}
		})
	}
}

func TestIDMSUpdate(t *testing.T) {
	verifyOpts := registriesConfigAndPolicyVerifyOptions{
		verifyPolicyJSON:                    false,
		verifySearchRegsDropin:              false,
		verifyImagePoliciesRegistriesConfig: false,
	}

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
			idms := newIDMS("built-in", []apicfgv1.ImageDigestMirrors{
				{Source: "built-in-source.example.com", Mirrors: []apicfgv1.ImageMirror{"built-in-mirror.example.com"}},
			})
			mcs1Update := mcs1.DeepCopy()
			mcs2Update := mcs2.DeepCopy()
			mcs1Update.Name = keyReg1
			mcs2Update.Name = keyReg2

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.idmsLister = append(f.idmsLister, idms)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.imgObjects = append(f.imgObjects, imgcfg1)
			f.operatorObjects = append(f.operatorObjects, idms)

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, idms, nil, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
			}

			// Perform Update
			f = newFixture(t)

			// Modify IDMS
			idmsUpdate := idms.DeepCopy()
			idmsUpdate.Spec.ImageDigestMirrors = append(idmsUpdate.Spec.ImageDigestMirrors, apicfgv1.ImageDigestMirrors{
				Source: "built-in-source.example.com", Mirrors: []apicfgv1.ImageMirror{"local-mirror.local"},
			})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.idmsLister = append(f.idmsLister, idmsUpdate)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.objects = append(f.objects, mcs1Update, mcs2Update)
			f.imgObjects = append(f.imgObjects, imgcfg1)
			f.operatorObjects = append(f.operatorObjects, idmsUpdate)

			c = f.newController()
			stopCh = make(chan struct{})

			klog.Info("Applying update")

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, idmsUpdate, nil, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
			}
		})
	}
}

func TestITMSUpdate(t *testing.T) {
	verifyOpts := registriesConfigAndPolicyVerifyOptions{
		verifyPolicyJSON:                    false,
		verifySearchRegsDropin:              false,
		verifyImagePoliciesRegistriesConfig: false,
	}

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
			itms := newITMS("built-in", []apicfgv1.ImageTagMirrors{
				{Source: "built-in-source.example.com", Mirrors: []apicfgv1.ImageMirror{"built-in-mirror.example.com"}},
			})
			mcs1Update := mcs1.DeepCopy()
			mcs2Update := mcs2.DeepCopy()
			mcs1Update.Name = keyReg1
			mcs2Update.Name = keyReg2

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.itmsLister = append(f.itmsLister, itms)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.imgObjects = append(f.imgObjects, imgcfg1)
			f.operatorObjects = append(f.operatorObjects, itms)

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, nil, itms, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
			}

			// Perform Update
			f = newFixture(t)

			// Modify ITMS
			itmsUpdate := itms.DeepCopy()
			itmsUpdate.Spec.ImageTagMirrors = append(itmsUpdate.Spec.ImageTagMirrors, apicfgv1.ImageTagMirrors{
				Source: "built-in-source.example.com", Mirrors: []apicfgv1.ImageMirror{"local-mirror.local"},
			})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.itmsLister = append(f.itmsLister, itmsUpdate)
			f.cvLister = append(f.cvLister, cvcfg1)
			f.objects = append(f.objects, mcs1Update, mcs2Update)
			f.imgObjects = append(f.imgObjects, imgcfg1)
			f.operatorObjects = append(f.operatorObjects, itmsUpdate)

			c = f.newController()
			stopCh = make(chan struct{})

			klog.Info("Applying update")

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
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, nil, itmsUpdate, nil, nil, cc.Spec.ReleaseImage, verifyOpts)
			}
		})
	}
}

func TestRunImageBootstrap(t *testing.T) {
	testClusterImagePolicy := clusterImagePolicyTestCRs()["test-cr0"]
	testImagePolicy := imagePolicyTestCRs()["test-cr2"]
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		for _, tc := range []struct {
			icspRules             []*apioperatorsv1alpha1.ImageContentSourcePolicy
			idmsRules             []*apicfgv1.ImageDigestMirrorSet
			itmsRules             []*apicfgv1.ImageTagMirrorSet
			clusterImagePolicies  []*apicfgv1.ClusterImagePolicy
			imagePolicies         []*apicfgv1.ImagePolicy
			imagePolicyNamespaces int
		}{
			{
				idmsRules: []*apicfgv1.ImageDigestMirrorSet{
					newIDMS("idms-1", []apicfgv1.ImageDigestMirrors{
						{Source: "source.example.com", Mirrors: []apicfgv1.ImageMirror{"mirror.example.com"}},
					}),
				},
				icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
					newICSP("built-in", []apioperatorsv1alpha1.RepositoryDigestMirrors{
						{Source: "built-in-source.example.com", Mirrors: []string{"built-in-mirror.example.com"}},
						{Source: "built-in-source.example.com", Mirrors: []string{"local-mirror.local"}},
					}),
				},
			},
			{
				idmsRules: []*apicfgv1.ImageDigestMirrorSet{
					newIDMS("idms-1", []apicfgv1.ImageDigestMirrors{
						{Source: "source.example.com", Mirrors: []apicfgv1.ImageMirror{"mirror.example.com"}},
					}),
				},
				itmsRules: []*apicfgv1.ImageTagMirrorSet{
					newITMS("itms-1", []apicfgv1.ImageTagMirrors{
						{Source: "source.example.com", Mirrors: []apicfgv1.ImageMirror{"local.mirrorexample"}},
					}),
				},
			},
			{
				clusterImagePolicies: []*apicfgv1.ClusterImagePolicy{
					&testClusterImagePolicy,
				},
				imagePolicies: []*apicfgv1.ImagePolicy{
					&testImagePolicy,
				},
				imagePolicyNamespaces: 1,
			},
		} {

			t.Run(string(platform), func(t *testing.T) {
				cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
				pools := []*mcfgv1.MachineConfigPool{
					helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
					helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
				}
				// Adding the release-image registry "release-reg.io" to the list of blocked registries to ensure that is it not added to
				// both registries.conf and policy.json as blocked
				imgCfg := newImageConfig("cluster", &apicfgv1.RegistrySources{InsecureRegistries: []string{"insecure-reg-1.io", "insecure-reg-2.io"}, BlockedRegistries: []string{"blocked-reg.io", "release-reg.io"}, ContainerRuntimeSearchRegistries: []string{"search-reg.io"}})
				// set FeatureGateSigstoreImageVerification enabled for testing
				fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]apicfgv1.FeatureGateName{features.FeatureGateSigstoreImageVerification}, []apicfgv1.FeatureGateName{})

				mcs, err := RunImageBootstrap("../../../templates", cc, pools, tc.icspRules, tc.idmsRules, tc.itmsRules, imgCfg, tc.clusterImagePolicies, tc.imagePolicies, fgHandler, nil)
				require.NoError(t, err)

				require.Len(t, mcs, len(pools))
				for i := range pools {
					keyReg, _ := getManagedKeyReg(pools[i], nil)
					verifyOpts := registriesConfigAndPolicyVerifyOptions{
						verifyPolicyJSON:       true,
						verifySearchRegsDropin: true,
					}
					if tc.clusterImagePolicies != nil {
						verifyOpts.verifyImagePoliciesRegistriesConfig = true
					}
					if tc.imagePolicies != nil {
						verifyOpts.verifyImagePoliciesRegistriesConfig = true
						verifyOpts.verifyNamedpacedPolicyJSONs = true
						verifyOpts.numberOfImagePolicyNamespaces = tc.imagePolicyNamespaces
					}
					verifyRegistriesConfigAndPolicyJSONContents(t, mcs[i], keyReg, imgCfg, tc.icspRules, tc.idmsRules, tc.itmsRules, tc.clusterImagePolicies, tc.imagePolicies, cc.Spec.ReleaseImage, verifyOpts)
				}
			})
		}
	}
}

// TestRegistriesValidation tests the validity of registries allowed to be listed
// under blocked registries
func TestRegistriesValidation(t *testing.T) {
	failureTests := []struct {
		name      string
		config    *apicfgv1.RegistrySources
		idmsRules []*apicfgv1.ImageDigestMirrorSet
	}{
		{
			name: "adding registry used by payload to blocked registries",
			config: &apicfgv1.RegistrySources{
				BlockedRegistries:  []string{"blah.io", "docker.io"},
				InsecureRegistries: []string{"test.io"},
			},
		},
		{
			name: "adding registry used by payload to blocked registries with mirror rules configured for the a different repo in the reg",
			config: &apicfgv1.RegistrySources{
				BlockedRegistries:  []string{"blah.io", "docker.io"},
				InsecureRegistries: []string{"test.io"},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "blah.io/myuser", Mirrors: []apicfgv1.ImageMirror{"mirror-1.io/myuser", "mirror-2.io/myuser"}},
						},
					},
				},
			},
		},
	}

	successTests := []struct {
		name                                                              string
		expectedRegistriesBlocked, expectedPolicyBlocked, expectedAllowed []string
		config                                                            *apicfgv1.RegistrySources
		idmsRules                                                         []*apicfgv1.ImageDigestMirrorSet
	}{
		{
			name: "adding registry used by payload to insecure registries",
			config: &apicfgv1.RegistrySources{
				BlockedRegistries:  []string{"docker.io"},
				InsecureRegistries: []string{"blah.io"},
			},
			expectedRegistriesBlocked: []string{"docker.io"},
			expectedPolicyBlocked:     []string{"docker.io"},
		},
		{
			name: "adding registry used by payload to blocked registries with mirror rules configured for the payload reg",
			config: &apicfgv1.RegistrySources{
				BlockedRegistries:  []string{"blah.io", "docker.io"},
				InsecureRegistries: []string{"test.io"},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "blah.io/payload", Mirrors: []apicfgv1.ImageMirror{"mirror-1.io/payload", "mirror-2.io/payload"}},
						},
					},
				},
			},
			expectedRegistriesBlocked: []string{"blah.io", "docker.io"},
			expectedPolicyBlocked:     []string{"blah.io", "docker.io"},
			expectedAllowed:           []string{"blah.io/payload/myimage"},
		},
		{
			name: "adding payload repository to blocked registries with mirror rules configured for the payload registry",
			config: &apicfgv1.RegistrySources{
				BlockedRegistries:  []string{"blah.io/payload", "docker.io"},
				InsecureRegistries: []string{"test.io"},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "blah.io", Mirrors: []apicfgv1.ImageMirror{"mirror-1.io", "mirror-2.io"}},
						},
					},
				},
			},
			expectedRegistriesBlocked: []string{"blah.io/payload", "docker.io"},
			expectedPolicyBlocked:     []string{"blah.io/payload", "docker.io"},
			expectedAllowed:           []string{"blah.io/payload/myimage"},
		},
	}

	// Failure Tests
	for _, test := range failureTests {
		imgcfg := newImageConfig(test.name, test.config)
		cvcfg := newClusterVersionConfig("version", "blah.io/payload/myimage@sha256:4207ba569ff014931f1b5d125fe3751936a768e119546683c899eb09f3cdceb0")
		registriesBlocked, _, _, err := getValidBlockedAndAllowedRegistries(cvcfg.Status.Desired.Image, &imgcfg.Spec, nil, test.idmsRules)
		if err == nil {
			t.Errorf("%s: failed", test.name)
		}
		for _, reg := range registriesBlocked {
			if reg == "blah.io" {
				t.Errorf("%s: failed to filter out registry being used by payload", test.name)
			}
		}
	}

	// Successful Tests
	for _, test := range successTests {
		imgcfg := newImageConfig(test.name, test.config)
		cvcfg := newClusterVersionConfig("version", "blah.io/payload/myimage@sha256:4207ba569ff014931f1b5d125fe3751936a768e119546683c899eb09f3cdceb0")
		registriesBlocked, policyBlocked, allowed, err := getValidBlockedAndAllowedRegistries(cvcfg.Status.Desired.Image, &imgcfg.Spec, nil, test.idmsRules)
		if err != nil {
			t.Errorf("%s: failed", test.name)
		}
		assert.Equal(t, test.expectedRegistriesBlocked, registriesBlocked)
		assert.Equal(t, test.expectedPolicyBlocked, policyBlocked)
		assert.Equal(t, test.expectedAllowed, allowed)
	}
}

// TestContainerRuntimeConfigOptions tests the validity of allowed and not allowed values
// for the options in containerruntime config
func TestContainerRuntimeConfigOptions(t *testing.T) {
	var (
		invalidPidsLimit int64 = 10
		validPidsLimit   int64 = 2048
		validZerolimit   int64 = 0
		invalidNegLimit  int64 = -10
		three                  = resource.MustParse("3k")
		ten                    = resource.MustParse("10k")
	)
	failureTests := []struct {
		name   string
		config *mcfgv1.ContainerRuntimeConfiguration
	}{
		{
			name: "invalid value of pids limit",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				PidsLimit: &invalidPidsLimit,
			},
		},
		{
			name: "invalid negative pids limit",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				PidsLimit: &invalidNegLimit,
			},
		},
		{
			name: "inalid value of max log size",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogSizeMax: &three,
			},
		},
		{
			name: "inalid value of log level",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogLevel: "invalid",
			},
		},
		{
			name: "invalid value of default runtime",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				DefaultRuntime: "invalid",
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
				PidsLimit: &validPidsLimit,
			},
		},
		{
			name: "valid 0 pids limit",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				PidsLimit: &validZerolimit,
			},
		},
		{
			name: "valid max log size",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogSizeMax: &ten,
			},
		},
		{
			name: "valid log level",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogLevel: "debug",
			},
		},
		{
			name: "valid value of default runtime",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				DefaultRuntime: "crun",
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

func TestMarshalResourceQuantityOptionsJSON(t *testing.T) {
	var (
		validLogSizeMax  = resource.MustParse("10k")
		validOverlaySize = resource.MustParse("10G")
	)

	emptyValueTests := []struct {
		name   string
		config *mcfgv1.ContainerRuntimeConfiguration
	}{
		{
			name: "valid log level, overlaySize/logsizeMax should not appear in json",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogLevel: "debug",
			},
		},
		{
			name: "valid value of default runtime， overlaySize/logsizeMax should not appear in json",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				DefaultRuntime: "crun",
			},
		},
	}

	successTests := []struct {
		name      string
		config    *mcfgv1.ContainerRuntimeConfiguration
		expectStr string
	}{
		{
			name: "valid max log size should appear in json",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				LogSizeMax: &validLogSizeMax,
				LogLevel:   "debug",
			},
			expectStr: "\"logSizeMax\":\"10k\"",
		},
		{
			name: "valid max overlay size should appear in json",
			config: &mcfgv1.ContainerRuntimeConfiguration{
				OverlaySize: &validOverlaySize,
				LogLevel:    "debug",
			},
			expectStr: "\"overlaySize\":\"10G\"",
		},
	}

	// Successful Tests
	for _, test := range successTests {
		ctrcfg := newContainerRuntimeConfig(test.name, test.config, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "", ""))
		data, err := json.Marshal(ctrcfg)
		if err != nil {
			t.Errorf("%s: failed with %v. should have succeeded", test.name, err)
		}
		require.Contains(t, string(data), test.expectStr)
	}

	for _, test := range emptyValueTests {
		ctrcfg := newContainerRuntimeConfig(test.name, test.config, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "", ""))
		data, err := json.Marshal(ctrcfg)
		if err != nil {
			t.Errorf("%s: failed with %v. should have succeeded", test.name, err)
		}
		require.NotContains(t, string(data), "\"overlaySize\"", "\"overlaySize\"")
		require.NotContains(t, string(data), "\"logSizeMax\"", "\"logSizeMax\"")
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

func generateManagedKey(ctrcfg *mcfgv1.ContainerRuntimeConfig, generation uint64) string {
	return fmt.Sprintf("99-%s-generated-containerruntime-%v", ctrcfg.Name, generation)
}

func TestCtrruntimeConfigMultiCreate(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			f.ccLister = append(f.ccLister, cc)

			ctrcfgCount := 30
			for i := 0; i < ctrcfgCount; i++ {
				f.resetActions()

				poolName := fmt.Sprintf("subpool%v", i)
				poolLabelName := fmt.Sprintf("pools.operator.machineconfiguration.openshift.io/%s", poolName)
				labelSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, poolLabelName, "")

				mcp := helpers.NewMachineConfigPool(poolName, nil, labelSelector, "v0")
				mcp.ObjectMeta.Labels[poolLabelName] = ""

				ccr := newContainerRuntimeConfig(poolName, &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, labelSelector)

				f.mcpLister = append(f.mcpLister, mcp)
				f.mccrLister = append(f.mccrLister, ccr)
				f.objects = append(f.objects, ccr)

				mcs := helpers.NewMachineConfig(generateManagedKey(ccr, 1), labelSelector.MatchLabels, "dummy://", []ign3types.File{{}})
				mcsDeprecated := mcs.DeepCopy()
				mcsDeprecated.Name = getManagedKeyCtrCfgDeprecated(mcp)

				expectedPatch := fmt.Sprintf("{\"metadata\":{\"finalizers\":[\"99-%v-generated-containerruntime\"]}}", poolName)

				f.expectGetMachineConfigAction(mcs)
				f.expectGetMachineConfigAction(mcsDeprecated)
				f.expectGetMachineConfigAction(mcs)
				f.expectUpdateContainerRuntimeConfigRoot(ccr)
				f.expectCreateMachineConfigAction(mcs)
				f.expectPatchContainerRuntimeConfig(ccr, []byte(expectedPatch))
				f.expectGetMachineConfigAction(mcs)
				f.expectUpdateContainerRuntimeConfig(ccr)
				f.run(poolName)
			}
		})
	}
}

func TestContainerruntimeConfigResync(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			ccr1 := newContainerRuntimeConfig("log-level-1", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			ccr2 := newContainerRuntimeConfig("log-level-2", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

			ctrConfigKey, _ := getManagedKeyCtrCfg(mcp, f.client, ccr1)
			mcs := helpers.NewMachineConfig(ctrConfigKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})
			mcsDeprecated := mcs.DeepCopy()
			mcsDeprecated.Name = getManagedKeyCtrCfgDeprecated(mcp)

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mccrLister = append(f.mccrLister, ccr1)
			f.objects = append(f.objects, ccr1)

			c := f.newController()
			err := c.syncHandler(getKey(ccr1, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			f.mccrLister = append(f.mccrLister, ccr2)
			f.objects = append(f.objects, ccr2)

			c = f.newController()
			err = c.syncHandler(getKey(ccr2, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			val := ccr2.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "1", val)

			val = ccr1.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)

			// resync kc1 and kc2
			c = f.newController()
			err = c.syncHandler(getKey(ccr1, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}
			val = ccr1.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)

			c = f.newController()
			err = c.syncHandler(getKey(ccr2, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}
			val = ccr2.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "1", val)
		})
	}
}

func TestAddAnnotationExistingContainerRuntimeConfig(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")

			ctrMCKey := "99-master-generated-containerruntime"
			ctr1MCKey := "99-master-generated-containerruntime-1"
			ctrc := newContainerRuntimeConfig("log-level", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			ctrc.Finalizers = []string{ctrMCKey}
			ctrc1 := newContainerRuntimeConfig("log-level-1", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			ctrc1.SetAnnotations(map[string]string{ctrlcommon.MCNameSuffixAnnotationKey: "1"})
			ctrc1.Finalizers = []string{ctr1MCKey}
			ctrcfgMC := helpers.NewMachineConfig(ctrMCKey, map[string]string{"node-role/master": ""}, "dummy://", []ign3types.File{{}})

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.mccrLister = append(f.mccrLister, ctrc, ctrc1)
			f.objects = append(f.objects, ctrc, ctrc1, ctrcfgMC)

			// ctrc created before ctrc1,
			// make sure ccr does not have annotation machineconfiguration.openshift.io/mc-name-suffix before sync, ccr1 has annotation machineconfiguration.openshift.io/mc-name-suffix
			_, ok := ctrc.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.False(t, ok)
			val := ctrc1.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "1", val)

			// no new machine config will be created
			f.expectGetMachineConfigAction(ctrcfgMC)
			f.expectGetMachineConfigAction(ctrcfgMC)
			f.expectUpdateContainerRuntimeConfigRoot(ctrc)
			f.expectUpdateMachineConfigAction(ctrcfgMC)
			f.expectPatchContainerRuntimeConfig(ctrc, []byte("{}"))
			f.expectGetMachineConfigAction(ctrcfgMC)
			f.expectUpdateContainerRuntimeConfig(ctrc)

			c := f.newController()
			err := c.syncHandler(getKey(ctrc, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}
			f.validateActions()

			// ctrc annotation machineconfiguration.openshift.io/mc-name-suffix after sync
			val = ctrc.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)
			ctrcFinalizers := ctrc.GetFinalizers()
			require.Equal(t, 1, len(ctrcFinalizers))
			require.Equal(t, ctrMCKey, ctrcFinalizers[0])
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
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)

			// test case: containerruntimrconfig ccr1, two machine config was generated from it: 99-master-generated-containerruntime, 99-master-generated-containerruntime-1
			// action: upgrade, only 99-master-generated-containerruntime-1 will update GeneratedByControllerVersionAnnotationKey
			// expect result: 99-master-generated-containerruntime will be deleted
			ccr1 := newContainerRuntimeConfig("log-level-1", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))
			ccr1.SetAnnotations(map[string]string{
				ctrlcommon.MCNameSuffixAnnotationKey: "1",
			})
			f.mccrLister = append(f.mccrLister, ccr1)
			f.objects = append(f.objects, ccr1)

			ctrl := f.newController()

			// machineconfig with wrong version needs to be removed
			machineConfigDegrade := mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "99-master-generated-containerruntime", UID: types.UID(utilrand.String(5))},
			}
			machineConfigDegrade.Annotations = make(map[string]string)
			machineConfigDegrade.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = versionDegrade
			ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), &machineConfigDegrade, metav1.CreateOptions{})

			// MC will be upgraded
			machineConfigUpgrade := mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "99-master-generated-containerruntime-1", UID: types.UID(utilrand.String(5))},
			}
			machineConfigUpgrade.Annotations = make(map[string]string)
			machineConfigUpgrade.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = versionDegrade
			ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), &machineConfigUpgrade, metav1.CreateOptions{})

			// machine config has no substring "generated-xxx" will stay
			machineConfigDegradeNotGen := mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "custom-containerruntime", UID: types.UID(utilrand.String(5))},
			}
			machineConfigDegradeNotGen.Annotations = make(map[string]string)
			machineConfigDegradeNotGen.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = versionDegrade
			ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), &machineConfigDegradeNotGen, metav1.CreateOptions{})

			// before the upgrade, 3 machine config exist
			mcList, err := ctrl.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, mcList.Items, 3)

			if err := ctrl.syncHandler(getKey(ccr1, t)); err != nil {
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
			require.True(t, ok, "expect custom-containerruntime in the list, but got false")
			_, ok = actual[machineConfigUpgrade.Name]
			require.True(t, ok, "expect 99-master-generated-containerruntime-1 in the list, but got false")
		})
	}
}

func TestClusterImagePolicyCreate(t *testing.T) {
	verifyOpts := registriesConfigAndPolicyVerifyOptions{
		verifyPolicyJSON:                    true,
		verifySearchRegsDropin:              true,
		verifyImagePoliciesRegistriesConfig: true,
	}

	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			imgcfg1 := newImageConfig("cluster", &apicfgv1.RegistrySources{InsecureRegistries: []string{"blah.io"}, AllowedRegistries: []string{"example.com"}, ContainerRuntimeSearchRegistries: []string{"search-reg.io"}})

			cvcfg1 := newClusterVersionConfig("version", "test.io/myuser/myimage:test")
			keyReg1, _ := getManagedKeyReg(mcp, nil)
			keyReg2, _ := getManagedKeyReg(mcp2, nil)

			mcs1 := helpers.NewMachineConfig(keyReg1, map[string]string{"node-role": "master"}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(keyReg2, map[string]string{"node-role": "worker"}, "dummy://", []ign3types.File{{}})

			clusterimgPolicy := newClusterImagePolicyWithPublicKey("image-policy", []string{"example.com"}, []byte("foo bar"))
			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.clusterImagePolicyLister = append(f.clusterImagePolicyLister, clusterimgPolicy)
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

			f.run("")

			for _, mcName := range []string{mcs1.Name, mcs2.Name} {
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, nil, nil, clusterimgPolicy, nil, cc.Spec.ReleaseImage, verifyOpts)
			}
		})
	}
}

func TestSigstoreRegistriesConfigIDMSandCIPCreate(t *testing.T) {
	verifyOpts := registriesConfigAndPolicyVerifyOptions{
		verifyPolicyJSON:                    true,
		verifySearchRegsDropin:              true,
		verifyImagePoliciesRegistriesConfig: true,
	}

	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			imgcfg1 := newImageConfig("cluster", &apicfgv1.RegistrySources{InsecureRegistries: []string{"blah.io"}, AllowedRegistries: []string{"example.com"}, ContainerRuntimeSearchRegistries: []string{"search-reg.io"}})

			cvcfg1 := newClusterVersionConfig("version", "test.io/myuser/myimage:test")
			keyReg1, _ := getManagedKeyReg(mcp, nil)
			keyReg2, _ := getManagedKeyReg(mcp2, nil)

			mcs1 := helpers.NewMachineConfig(keyReg1, map[string]string{"node-role": "master"}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(keyReg2, map[string]string{"node-role": "worker"}, "dummy://", []ign3types.File{{}})

			// idms source is the same as cip scope
			idms := newIDMS("built-in", []apicfgv1.ImageDigestMirrors{
				{Source: "built-in-source.example.com", Mirrors: []apicfgv1.ImageMirror{"built-in-mirror.example.com"}},
			})
			clusterimgPolicy := newClusterImagePolicyWithPublicKey("built-in-source.example.com", []string{"example.com"}, []byte("foo bar"))
			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.idmsLister = append(f.idmsLister, idms)
			f.clusterImagePolicyLister = append(f.clusterImagePolicyLister, clusterimgPolicy)
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

			f.run("")

			for _, mcName := range []string{mcs1.Name, mcs2.Name} {
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, idms, nil, clusterimgPolicy, nil, cc.Spec.ReleaseImage, verifyOpts)
			}
		})
	}
}

func TestImagePolicyCreate(t *testing.T) {
	verifyOpts := registriesConfigAndPolicyVerifyOptions{
		verifyPolicyJSON:                    true,
		verifySearchRegsDropin:              true,
		verifyImagePoliciesRegistriesConfig: true,
		verifyNamedpacedPolicyJSONs:         true,
		numberOfImagePolicyNamespaces:       1,
	}

	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			mcp := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
			mcp2 := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
			imgcfg1 := newImageConfig("cluster", &apicfgv1.RegistrySources{InsecureRegistries: []string{"blah.io"}, AllowedRegistries: []string{"example.com"}, ContainerRuntimeSearchRegistries: []string{"search-reg.io"}})

			cvcfg1 := newClusterVersionConfig("version", "test.io/myuser/myimage:test")
			keyReg1, _ := getManagedKeyReg(mcp, nil)
			keyReg2, _ := getManagedKeyReg(mcp2, nil)

			mcs1 := helpers.NewMachineConfig(keyReg1, map[string]string{"node-role": "master"}, "dummy://", []ign3types.File{{}})
			mcs2 := helpers.NewMachineConfig(keyReg2, map[string]string{"node-role": "worker"}, "dummy://", []ign3types.File{{}})

			imgPolicy := newImagePolicyWithPublicKey("image-policy", "testnamespace", []string{"example.com"}, []byte("namespace foo bar"))
			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp)
			f.mcpLister = append(f.mcpLister, mcp2)
			f.imgLister = append(f.imgLister, imgcfg1)
			f.imagePolicyLister = append(f.imagePolicyLister, imgPolicy)
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

			f.run("")

			for _, mcName := range []string{mcs1.Name, mcs2.Name} {
				f.verifyRegistriesConfigAndPolicyJSONContents(t, mcName, imgcfg1, nil, nil, nil, nil, imgPolicy, cc.Spec.ReleaseImage, verifyOpts)
			}
		})
	}
}
