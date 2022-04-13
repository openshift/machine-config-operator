package node

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/uuid"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	apicfgv1 "github.com/openshift/api/config/v1"
	configv1 "github.com/openshift/api/config/v1"
	fakeconfigv1client "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1informer "github.com/openshift/client-go/config/informers/externalversions"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client          *fake.Clientset
	kubeclient      *k8sfake.Clientset
	schedulerClient *fakeconfigv1client.Clientset

	ccLister   []*mcfgv1.ControllerConfig
	mcLister   []*mcfgv1.MachineConfig
	mcpLister  []*mcfgv1.MachineConfigPool
	nodeLister []*corev1.Node

	kubeactions []core.Action
	actions     []core.Action

	kubeobjects      []runtime.Object
	objects          []runtime.Object
	schedulerObjects []runtime.Object
	schedulerLister  []*apicfgv1.Scheduler
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
	f.schedulerClient = fakeconfigv1client.NewSimpleClientset(f.schedulerObjects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	k8sI := kubeinformers.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())
	ci := configv1informer.NewSharedInformerFactory(f.schedulerClient, noResyncPeriodFunc())
	c := New(i.Machineconfiguration().V1().ControllerConfigs(), i.Machineconfiguration().V1().MachineConfigs(), i.Machineconfiguration().V1().MachineConfigPools(), k8sI.Core().V1().Nodes(),
		ci.Config().V1().Schedulers(), f.kubeclient, f.client)

	c.ccListerSynced = alwaysReady
	c.mcpListerSynced = alwaysReady
	c.nodeListerSynced = alwaysReady
	c.schedulerListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	stopCh := make(chan struct{})
	defer close(stopCh)
	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)
	k8sI.Start(stopCh)
	k8sI.WaitForCacheSync(stopCh)

	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mcpLister {
		i.Machineconfiguration().V1().MachineConfigPools().Informer().GetIndexer().Add(c)
	}

	for _, m := range f.nodeLister {
		k8sI.Core().V1().Nodes().Informer().GetIndexer().Add(m)
	}
	for _, c := range f.schedulerLister {
		ci.Config().V1().Schedulers().Informer().GetIndexer().Add(c)
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

func newControllerConfig(name string, topology configv1.TopologyMode) *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{daemonconsts.GeneratedByVersionAnnotationKey: version.Raw}, Name: name, UID: types.UID(utilrand.String(5))},
		Spec: mcfgv1.ControllerConfigSpec{
			Infra: &configv1.Infrastructure{
				Status: configv1.InfrastructureStatus{
					ControlPlaneTopology: topology,
				},
			},
		},
	}
}

// checkAction verifies that expected and actual actions are equal and both have
// same attached resources
func checkAction(expected, actual core.Action, t *testing.T) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
		return
	}

	if !assert.Equal(t, reflect.TypeOf(expected), reflect.TypeOf(actual)) {
		return
	}

	switch a := actual.(type) {
	case core.CreateAction:
		e, _ := expected.(core.CreateAction)
		expObject := filterLastTransitionTime(e.GetObject())
		object := filterLastTransitionTime(a.GetObject())
		assert.Equal(t, expObject, object)
	case core.UpdateAction:
		e, _ := expected.(core.UpdateAction)
		expObject := filterLastTransitionTime(e.GetObject())
		object := filterLastTransitionTime(a.GetObject())
		assert.Equal(t, expObject, object)
	case core.PatchAction:
		e, _ := expected.(core.PatchAction)
		expPatch := e.GetPatch()
		patch := a.GetPatch()
		assert.Equal(t, expPatch, patch)
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
				action.Matches("list", "controllerconfigs") ||
				action.Matches("watch", "controllerconfigs") ||
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

func TestGetNodesForPool(t *testing.T) {
	tests := []struct {
		pool  *mcfgv1.MachineConfigPool
		nodes []*corev1.Node

		expected int
		err      bool
	}{
		{
			pool:     helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			nodes:    newMixedNodeSet(3, map[string]string{"node-role": ""}, map[string]string{"node-role/worker": ""}),
			expected: 0,
			err:      false,
		},
		{
			pool:     helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			nodes:    newMixedNodeSet(2, map[string]string{"node-role/master": ""}, map[string]string{"node-role/worker": ""}),
			expected: 2,
			err:      false,
		},
		{
			pool:     helpers.NewMachineConfigPool("ïnfra", nil, helpers.InfraSelector, "v0"),
			nodes:    newMixedNodeSet(3, map[string]string{"node-role/master": ""}, map[string]string{"node-role/worker": "", "node-role/infra": ""}),
			expected: 3,
			err:      false,
		},
		{
			// Mixed cluster with both Windows and Linux worker nodes. Only Linux nodes should be managed by MCO
			pool:     helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
			nodes:    append(newMixedNodeSet(3, map[string]string{"node-role/master": ""}, map[string]string{"node-role/worker": "", "node-role/infra": ""}), newNodeWithLabels("windowsNode", map[string]string{osLabel: "windows"})),
			expected: 3,
			err:      false,
		},
		{
			// Single Windows node is the cluster, so shouldn't be managed by MCO
			pool:     helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
			nodes:    []*corev1.Node{newNodeWithLabels("windowsNode", map[string]string{osLabel: "windows"})},
			expected: 0,
			err:      false,
		},
	}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			f := newFixture(t)

			f.nodeLister = append(f.nodeLister, test.nodes...)
			f.mcpLister = append(f.mcpLister, test.pool)

			c := f.newController()

			got, err := c.getNodesForPool(test.pool)
			if err != nil && !test.err {
				t.Fatal("expected non-nil error")
			}
			assert.Equal(t, test.expected, len(got))
		})
	}
}

func TestGetPrimaryPoolForNode(t *testing.T) {
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
	}, {
		// MCP with Widows worker, it should not be assigned a pool
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
		},
		nodeLabel: map[string]string{"node-role/master": "", "node-role/worker": "", osLabel: "windows"},
		expected:  nil,
		err:       false,
	},
	}

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

			got, err := c.getPrimaryPoolForNode(node)
			if err != nil && !test.err {
				t.Fatal("expected non-nil error")
			}

			if got != nil {
				got.ObjectMeta.UID = ""
			}
			if test.expected != nil {
				test.expected.ObjectMeta.UID = ""
			}
			assert.Equal(t, test.expected, got)
		})
	}
}

func intStrPtr(obj intstr.IntOrString) *intstr.IntOrString { return &obj }

// newMixedNodeSet generates a slice of nodes for each role specified of length setlen.
func newMixedNodeSet(setlen int, roles ...map[string]string) []*corev1.Node {
	var nodeSet []*corev1.Node
	for _, role := range roles {
		nodes := newRoleNodeSet(setlen, role)
		nodeSet = append(nodeSet, nodes...)
	}
	return nodeSet
}

func newRoleNodeSet(len int, roles map[string]string) []*corev1.Node {
	nodes := []*corev1.Node{}
	for i := 0; i < len; i++ {
		nodes = append(nodes, newNodeWithLabels(fmt.Sprintf("node-%s", uuid.NewUUID()), roles))
	}
	return nodes
}

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
			maxUnavail: intStrPtr(intstr.FromString("60")),
			nodes:      newNodeSet(4),
			expected:   0,
			err:        true,
		}, {
			maxUnavail: intStrPtr(intstr.FromString("40 %")),
			nodes:      newNodeSet(4),
			expected:   0,
			err:        true,
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

			assert.Equal(t, test.expected, got)
		})
	}
}

func TestGetCandidateMachines(t *testing.T) {
	tests := []struct {
		nodes    []*corev1.Node
		progress int

		expected []string
		// otherCandidates is nodes that *could* be updated but we chose not to
		otherCandidates []string
		// capacity is the maximum number of nodes we could update
		capacity uint
	}{{
		//no progress
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionTrue),
		},
		expected:        nil,
		otherCandidates: nil,
		capacity:        1,
	}, {
		//no progress
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-2", "v1", "v1", corev1.ConditionFalse),
		},
		expected:        nil,
		otherCandidates: nil,
		capacity:        0,
	}, {
		//no progress because we have an unavailable node
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionFalse),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionTrue),
		},
		expected:        nil,
		otherCandidates: nil,
		capacity:        0,
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
		expected:        []string{"node-3"},
		otherCandidates: []string{"node-4"},
		capacity:        1,
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
		expected:        nil,
		otherCandidates: nil,
		capacity:        0,
	}, {
		//progress on old stuck node
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReadyAndDaemonState("node-1", "v0.1", "v0.2", corev1.ConditionTrue, daemonconsts.MachineConfigDaemonStateDegraded),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		expected:        []string{"node-1"},
		otherCandidates: []string{"node-2"},
		capacity:        1,
	}, {
		// Don't change a degraded node to same config, but also don't start another
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReadyAndDaemonState("node-1", "v1", "v1", corev1.ConditionTrue, daemonconsts.MachineConfigDaemonStateDegraded),
			newNodeWithReady("node-2", "v0", "v0", corev1.ConditionTrue),
		},
		expected:        nil,
		otherCandidates: nil,
		capacity:        0,
	}, {
		// Must be able to roll back
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReadyAndDaemonState("node-2", "v1", "v2", corev1.ConditionTrue, daemonconsts.MachineConfigDaemonStateDegraded),
			newNodeWithReady("node-3", "v1", "v1", corev1.ConditionTrue),
		},
		expected:        []string{"node-2"},
		otherCandidates: nil,
		capacity:        1,
	}, {
		// Validate we also don't affect nodes which haven't started work
		progress: 1,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReadyAndDaemonState("node-2", "v1", "v2", corev1.ConditionTrue, daemonconsts.MachineConfigDaemonStateDone),
			newNodeWithReady("node-3", "v1", "v1", corev1.ConditionTrue),
		},
		expected:        nil,
		otherCandidates: nil,
		capacity:        0,
	}, {
		// A test with more nodes in mixed order
		progress: 4,
		nodes: []*corev1.Node{
			newNodeWithReady("node-0", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-1", "v1", "v1", corev1.ConditionFalse),
			newNodeWithReady("node-2", "v0", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-3", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-4", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-5", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-6", "v0", "v0", corev1.ConditionTrue),
			newNodeWithReady("node-7", "v1", "v1", corev1.ConditionTrue),
			newNodeWithReady("node-8", "v1", "v1", corev1.ConditionTrue),
		},
		expected:        []string{"node-3", "node-4"},
		otherCandidates: []string{"node-5", "node-6"},
		capacity:        2,
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
			assert.Equal(t, test.expected, nodeNames)

			allCandidates, capacity := getAllCandidateMachines(pool, test.nodes, test.progress)
			assert.Equal(t, test.capacity, capacity)
			var otherCandidates []string
			for i, node := range allCandidates {
				if i < len(nodeNames) {
					assert.Equal(t, node.Name, nodeNames[i])
				} else {
					otherCandidates = append(otherCandidates, node.Name)
				}
			}
			assert.Equal(t, test.otherCandidates, otherCandidates)
		})
	}
}

func assertPatchesNode0ToV1(t *testing.T, actions []core.Action) {
	if !assert.Equal(t, 2, len(actions)) {
		t.Fatal("actions")
	}
	if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
		t.Fatal(actions)
	}
	if !actions[1].Matches("patch", "nodes") {
		t.Fatal(actions)
	}

	expected := []byte(`{"metadata":{"annotations":{"machineconfiguration.openshift.io/desiredConfig":"v1"}}}`)
	actual := actions[1].(core.PatchAction).GetPatch()
	assert.Equal(t, expected, actual)
}

func TestSetDesiredMachineConfigAnnotation(t *testing.T) {

	tests := []struct {
		node       *corev1.Node
		extraannos map[string]string

		verify func([]core.Action, *testing.T)
	}{{
		node: newNode("node-0", "", ""),
		verify: func(actions []core.Action, t *testing.T) {
			assertPatchesNode0ToV1(t, actions)
		},
	}, {
		node:       newNode("node-0", "", ""),
		extraannos: map[string]string{"test": "extra-annotation"},
		verify: func(actions []core.Action, t *testing.T) {
			assertPatchesNode0ToV1(t, actions)
		},
	}, {
		node: newNode("node-0", "v0", ""),
		verify: func(actions []core.Action, t *testing.T) {
			assertPatchesNode0ToV1(t, actions)
		},
	}, {
		node:       newNode("node-0", "v0", ""),
		extraannos: map[string]string{"test": "extra-annotation"},
		verify: func(actions []core.Action, t *testing.T) {
			assertPatchesNode0ToV1(t, actions)
		},
	}, {
		node: newNode("node-0", "v0", "v0"),
		verify: func(actions []core.Action, t *testing.T) {
			assertPatchesNode0ToV1(t, actions)
		},
	}, {
		node:       newNode("node-0", "v0", "v0"),
		extraannos: map[string]string{"test": "extra-annotation"},
		verify: func(actions []core.Action, t *testing.T) {
			assertPatchesNode0ToV1(t, actions)
		},
	}, {
		node: newNode("node-0", "v0", "v1"),
		verify: func(actions []core.Action, t *testing.T) {
			if !assert.Equal(t, 1, len(actions)) {
				return
			}

			if !actions[0].Matches("get", "nodes") || actions[0].(core.GetAction).GetName() != "node-0" {
				t.Fatal(actions)
			}
		},
	}, {
		node:       newNode("node-0", "v0", "v1"),
		extraannos: map[string]string{"test": "extra-annotation"},
		verify: func(actions []core.Action, t *testing.T) {
			if !assert.Equal(t, 1, len(actions)) {
				return
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
			if !assert.Nil(t, err) {
				return
			}

			test.verify(filterInformerActions(f.kubeclient.Actions()), t)
		})
	}
}

func TestShouldMakeProgress(t *testing.T) {
	// nodeWithDesiredConfigTaints is at desired config, so need to do a get on the nodeWithDesiredConfigTaints to check for the taint status
	nodeWithDesiredConfigTaints := newNodeWithLabel("nodeWithDesiredConfigTaints", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""})
	// Update nodeWithDesiredConfigTaints to have the needed taint, this should still have no effect
	nodeWithDesiredConfigTaints.Spec.Taints = []corev1.Taint{*constants.NodeUpdateInProgressTaint}
	// nodeWithNoDesiredConfigButTaints
	nodeWithNoDesiredConfigButTaints := newNodeWithLabel("nodeWithNoDesiredConfigButTaints", "v0", "v0", map[string]string{"node-role/worker": "", "node-role/infra": ""})
	nodeWithNoDesiredConfigButTaints.Spec.Taints = []corev1.Taint{*constants.NodeUpdateInProgressTaint}
	tests := []struct {
		description           string
		node                  *corev1.Node
		expectAnnotationPatch bool
		expectTaintsPatch     bool
		expectTaintsGet       bool
	}{
		{
			description:           "node at desired config no patch on annotation or taints",
			node:                  newNodeWithLabel("nodeAtDesiredConfig", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
			expectAnnotationPatch: false,
			expectTaintsPatch:     false,
		},
		{
			description:           "node not at desired config, patch on annotation and taints",
			node:                  newNodeWithLabel("nodeNeedingUpdates", "v0", "v0", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
			expectAnnotationPatch: true,
			expectTaintsPatch:     true,
		},
		{
			description:           "node at desired config, no patch on annotation or taints",
			node:                  nodeWithDesiredConfigTaints,
			expectAnnotationPatch: false,
			expectTaintsPatch:     false,
		},
		{
			description:           "node not at desired config, patch on annotation but not on taint",
			node:                  nodeWithNoDesiredConfigButTaints,
			expectAnnotationPatch: true,
			expectTaintsPatch:     false,
			expectTaintsGet:       true,
		},
	}
	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			f := newFixture(t)
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, configv1.TopologyMode(""))
			mcp := helpers.NewMachineConfigPool("test-cluster-infra", nil, helpers.InfraSelector, "v1")
			mcpWorker := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v1")
			mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))

			nodes := []*corev1.Node{
				// Existing node in the cluster at desired config
				newNodeWithLabel("existingNodeAtDesiredConfig", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
				test.node,
			}

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, mcp, mcpWorker)
			f.objects = append(f.objects, mcp, mcpWorker)
			f.nodeLister = append(f.nodeLister, test.node)
			for idx := range nodes {
				f.kubeobjects = append(f.kubeobjects, nodes[idx])
			}
			var oldData, newData, exppatch []byte
			var err error
			expNode := nodes[1].DeepCopy()
			if test.expectTaintsPatch {
				f.expectGetNodeAction(nodes[1])
				expNode.Spec.Taints = append(expNode.Spec.Taints, *constants.NodeUpdateInProgressTaint)
				oldData, err = json.Marshal(nodes[1])
				if err != nil {
					t.Fatal(err)
				}
				newData, err = json.Marshal(expNode)
				if err != nil {
					t.Fatal(err)
				}
				exppatch, err = strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
				if err != nil {
					t.Fatal(err)
				}
				f.expectPatchNodeAction(expNode, exppatch)
			}
			if test.expectTaintsGet {
				f.expectGetNodeAction(nodes[1])
			}
			// Patch the annotations on the node object now. Always doing it for nodes[1] as nodes[0] is already at
			// desired config
			if test.expectAnnotationPatch {
				f.expectGetNodeAction(nodes[1])
				oldData, err = json.Marshal(expNode)
				if err != nil {
					t.Fatal(err)
				}
				expNode.Annotations[daemonconsts.DesiredMachineConfigAnnotationKey] = "v1"
				newData, err = json.Marshal(expNode)
				if err != nil {
					t.Fatal(err)
				}
				exppatch, err = strategicpatch.CreateTwoWayMergePatch(oldData, newData, corev1.Node{})
				if err != nil {
					t.Fatal(err)
				}
				f.expectPatchNodeAction(expNode, exppatch)
			}
			expStatus := calculateStatus(mcp, nodes)
			expMcp := mcp.DeepCopy()
			expMcp.Status = expStatus
			f.expectUpdateMachineConfigPoolStatus(expMcp)
			f.run(getKey(mcp, t))
		})
	}
}

func TestEmptyCurrentMachineConfig(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig(ctrlcommon.ControllerConfigName, configv1.TopologyMode(""))
	mcp := helpers.NewMachineConfigPool("test-cluster-master", nil, helpers.MasterSelector, "")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))

	f.ccLister = append(f.ccLister, cc)
	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.run(getKey(mcp, t))
}

func TestPaused(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-infra", nil, helpers.InfraSelector, "v1")
	mcpWorker := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	mcp.Spec.Paused = true
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
		newNodeWithLabel("node-1", "v0", "v0", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
	}

	f.mcpLister = append(f.mcpLister, mcp, mcpWorker)
	f.objects = append(f.objects, mcp, mcpWorker)
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

func TestAlertOnPausedKubeletCA(t *testing.T) {
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v1")

	mcfiles := []ign3types.File{
		helpers.NewIgnFile("/etc/kubernetes/kubelet-ca.crt", TestKubeletCABundle),
		helpers.NewIgnFile("/etc/kubernetes/kubelet-ca.crt", "newcertificates"),
	}

	mcs := []*mcfgv1.MachineConfig{
		helpers.NewMachineConfig("rendered-worker-1", map[string]string{"node-role/worker": ""}, "dummy://", []ign3types.File{mcfiles[0]}),
		helpers.NewMachineConfig("rendered-worker-2", map[string]string{"node-role/worker": ""}, "dummy://1", []ign3types.File{mcfiles[1]}),
	}
	mcp.Spec.Configuration.Name = "rendered-worker-2"
	mcp.Status.Configuration.Name = "rendered-worker-1"

	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	mcp.Spec.Paused = true
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/worker": ""}),
		newNodeWithLabel("node-1", "v0", "v0", map[string]string{"node-role/worker": ""}),
	}

	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.nodeLister = append(f.nodeLister, nodes...)
	for idx := range nodes {
		f.kubeobjects = append(f.kubeobjects, nodes[idx])
	}
	f.mcLister = append(f.mcLister, mcs...)
	for idx := range mcs {
		f.objects = append(f.objects, mcs[idx])
	}

	expStatus := calculateStatus(mcp, nodes)
	expMcp := mcp.DeepCopy()
	expMcp.Status = expStatus
	f.expectUpdateMachineConfigPoolStatus(expMcp)
	f.run(getKey(mcp, t))

	metric := testutil.ToFloat64(ctrlcommon.MachineConfigControllerPausedPoolKubeletCA.WithLabelValues("worker"))

	// The metric should be set the expiry date of the *newest* kube-apiserver-to-kubelet-signer in UTC
	// which is 1670379908 -- the unix equivalent of Dec  7 02:25:08 2022 GMT
	if metric != 1670379908 {
		t.Errorf("Expected metric to be 1670379908 (Dec  7 02:25:08 2022 GMT), metric was %f", metric)
	}
}

func TestShouldUpdateStatusOnlyUpdated(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig(ctrlcommon.ControllerConfigName, configv1.TopologyMode(""))
	mcp := helpers.NewMachineConfigPool("test-cluster-infra", nil, helpers.InfraSelector, "v1")
	mcpWorker := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
		newNodeWithLabel("node-1", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
	}

	f.ccLister = append(f.ccLister, cc)
	f.mcpLister = append(f.mcpLister, mcp, mcpWorker)
	f.objects = append(f.objects, mcp, mcpWorker)
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
	cc := newControllerConfig(ctrlcommon.ControllerConfigName, configv1.TopologyMode(""))
	mcp := helpers.NewMachineConfigPool("test-cluster-infra", nil, helpers.InfraSelector, "v1")
	mcpWorker := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
		newNodeWithLabel("node-1", "v0", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
	}

	f.ccLister = append(f.ccLister, cc)
	f.mcpLister = append(f.mcpLister, mcp, mcpWorker)
	f.objects = append(f.objects, mcp, mcpWorker)
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
	cc := newControllerConfig(ctrlcommon.ControllerConfigName, configv1.TopologyMode(""))
	mcp := helpers.NewMachineConfigPool("test-cluster-infra", nil, helpers.InfraSelector, "v1")
	mcpWorker := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
		newNodeWithLabel("node-1", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
	}
	status := calculateStatus(mcp, nodes)
	mcp.Status = status

	f.ccLister = append(f.ccLister, cc)
	f.mcpLister = append(f.mcpLister, mcp, mcpWorker)
	f.objects = append(f.objects, mcp, mcpWorker)
	f.nodeLister = append(f.nodeLister, nodes...)
	for idx := range nodes {
		f.kubeobjects = append(f.kubeobjects, nodes[idx])
	}

	f.run(getKey(mcp, t))
}

func TestSortNodeList(t *testing.T) {
	old_node_nozone := newNode("node-4", "v1", "v1")
	old_node_nozone.CreationTimestamp = metav1.NewTime(time.Unix(0, 42))

	newer_node_nozone := newNode("node-5", "v1", "v1")
	newer_node_nozone.CreationTimestamp = metav1.NewTime(time.Unix(200, 30))

	newest_node_nozone := newNode("node-6", "v1", "v1")
	newest_node_nozone.CreationTimestamp = metav1.NewTime(time.Unix(900, 30))

	nodes := []*corev1.Node{
		newNodeWithLabel("node-3", "v1", "v1", map[string]string{"topology.kubernetes.io/zone": "ZZZ"}),
		newNodeWithLabel("node-1", "v1", "v1", map[string]string{"topology.kubernetes.io/zone": "RRR"}),
		newer_node_nozone,
		newNodeWithLabel("node-2", "v1", "v1", map[string]string{"topology.kubernetes.io/zone": "RRR"}),
		newest_node_nozone,
		old_node_nozone,
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"topology.kubernetes.io/zone": "QQQ"}),
	}

	sorted_nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"topology.kubernetes.io/zone": "QQQ"}),
		newNodeWithLabel("node-1", "v1", "v1", map[string]string{"topology.kubernetes.io/zone": "RRR"}),
		newNodeWithLabel("node-2", "v1", "v1", map[string]string{"topology.kubernetes.io/zone": "RRR"}),
		newNodeWithLabel("node-3", "v1", "v1", map[string]string{"topology.kubernetes.io/zone": "ZZZ"}),
		old_node_nozone,
		newer_node_nozone,
		newest_node_nozone,
	}

	output_nodes := sortNodeList(nodes)

	if !reflect.DeepEqual(sorted_nodes, output_nodes) {
		t.Fatalf("sorting failed. expected: %v got: %v", sorted_nodes, output_nodes)
	}
}

func TestControlPlaneTopology(t *testing.T) {
	f := newFixture(t)
	cc := newControllerConfig(ctrlcommon.ControllerConfigName, configv1.SingleReplicaTopologyMode)
	mcp := helpers.NewMachineConfigPool("test-cluster-infra", nil, helpers.InfraSelector, "v1")
	mcpWorker := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v1")
	mcp.Spec.MaxUnavailable = intStrPtr(intstr.FromInt(1))
	annotations := map[string]string{daemonconsts.ClusterControlPlaneTopologyAnnotationKey: "SingleReplica"}

	nodes := []*corev1.Node{
		newNodeWithLabel("node-0", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
		newNodeWithLabel("node-1", "v1", "v1", map[string]string{"node-role/worker": "", "node-role/infra": ""}),
	}

	for _, node := range nodes {
		addNodeAnnotations(node, annotations)
	}
	status := calculateStatus(mcp, nodes)
	mcp.Status = status

	f.ccLister = append(f.ccLister, cc)
	f.mcpLister = append(f.mcpLister, mcp, mcpWorker)
	f.objects = append(f.objects, mcp, mcpWorker)
	f.nodeLister = append(f.nodeLister, nodes...)
	for idx := range nodes {
		f.kubeobjects = append(f.kubeobjects, nodes[idx])
	}

	f.run(getKey(mcp, t))
}

// adds annotation to the node
func addNodeAnnotations(node *corev1.Node, annotations map[string]string) {
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	for k, v := range annotations {
		node.Annotations[k] = v
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

// TestKubeletCABundle is a fake kubelet CA bundle consisting of 4 certificates
// openshift-kube-apiserver-operator_kube-apiserver-to-kubelet-signer@0  Not After : Dec  5 02:25:08 2022 GMT
// openshift-kube-apiserver-operator_kube-apiserver-to-kubelet-signer@1  Not After : Dec  5 02:25:08 2022 GMT
// openshift-kube-apiserver-operator_kube-apiserver-to-kubelet-signer@2  Not After : Dec  7 02:25:08 2022 GMT
// kubelet-api-to-kubelet-signer                                         Not After : Dec 10 02:25:08 2021 GMT
// Originally this was generated as part of the test, but the crypto was expensive
var TestKubeletCABundle = `-----BEGIN CERTIFICATE-----
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
2ypMtC5cPw==
-----END CERTIFICATE-----
-----BEGIN CERTIFICATE-----
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
gFn9d+CPeQ==
-----END CERTIFICATE-----
-----BEGIN CERTIFICATE-----
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
atKsq3hqxg==
-----END CERTIFICATE-----
-----BEGIN CERTIFICATE-----
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
GjL/XaQj7QkXkVNKrd28YEpsslen5EBjwg==
-----END CERTIFICATE-----`
