package daemon

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	"github.com/stretchr/testify/require"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
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

func TestOverwrittenFile(t *testing.T) {
	fi, err := os.Lstat("fixtures/test1.txt")
	if err != nil {
		t.Errorf("Could not Lstat file: %v", err)
	}
	fileMode := int(fi.Mode().Perm())

	// validate single file
	files := []igntypes.File{
		{
			Node: igntypes.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Contents: igntypes.FileContents{
					Source: dataurl.EncodeBytes([]byte("hello world\n")),
				},
				Mode: &fileMode,
			},
		},
	}

	if status := checkFiles(files); !status {
		t.Errorf("Invalid files")
	}

	// validate overwritten file
	files = []igntypes.File{
		{
			Node: igntypes.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Contents: igntypes.FileContents{
					Source: dataurl.EncodeBytes([]byte("hello\n")),
				},
				Mode: &fileMode,
			},
		},
		{
			Node: igntypes.Node{
				Path: "fixtures/test1.txt",
			},
			FileEmbedded1: igntypes.FileEmbedded1{
				Contents: igntypes.FileContents{
					Source: dataurl.EncodeBytes([]byte("hello world\n")),
				},
				Mode: &fileMode,
			},
		},
	}

	if status := checkFiles(files); !status {
		t.Errorf("Validating an overwritten file failed")
	}
}

func TestCompareOSImageURL(t *testing.T) {
	refA := "registry.example.com/foo/bar@sha256:0743a3cc3bcf3b4aabb814500c2739f84cb085ff4e7ec7996aef7977c4c19c7f"
	refB := "registry.example.com/foo/baz@sha256:0743a3cc3bcf3b4aabb814500c2739f84cb085ff4e7ec7996aef7977c4c19c7f"
	refC := "registry.example.com/foo/bar@sha256:2a76681fd15bfc06fa4aa0ff6913ba17527e075417fc92ea29f6bcc2afca24ff"
	m, err := compareOSImageURL(refA, refA)
	if !m {
		t.Fatalf("Expected refA ident")
	}
	m, err = compareOSImageURL(refA, refB)
	if !m {
		t.Fatalf("Expected refA = refB")
	}
	m, err = compareOSImageURL(refA, refC)
	if m {
		t.Fatalf("Expected refA != refC")
	}
	m, err = compareOSImageURL(refA, "registry.example.com/foo/bar")
	if m || err == nil {
		t.Fatalf("Expected err")
	}
}

type fixture struct {
	t *testing.T

	client     *fake.Clientset
	kubeclient *k8sfake.Clientset

	mcLister   []*mcfgv1.MachineConfig
	nodeLister []*corev1.Node

	kubeactions []core.Action
	actions     []core.Action

	objects     []runtime.Object
	kubeobjects []runtime.Object
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

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	k8sI := kubeinformers.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())

	d, err := New(NewNodeUpdaterClient(), nil)
	if err != nil {
		f.t.Fatalf("can't bring up daemon: %v", err)
	}
	d.ClusterConnect("node_name_test",
		f.kubeclient,
		i.Machineconfiguration().V1().MachineConfigs(),
		k8sI.Core().V1().Nodes(),
		false,
		"",
	)

	d.mcListerSynced = alwaysReady
	d.nodeListerSynced = alwaysReady
	d.recorder = &record.FakeRecorder{}

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

func TestPrepUpdateFromClusterOnDiskDrift(t *testing.T) {
	tmpCurrentConfig, err := ioutil.TempFile("", "currentconfig")
	require.Nil(t, err)
	defer os.Remove(tmpCurrentConfig.Name())

	// 1: onDisk matches what on the node, so we now have currentFromNode == currentOnDisk, desiredFromNode
	f := newFixture(t)
	onDiskMC := helpers.NewMachineConfig("test1", nil, "", nil)
	annotations := map[string]string{
		constants.CurrentMachineConfigAnnotationKey:     "test1",
		constants.DesiredMachineConfigAnnotationKey:     "test2",
		constants.MachineConfigDaemonStateAnnotationKey: "",
	}
	node := newNode(annotations)
	f.objects = append(f.objects, helpers.NewMachineConfig("test1", nil, "", nil))
	f.objects = append(f.objects, helpers.NewMachineConfig("test2", nil, "", nil))
	tmpCurrentConfig.Truncate(0)
	tmpCurrentConfig.Seek(0, 0)
	require.Nil(t, json.NewEncoder(tmpCurrentConfig).Encode(onDiskMC))
	dn := f.newController()
	dn.node = node
	dn.currentConfigPath = tmpCurrentConfig.Name()
	current, desired, err := dn.prepUpdateFromCluster()
	require.Nil(t, err)
	require.NotNil(t, current)
	require.NotNil(t, desired)
	require.Equal(t, current.GetName(), onDiskMC.GetName())

	// 2: onDisk matches what on the node and current == desired,
	//    so we now have currentFromNode == currentOnDisk == desiredFromNode
	//    so no update required
	f = newFixture(t)
	onDiskMC = helpers.NewMachineConfig("test1", nil, "", nil)
	annotations = map[string]string{
		constants.CurrentMachineConfigAnnotationKey:     "test1",
		constants.DesiredMachineConfigAnnotationKey:     "test1",
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
	}
	node = newNode(annotations)
	f.objects = append(f.objects, helpers.NewMachineConfig("test1", nil, "", nil))
	f.objects = append(f.objects, helpers.NewMachineConfig("test2", nil, "", nil))
	tmpCurrentConfig.Truncate(0)
	tmpCurrentConfig.Seek(0, 0)
	require.Nil(t, json.NewEncoder(tmpCurrentConfig).Encode(onDiskMC))
	dn = f.newController()
	dn.node = node
	dn.currentConfigPath = tmpCurrentConfig.Name()
	current, desired, err = dn.prepUpdateFromCluster()
	require.Nil(t, err)
	require.Nil(t, current)
	require.Nil(t, desired)

	// 3: onDisk doesn't what on the node and current == desired,
	//    so we now have currentFromNode != currentOnDisk, desiredFromNode
	f = newFixture(t)
	onDiskMC = helpers.NewMachineConfig("test3", nil, "", nil)
	annotations = map[string]string{
		constants.CurrentMachineConfigAnnotationKey:     "test1",
		constants.DesiredMachineConfigAnnotationKey:     "test2",
		constants.MachineConfigDaemonStateAnnotationKey: "",
	}
	node = newNode(annotations)
	f.objects = append(f.objects, helpers.NewMachineConfig("test1", nil, "", nil))
	f.objects = append(f.objects, helpers.NewMachineConfig("test2", nil, "", nil))
	tmpCurrentConfig.Truncate(0)
	tmpCurrentConfig.Seek(0, 0)
	require.Nil(t, json.NewEncoder(tmpCurrentConfig).Encode(onDiskMC))
	dn = f.newController()
	dn.node = node
	dn.currentConfigPath = tmpCurrentConfig.Name()
	current, desired, err = dn.prepUpdateFromCluster()
	require.Nil(t, err)
	require.NotNil(t, current)
	require.NotNil(t, desired)
	require.Equal(t, onDiskMC.GetName(), current.GetName())
	require.Equal(t, desired.GetName(), "test2")
}
