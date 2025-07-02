package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	mcopfake "github.com/openshift/client-go/operator/clientset/versioned/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/helpers"
)

var pathtests = []struct {
	path    string
	isValid bool
}{
	{".good", true},
	{"./good", true},
	{"/good", true},
	{"../good", true},
	{"bad", false},
}

func TestValidPath(t *testing.T) {
	var isValid bool
	for _, tt := range pathtests {
		isValid = ValidPath(tt.path)
		if isValid != tt.isValid {
			t.Errorf("%s isValid should be %s, found %s", tt.path, strconv.FormatBool(tt.isValid), strconv.FormatBool(isValid))
		}
	}
}

func TestValidateFiles(t *testing.T) {
	fi, err := os.Lstat("fixtures/test1.txt")
	if err != nil {
		t.Errorf("Could not Lstat file: %v", err)
	}
	fileMode := int(fi.Mode().Perm())

	// validate single file in spec 3
	filesV3 := []ign3types.File{
		{
			Node: ign3types.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte("hello world\n"))),
				},
				Mode: &fileMode,
			},
		},
	}

	if err := checkV3Files(filesV3); err != nil {
		t.Errorf("Invalid files: %v", err)
	}

	// validate overwritten file in spec 2
	filesV2 := []ign2types.File{
		{
			Node: ign2types.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: ign2types.FileEmbedded1{
				Contents: ign2types.FileContents{
					Source: dataurl.EncodeBytes([]byte("hello\n")),
				},
				Mode: &fileMode,
			},
		},
		{
			Node: ign2types.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: ign2types.FileEmbedded1{
				Contents: ign2types.FileContents{
					Source: dataurl.EncodeBytes([]byte("hello world\n")),
				},
				Mode: &fileMode,
			},
		},
	}

	if err := checkV2Files(filesV2); err != nil {
		t.Errorf("Validating an overwritten file failed: %v", err)
	}
}

type fixture struct {
	t *testing.T

	client     *fake.Clientset
	kubeclient *k8sfake.Clientset
	oclient    *mcopfake.Clientset

	mcLister   []*mcfgv1.MachineConfig
	nodeLister []*corev1.Node

	kubeactions []core.Action
	actions     []core.Action

	objects     []runtime.Object
	kubeobjects []runtime.Object
	oObjects    []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.kubeobjects = []runtime.Object{}
	return f
}

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

func (f *fixture) newController() *Daemon {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)
	f.oclient = mcopfake.NewSimpleClientset(f.oObjects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	k8sI := kubeinformers.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())

	d, err := New(nil)
	if err != nil {
		f.t.Fatalf("can't bring up daemon: %v", err)
	}
	d.ClusterConnect("node_name_test",
		f.kubeclient,
		f.client,
		i.Machineconfiguration().V1().MachineConfigs(),
		k8sI.Core().V1().Nodes(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().MachineConfigPools(),
		f.oclient,
		false,
		"",
		d.fgHandler,
	)

	d.mcListerSynced = alwaysReady
	d.nodeListerSynced = alwaysReady
	d.mcpListerSynced = alwaysReady

	stopCh := make(chan struct{})
	defer close(stopCh)
	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)
	k8sI.Start(stopCh)
	k8sI.WaitForCacheSync(stopCh)

	for _, mc := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(mc)
	}

	for _, n := range f.nodeLister {
		k8sI.Core().V1().Nodes().Informer().GetIndexer().Add(n)
	}

	return d
}

func (f *fixture) run(node string) {
	f.runController(node, false)
}

func (f *fixture) runExpectError(node string) {
	f.runController(node, true)
}

func (f *fixture) runController(node string, expectError bool) {
	d := f.newController()

	err := d.syncHandler(node)
	if !expectError && err != nil {
		f.t.Errorf("error syncing node: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing node, got nil")
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
		expObject := filterLastTransitionTime(e.GetObject())
		object := filterLastTransitionTime(a.GetObject())

		if !equality.Semantic.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintDiff(expObject, object))
		}
	case core.UpdateAction:
		e, _ := expected.(core.UpdateAction)
		expObject := filterLastTransitionTime(e.GetObject())
		object := filterLastTransitionTime(a.GetObject())

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
			(action.Matches("list", "machineconfigs") ||
				action.Matches("watch", "machineconfigs") ||
				action.Matches("list", "nodes") ||
				action.Matches("watch", "nodes")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func getKey(node *corev1.Node, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(node)
	if err != nil {
		t.Errorf("Unexpected error getting key for node %v: %v", node.Name, err)
		return ""
	}
	return key
}

func filterLastTransitionTime(obj runtime.Object) runtime.Object {
	obj = obj.DeepCopyObject()
	o, ok := obj.(*corev1.Node)
	if !ok {
		return obj
	}

	for idx := range o.Status.Conditions {
		o.Status.Conditions[idx].LastTransitionTime = metav1.Time{}
	}
	return o
}

func newNode(annotations map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotations,
		},
	}
}

func TestSetRunningKargs(t *testing.T) {
	oldIgnCfg := ctrlcommon.NewIgnConfig()
	oldConfig := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	oldConfig.ObjectMeta = metav1.ObjectMeta{Name: "oldconfig"}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newConfig := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	newConfig.ObjectMeta = metav1.ObjectMeta{Name: "newconfig"}
	diff, err := newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.True(t, diff.isEmpty())

	cmdline := "BOOT_IMAGE=(hd0,gpt3)/ostree/rhcos-c3b004db4/vmlinuz-5.14.0-284.23.1.el9_2.x86_64 systemd.unified_cgroup_hierarchy=0 systemd.legacy_systemd_cgroup_controller=1"
	newConfig.Spec.KernelArguments = []string{"systemd.unified_cgroup_hierarchy=0", "systemd.legacy_systemd_cgroup_controller=1"}
	_ = setRunningKargsWithCmdline(oldConfig, newConfig.Spec.KernelArguments, []byte(cmdline))
	diff, err = newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.True(t, diff.isEmpty())

	newConfig.Spec.KernelArguments = []string{"systemd.legacy_systemd_cgroup_controller=1", "systemd.unified_cgroup_hierarchy=0"}
	diff, err = newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.False(t, diff.isEmpty())
	assert.True(t, diff.kargs)

	newConfig.Spec.KernelArguments = []string{"systemd.unified_cgroup_hierarchy=0", "systemd.legacy_systemd_cgroup_controller=1", "systemd.unified_cgroup_hierarchy=0"}
	diff, err = newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.False(t, diff.isEmpty())
	assert.True(t, diff.kargs)

	cmdline = "BOOT_IMAGE=(hd0,gpt3)/ostree/rhcos-c3b004db4/vmlinuz-5.14.0-284.23.1.el9_2.x86_64 systemd.unified_cgroup_hierarchy=0 systemd.unified_cgroup_hierarchy=0 systemd.legacy_systemd_cgroup_controller=1"
	_ = setRunningKargsWithCmdline(oldConfig, newConfig.Spec.KernelArguments, []byte(cmdline))
	diff, err = newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.False(t, diff.isEmpty())
	assert.True(t, diff.kargs)

	cmdline = "BOOT_IMAGE=(hd0,gpt3)/ostree/rhcos-c3b004db4/vmlinuz-5.14.0-284.23.1.el9_2.x86_64 systemd.unified_cgroup_hierarchy=0 systemd.legacy_systemd_cgroup_controller=1 systemd.unified_cgroup_hierarchy=0"
	_ = setRunningKargsWithCmdline(oldConfig, newConfig.Spec.KernelArguments, []byte(cmdline))
	diff, err = newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.True(t, diff.isEmpty())
}

func TestPrepUpdateFromClusterOnDiskDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		onDiskMCName string
		onDiskImage  string
		annotations  map[string]string
		verify       func(*testing.T, *updateFromCluster, error)
	}{
		// 1: onDisk matches what on the node, so we now have currentFromNode == currentOnDisk, desiredFromNode
		{
			onDiskMCName: "test1",
			annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     "test1",
				constants.DesiredMachineConfigAnnotationKey:     "test2",
				constants.MachineConfigDaemonStateAnnotationKey: "",
			},
			verify: func(t *testing.T, ufc *updateFromCluster, err error) {
				require.Nil(t, err)
				require.NotNil(t, ufc.currentConfig)
				require.NotNil(t, ufc.desiredConfig)
				require.Equal(t, ufc.currentConfig.GetName(), "test1")
				require.Equal(t, "", ufc.currentImage)
			},
		},
		// 2: onDisk matches what on the node and current == desired,
		//    so we now have currentFromNode == currentOnDisk == desiredFromNode
		//    so no update required
		{
			onDiskMCName: "test1",
			annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     "test1",
				constants.DesiredMachineConfigAnnotationKey:     "test1",
				constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
			},
			verify: func(t *testing.T, ufc *updateFromCluster, err error) {
				require.Nil(t, err)
				require.Nil(t, ufc)
			},
		},
		// 3: onDisk doesn't what on the node and current == desired,
		//    so we now have currentFromNode != currentOnDisk, desiredFromNode
		{
			onDiskMCName: "test3",
			annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     "test1",
				constants.DesiredMachineConfigAnnotationKey:     "test2",
				constants.MachineConfigDaemonStateAnnotationKey: "",
			},
			verify: func(t *testing.T, ufc *updateFromCluster, err error) {
				require.Nil(t, err)
				require.NotNil(t, ufc.currentConfig)
				require.NotNil(t, ufc.desiredConfig)
				require.Equal(t, "test3", ufc.currentConfig.GetName())
				require.Equal(t, ufc.desiredConfig.GetName(), "test2")
			},
		},
		// 4: onDisk matches what is on the node and images match, but MCD is not
		// done.
		{
			onDiskMCName: "test1",
			onDiskImage:  "image1",
			annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     "test1",
				constants.DesiredMachineConfigAnnotationKey:     "test1",
				constants.CurrentImageAnnotationKey:             "image1",
				constants.DesiredImageAnnotationKey:             "image1",
				constants.MachineConfigDaemonStateAnnotationKey: "",
			},
			verify: func(t *testing.T, ufc *updateFromCluster, err error) {
				require.Nil(t, err)
				require.NotNil(t, ufc.currentConfig)
				require.NotNil(t, ufc.desiredConfig)
				require.Equal(t, "image1", ufc.currentImage)
				require.Equal(t, "image1", ufc.desiredImage)
			},
		},
		// 5: onDisk matches what is on the node and images do not match, but MCD
		// is not done.
		{
			onDiskMCName: "test1",
			onDiskImage:  "image2",
			annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     "test1",
				constants.DesiredMachineConfigAnnotationKey:     "test1",
				constants.CurrentImageAnnotationKey:             "image1",
				constants.DesiredImageAnnotationKey:             "image1",
				constants.MachineConfigDaemonStateAnnotationKey: "",
			},
			verify: func(t *testing.T, ufc *updateFromCluster, err error) {
				require.Nil(t, err)
				require.NotNil(t, ufc.currentConfig)
				require.NotNil(t, ufc.desiredConfig)
				require.Equal(t, "image2", ufc.currentImage)
				require.Equal(t, "image1", ufc.desiredImage)
			},
		},
		// 6: onDisk matches what is on the node and images do not match, but MCD
		// is not done.
		{
			onDiskMCName: "test1",
			onDiskImage:  "image1",
			annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     "test1",
				constants.DesiredMachineConfigAnnotationKey:     "test1",
				constants.CurrentImageAnnotationKey:             "image1",
				constants.DesiredImageAnnotationKey:             "image1",
				constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
			},
			verify: func(t *testing.T, ufc *updateFromCluster, err error) {
				require.Nil(t, err)
				require.Nil(t, ufc)
			},
		},
		{
			onDiskMCName: "test1",
			onDiskImage:  "image3",
			annotations: map[string]string{
				constants.CurrentMachineConfigAnnotationKey:     "test1",
				constants.DesiredMachineConfigAnnotationKey:     "test1",
				constants.CurrentImageAnnotationKey:             "image2",
				constants.DesiredImageAnnotationKey:             "image1",
				constants.MachineConfigDaemonStateAnnotationKey: "",
			},
			verify: func(t *testing.T, ufc *updateFromCluster, err error) {
				require.Nil(t, err)
				require.NotNil(t, ufc.currentConfig)
				require.NotNil(t, ufc.desiredConfig)
				require.Equal(t, "image3", ufc.currentImage)
				require.Equal(t, "image1", ufc.desiredImage)
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run("", func(t *testing.T) {
			t.Parallel()

			onDiskMC := helpers.NewMachineConfig(test.onDiskMCName, nil, "", nil)
			currentConfigPath := filepath.Join(t.TempDir(), "currentconfig")
			currentConfigFile, err := os.Create(currentConfigPath)
			require.NoError(t, err)
			require.NoError(t, json.NewEncoder(currentConfigFile).Encode(onDiskMC))
			require.NoError(t, currentConfigFile.Close())

			currentImagePath := filepath.Join(t.TempDir(), "currentimage")

			if test.onDiskImage != "" {
				require.NoError(t, os.WriteFile(currentImagePath, []byte(test.onDiskImage), 0755))
			}

			f := newFixture(t)
			node := newNode(test.annotations)
			f.objects = append(f.objects, helpers.NewMachineConfig("test1", nil, "", nil))
			f.objects = append(f.objects, helpers.NewMachineConfig("test2", nil, "", nil))
			dn := f.newController()
			dn.node = node
			dn.currentConfigPath = currentConfigPath
			dn.currentImagePath = currentImagePath
			ufc, err := dn.prepUpdateFromCluster()
			test.verify(t, ufc, err)
		})
	}
}
