package node

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/machine-config-operator/test/helpers"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client     *fake.Clientset
	kubeclient *k8sfake.Clientset

	mcpLister  []*mcfgv1.MachineConfigPool
	mcLister   []*mcfgv1.MachineConfig
	nodeLister []*corev1.Node

	kubeactions []core.Action
	actions     []core.Action

	kubeobjects []runtime.Object
	objects     []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.kubeobjects = []runtime.Object{}
	return f
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	k8sI := kubeinformers.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())
	c := New(i.Machineconfiguration().V1().MachineConfigPools(), i.Machineconfiguration().V1().MachineConfigs(),
		k8sI.Core().V1().Nodes(), f.kubeclient, f.client)

	c.mcpListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.nodeListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	stopCh := make(chan struct{})
	defer close(stopCh)
	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)
	k8sI.Start(stopCh)
	k8sI.WaitForCacheSync(stopCh)

	for _, c := range f.mcpLister {
		i.Machineconfiguration().V1().MachineConfigPools().Informer().GetIndexer().Add(c)
	}

	for _, c := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(c)
	}

	for _, m := range f.nodeLister {
		k8sI.Core().V1().Nodes().Informer().GetIndexer().Add(m)
	}

	return c
}

func (f *fixture) run(pool string) {
	f.runController(pool, false)
}

func (f *fixture) runExpectError(pool string) {
	f.runController(pool, true)
}

func (f *fixture) runController(pool string, expectError bool) {
	c := f.newController()

	err := c.syncHandler(pool)
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
			(action.Matches("list", "machineconfigpools") ||
				action.Matches("watch", "machineconfigpools") ||
				action.Matches("list", "machineconfigs") ||
				action.Matches("watch", "machineconfigs") ||
				action.Matches("list", "nodes") ||
				action.Matches("watch", "nodes")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func (f *fixture) expectUpdateMachineConfigPoolStatus(pool *mcfgv1.MachineConfigPool) {
	f.actions = append(f.actions, core.NewRootUpdateSubresourceAction(schema.GroupVersionResource{Resource: "machineconfigpools"}, "status", pool))
}

func (f *fixture) expectGetNodeAction(node *corev1.Node) {
	f.kubeactions = append(f.kubeactions, core.NewGetAction(schema.GroupVersionResource{Resource: "nodes"}, node.Namespace, node.Name))
}

func (f *fixture) expectPatchNodeAction(node *corev1.Node, patch []byte) {
	f.kubeactions = append(f.kubeactions, core.NewPatchAction(schema.GroupVersionResource{Resource: "nodes"}, node.Namespace, node.Name, types.MergePatchType, patch))
}

func TestGetPoolForNode(t *testing.T) {
	tests := []struct {
		pools     []*mcfgv1.MachineConfigPool
		nodeLabel map[string]string

		expected *mcfgv1.MachineConfigPool
		err      bool
	}{{
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
		},
		nodeLabel: map[string]string{"node-role": ""},

		expected: nil,
		err:      false,
	}, {
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
		},
		nodeLabel: map[string]string{"node-role/master": ""},

		expected: helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
		err:      false,
	}, {
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
		},
		nodeLabel: map[string]string{"node-role/master": "", "node-role/worker": ""},

		expected: helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
		err:      false,
	}, {
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
			helpers.NewMachineConfigPool("infra", nil, helpers.InfraSelector, "v0"),
		},
		nodeLabel: map[string]string{"node-role/worker": "", "node-role/infra": ""},

		expected: helpers.NewMachineConfigPool("infra", nil, helpers.InfraSelector, "v0"),
		err:      false,
	}, {
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
			helpers.NewMachineConfigPool("infra", nil, helpers.InfraSelector, "v0"),
		},
		nodeLabel: map[string]string{"node-role/master": "", "node-role/infra": ""},

		expected: nil,
		err:      true,
	}, {
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
			helpers.NewMachineConfigPool("infra", nil, helpers.InfraSelector, "v0"),
			helpers.NewMachineConfigPool("infra2", nil, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/infra2", ""), "v0"),
		},
		nodeLabel: map[string]string{"node-role/infra": "", "node-role/infra2": ""},

		expected: nil,
		err:      true,
	}, {

		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("test-cluster-pool-1", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("test-cluster-pool-2", nil, helpers.MasterSelector, "v0"),
		},
		nodeLabel: map[string]string{"node-role": "master"},

		expected: nil,
		err:      true,
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			f := newFixture(t)
			node := newNode("node-0", "v0", "v0")
			node.Labels = test.nodeLabel

			f.nodeLister = append(f.nodeLister, node)
			f.kubeobjects = append(f.kubeobjects, node)
			f.mcpLister = append(f.mcpLister, test.pools...)
			for idx := range test.pools {
				f.objects = append(f.objects, test.pools[idx])
			}

			c := f.newController()

			got, err := c.getPoolForNode(node)
			if err != nil && !test.err {
				t.Fatal("expected non-nil error")
			}

			if got != nil {
				got.ObjectMeta.UID = ""
			}
			if test.expected != nil {
				test.expected.ObjectMeta.UID = ""
			}
			if !reflect.DeepEqual(got, test.expected) {
				t.Fatalf("mismatch: got: %v want: %v", got, test.expected)
			}
		})
	}
}

func intStrPtr(obj intstr.IntOrString) *intstr.IntOrString { return &obj }

func newNodeSet(len int) []*corev1.Node {
	nodes := []*corev1.Node{}
	for i := 0; i < len; i++ {
		nodes = append(nodes, newNode(fmt.Sprintf("node-%d", i), "", ""))
	}
	return nodes
}

func TestMaxUnavailable(t *testing.T) {
	tests := []struct {
		poolName   string
		maxUnavail *intstr.IntOrString
		nodes      []*corev1.Node
		expected   int
		err        bool
	}{
		{
			maxUnavail: nil,
			nodes:      newNodeSet(4),
			expected:   1,
			err:        false,
		}, {
			maxUnavail: intStrPtr(intstr.FromInt(2)),
			nodes:      newNodeSet(4),
			expected:   2,
			err:        false,
		}, {
			maxUnavail: intStrPtr(intstr.FromInt(0)),
			nodes:      newNodeSet(4),
			expected:   1,
			err:        false,
		}, {
			maxUnavail: intStrPtr(intstr.FromString("50%")),
			nodes:      newNodeSet(4),
			expected:   2,
			err:        false,
		}, {
			maxUnavail: intStrPtr(intstr.FromString("60%")),
			nodes:      newNodeSet(4),
			expected:   2,
			err:        false,
		}, {
			maxUnavail: intStrPtr(intstr.FromString("50 percent")),
			nodes:      newNodeSet(4),
			expected:   0,
			err:        true,
		}, {
			poolName:   "master",
			maxUnavail: intStrPtr(intstr.FromInt(1)),
			nodes:      newNodeSet(5),
			expected:   1,
			err:        false,
		}, {
			poolName:   "master",
			maxUnavail: intStrPtr(intstr.FromInt(2)),
			nodes:      newNodeSet(3),
			expected:   1,
			err:        false,
		}, {
			poolName:   "master",
			maxUnavail: intStrPtr(intstr.FromInt(2)),
			nodes:      newNodeSet(5),
			expected:   2,
			err:        false,
		},
		{
			poolName:   "master",
			maxUnavail: intStrPtr(intstr.FromInt(4)),
			nodes:      newNodeSet(7),
			expected:   3,
			err:        false,
		},
	}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			pool := &mcfgv1.MachineConfigPool{
				ObjectMeta: metav1.ObjectMeta{
					Name: test.poolName,
				},
				Spec: mcfgv1.MachineConfigPoolSpec{
					MaxUnavailable: test.maxUnavail,
				},
			}
			got, err := maxUnavailable(pool, test.nodes)
			if err != nil && !test.err {
				t.Fatal("expected non-nil error")
			}

			if got != test.expected {
				t.Fatalf("mismatch maxUnavailable: got %d want: %d", got, test.expected)
			}
		})
	}
}

func TestGetCandidateMachines(t *testing.T) {
	tests := []struct {
		nodes    []*corev1.Node
		progress int

		expected []string
	}{{
		//no progress
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionTrue),
		},
		expected: nil,
	}, {
		//no progress
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionFalse),
		},
		expected: nil,
	}, {
		//no progress because we have an unavailable node
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionFalse),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionTrue),
		},
		expected: nil,
	}, {
		// node-2 is going to change config, so we can only progress one more
		progress: 3,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionFalse),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-3", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-4", "v0", "v0", corev1.ConditionTrue),
		},
		expected: []string{"node-3"},
	}, {
		// We have a node working, don't start anything else
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-3", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-4", "v0", "v0", corev1.ConditionTrue),
		},
		expected: nil,
	}, {
		//progress on old stuck node
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReadyAndDaemonState("node-1", "v0.1", "v0.2", corev1.ConditionTrue, daemonconsts.MachineConfigDaemonStateDegraded),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		expected: []string{"node-1"},
	}, {
		// Don't change a degraded node to same config, but also don't start another
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReadyAndDaemonState("node-1", "v1", "v1", corev1.ConditionTrue, daemonconsts.MachineConfigDaemonStateDegraded),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		expected: nil,
	}, {
		// Must be able to roll back
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReadyAndDaemonState("node-2", "v1", "v2", corev1.ConditionTrue, daemonconsts.MachineConfigDaemonStateDegraded),
			newNodeWithReady("node-3", "v1", "v1", corev1.ConditionTrue),
		},
		expected: []string{"node-2"},
	}, {
		// Validate we also don't affect nodes which haven't started work
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReadyAndDaemonState("node-2", "v1", "v2", corev1.ConditionTrue, daemonconsts.MachineConfigDaemonStateDone),
			newNodeWithReady("node-3", "v1", "v1", corev1.ConditionTrue),
		},
		expected: nil,
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			pool := &mcfgv1.MachineConfigPool{
				Spec: mcfgv1.MachineConfigPoolSpec{
					Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{ObjectReference: corev1.ObjectReference{Name: "v1"}},
				},
			}

			got := getCandidateMachines(pool, test.nodes, test.progress)
			var nodeNames []string
			for _, node := range got {
				nodeNames = append(nodeNames, node.Name)
			}
			if !reflect.DeepEqual(nodeNames, test.expected) {
				t.Fatalf("mismatch: got %v want: %v", nodeNames, test.expected)
			}
		})
	}
}

func TestSetDesiredMachineConfigAnnotation(t *testing.T) {
	tests := []struct {
		node       *corev1.Node
		extraannos map[string]string

		verify func([]core.Action, *testing.T)
	}{{
		node: newNode("node-0", "", ""),
		verify: func(actions []core.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(actions)
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}

			if !actions[1].Matches("patch", "nodes") {
				t.Fatal(actions)
			}

			expected := []byte(`{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"v1"}}}`)
			actual := actions[1].(core.PatchAction).GetPatch()

			if !reflect.DeepEqual(expected, actual) {
				t.Fatal(diff.ObjectDiff(string(expected), string(actual)))
			}
		},
	}, {
		node:       newNode("node-0", "", ""),
		extraannos: map[string]string{"test": "extra-annotation"},
		verify: func(actions []core.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(actions)
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}

			if !actions[1].Matches("patch", "nodes") {
				t.Fatal(actions)
			}

			expected := []byte(`{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"v1"}}}`)
			actual := actions[1].(core.PatchAction).GetPatch()

			if !reflect.DeepEqual(expected, actual) {
				t.Fatal(diff.ObjectDiff(string(expected), string(actual)))
			}
		},
	}, {
		node: newNode("node-0", "v0", ""),
		verify: func(actions []core.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(actions)
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}

			if !actions[1].Matches("patch", "nodes") {
				t.Fatal(actions)
			}

			expected := []byte(`{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"v1"}}}`)
			actual := actions[1].(core.PatchAction).GetPatch()

			if !reflect.DeepEqual(expected, actual) {
				t.Fatal(diff.ObjectDiff(string(expected), string(actual)))
			}
		},
	}, {
		node:       newNode("node-0", "v0", ""),
		extraannos: map[string]string{"test": "extra-annotation"},
		verify: func(actions []core.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(actions)
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}

			if !actions[1].Matches("patch", "nodes") {
				t.Fatal(actions)
			}

			expected := []byte(`{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"v1"}}}`)
			actual := actions[1].(core.PatchAction).GetPatch()

			if !reflect.DeepEqual(expected, actual) {
				t.Fatal(diff.ObjectDiff(string(expected), string(actual)))
			}
		},
	}, {
		node: newNode("node-0", "v0", "v0"),
		verify: func(actions []core.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(actions)
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}

			if !actions[1].Matches("patch", "nodes") {
				t.Fatal(actions)
			}

			expected := []byte(`{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"v1"}}}`)
			actual := actions[1].(core.PatchAction).GetPatch()

			if !reflect.DeepEqual(expected, actual) {
				t.Fatal(diff.ObjectDiff(string(expected), string(actual)))
			}
		},
	}, {
		node:       newNode("node-0", "v0", "v0"),
		extraannos: map[string]string{"test": "extra-annotation"},
		verify: func(actions []core.Action, t *testing.T) {
			if len(actions) != 2 {
				t.Fatal(actions)
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}

			if !actions[1].Matches("patch", "nodes") {
				t.Fatal(actions)
			}

			expected := []byte(`{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"v1"}}}`)
			actual := actions[1].(core.PatchAction).GetPatch()

			if !reflect.DeepEqual(expected, actual) {
				t.Fatal(diff.ObjectDiff(string(expected), string(actual)))
			}
		},
	}, {
		node: newNode("node-0", "v0", "v1"),
		verify: func(actions []core.Action, t *testing.T) {
			if len(actions) != 1 {
				t.Fatal(actions)
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}
		},
	}, {
		node:       newNode("node-0", "v0", "v1"),
		extraannos: map[string]string{"test": "extra-annotation"},
		verify: func(actions []core.Action, t *testing.T) {
			if len(actions) != 1 {
				t.Fatal(actions)
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}
		},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			f := newFixture(t)
			if test.extraannos != nil {
				if test.node.Annotations == nil {
					test.node.Annotations = map[string]string{}
				}
				for k, v := range test.extraannos {
					test.node.Annotations[k] = v
				}
			}
			f.nodeLister = append(f.nodeLister, test.node)
			f.kubeobjects = append(f.kubeobjects, test.node)

			c := f.newController()

			err := c.setDesiredMachineConfigAnnotation(test.node.Name, "v1")
			if err != nil {
				t.Fatalf("expected non-nil error: %v", err)
			}

			test.verify(filterInformerActions(f.kubeclient.Actions()), t)
		})
	}
}

func TestShouldMakeProgress(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", nil, helpers.MasterSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)

	mc := helpers.NewMachineConfig("v1", map[string]string{"node-role": "master"}, "", nil)
	f.mcLister = append(f.mcLister, mc)
	f.objects = append(f.objects, mc)

	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/master": ""}),
		newNodeWithLabel("node-1", "v0", "v0", map[string]string{"node-role/master": ""}),
	}
	f.nodeLister = append(f.nodeLister, nodes...)
	for idx := range nodes {
		f.kubeobjects = append(f.kubeobjects, nodes[idx])
	}

	f.expectGetNodeAction(nodes[1])
	expNode := nodes[1].DeepCopy()
	expNode.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] = "v1"
	oldData, err := json.Marshal(nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	newData, err := json.Marshal(expNode)
	if err != nil {
		t.Fatal(err)
	}
	exppatch, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
	if err != nil {
		t.Fatal(err)
	}
	f.expectPatchNodeAction(expNode, exppatch)
	expStatus := calculateStatus(mcp, nodes)
	expMcp := mcp.DeepCopy()
	expMcp.Status = expStatus
	f.expectUpdateMachineConfigPoolStatus(expMcp)

	f.run(getKey(mcp, t))
}

func TestEmptyCurrentMachineConfig(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", nil, helpers.MasterSelector, "")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.run(getKey(mcp, t))
}

func TestPaused(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", nil, helpers.MasterSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	mcp.Spec.Paused = true
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/master": ""}),
		newNodeWithLabel("node-1", "v0", "v0", map[string]string{"node-role/master": ""}),
	}

	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.nodeLister = append(f.nodeLister, nodes...)
	for idx := range nodes {
		f.kubeobjects = append(f.kubeobjects, nodes[idx])
	}

	expStatus := calculateStatus(mcp, nodes)
	expMcp := mcp.DeepCopy()
	expMcp.Status = expStatus
	f.expectUpdateMachineConfigPoolStatus(expMcp)

	f.run(getKey(mcp, t))
}

func TestShouldUpdateStatusOnlyUpdated(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", nil, helpers.MasterSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/master": ""}),
		newNodeWithLabel("node-1", "v1", "v1", map[string]string{"node-role/master": ""}),
	}

	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.nodeLister = append(f.nodeLister, nodes...)
	for idx := range nodes {
		f.kubeobjects = append(f.kubeobjects, nodes[idx])
	}

	expStatus := calculateStatus(mcp, nodes)
	expMcp := mcp.DeepCopy()
	expMcp.Status = expStatus
	f.expectUpdateMachineConfigPoolStatus(expMcp)

	f.run(getKey(mcp, t))
}

func TestShouldUpdateStatusOnlyNoProgress(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", nil, helpers.MasterSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/master": ""}),
		newNodeWithLabel("node-1", "v0", "v1", map[string]string{"node-role/master": ""}),
	}

	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.nodeLister = append(f.nodeLister, nodes...)
	for idx := range nodes {
		f.kubeobjects = append(f.kubeobjects, nodes[idx])
	}

	expStatus := calculateStatus(mcp, nodes)
	expMcp := mcp.DeepCopy()
	expMcp.Status = expStatus
	f.expectUpdateMachineConfigPoolStatus(expMcp)

	f.run(getKey(mcp, t))
}

func TestShouldDoNothing(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", nil, helpers.MasterSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/master": ""}),
		newNodeWithLabel("node-1", "v1", "v1", map[string]string{"node-role/master": ""}),
	}
	status := calculateStatus(mcp, nodes)
	mcp.Status = status

	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.nodeLister = append(f.nodeLister, nodes...)
	for idx := range nodes {
		f.kubeobjects = append(f.kubeobjects, nodes[idx])
	}

	f.run(getKey(mcp, t))
}

func getKey(config *mcfgv1.MachineConfigPool, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(config)
	if err != nil {
		t.Errorf("Unexpected error getting key for config %v: %v", config.Name, err)
		return ""
	}
	return key
}

func filterLastTransitionTime(obj runtime.Object) runtime.Object {
	obj = obj.DeepCopyObject()
	o, ok := obj.(*mcfgv1.MachineConfigPool)
	if !ok {
		return obj
	}

	for idx := range o.Status.Conditions {
		o.Status.Conditions[idx].LastTransitionTime = metav1.Time{}
	}
	return o
}
