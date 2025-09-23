package drain

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"

	"github.com/stretchr/testify/assert"
)

type fakeFeatureGateHandler struct{}

func (f *fakeFeatureGateHandler) Connect(ctx context.Context) error                 { return nil }
func (f *fakeFeatureGateHandler) Enabled(featureName configv1.FeatureGateName) bool { return false }
func (f *fakeFeatureGateHandler) Exists(featureName configv1.FeatureGateName) bool  { return false }
func (f *fakeFeatureGateHandler) KnownFeatures() []configv1.FeatureGateName         { return nil }

const (
	testNodeName      = "test-node"
	testPoolName      = "worker"
	testDrainState    = "drain-test-hash"
	testUncordonState = "uncordon-test-hash"
)

func createTestNode(name string, unschedulable bool) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: make(map[string]string),
		},
		Spec: corev1.NodeSpec{
			Unschedulable: unschedulable,
		},
	}
	return node
}

func createTestNodeWithAnnotations(name string, unschedulable bool, annotations map[string]string) *corev1.Node {
	node := createTestNode(name, unschedulable)
	maps.Copy(node.Annotations, annotations)
	return node
}

func createTestMCP(name string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"node-role.kubernetes.io/" + name: "",
				},
			},
		},
	}
}

func createTestController(nodes []*corev1.Node, mcps []*mcfgv1.MachineConfigPool) (*Controller, *k8sfake.Clientset, *fakemcfgclientset.Clientset) {
	kubeObjs := make([]runtime.Object, len(nodes))
	for i, node := range nodes {
		kubeObjs[i] = node
	}
	kubeClient := k8sfake.NewSimpleClientset(kubeObjs...)

	mcfgObjs := make([]runtime.Object, len(mcps))
	for i, mcp := range mcps {
		mcfgObjs[i] = mcp
	}
	mcfgClient := fakemcfgclientset.NewSimpleClientset(mcfgObjs...)

	kubeInformers := kubeinformers.NewSharedInformerFactory(kubeClient, 0)
	mcfgInformers := mcfginformers.NewSharedInformerFactory(mcfgClient, 0)

	nodeInformer := kubeInformers.Core().V1().Nodes()
	mcpInformer := mcfgInformers.Machineconfiguration().V1().MachineConfigPools()

	// Start informers to properly initialize them
	kubeInformers.Start(make(chan struct{}))
	mcfgInformers.Start(make(chan struct{}))

	// Add nodes to informer
	for _, node := range nodes {
		nodeInformer.Informer().GetIndexer().Add(node)
	}
	// Add MCPs to informer
	for _, mcp := range mcps {
		mcpInformer.Informer().GetIndexer().Add(mcp)
	}

	cfg := DefaultConfig()
	cfg.DrainTimeoutDuration = 10 * time.Minute
	cfg.DrainRequeueDelay = 1 * time.Minute
	cfg.DrainRequeueFailingDelay = 5 * time.Minute
	cfg.DrainRequeueFailingThreshold = 5 * time.Minute

	fgAccess := featuregates.NewHardcodedFeatureGateAccess([]configv1.FeatureGateName{"MachineConfigNodes", "PinnedImages"}, nil)

	ctrl := New(cfg, nodeInformer, mcpInformer, kubeClient, mcfgClient, fgAccess)

	// Initialize ongoing drains map for testing
	ctrl.ongoingDrains = make(map[string]time.Time)

	return ctrl, kubeClient, mcfgClient
}

func createDrainTestNode(nodeName string, unschedulable bool, desiredState, lastAppliedState string) *corev1.Node {
	node := createTestNodeWithAnnotations(nodeName, unschedulable, map[string]string{
		daemonconsts.DesiredDrainerAnnotationKey:     desiredState,
		daemonconsts.LastAppliedDrainerAnnotationKey: lastAppliedState,
	})
	node.Labels = map[string]string{
		"node-role.kubernetes.io/" + testPoolName: "",
	}
	return node
}

func setupControllerAndSync(node *corev1.Node, ongoingDrains map[string]time.Time) (*Controller, *k8sfake.Clientset, error) {
	ctrl, kubeClient, _ := createTestController([]*corev1.Node{node}, []*mcfgv1.MachineConfigPool{createTestMCP(testPoolName)})

	if ongoingDrains != nil {
		ctrl.ongoingDrains = ongoingDrains
	}

	err := ctrl.syncNode(testNodeName)
	return ctrl, kubeClient, err
}

func verifyDrainPatches(t *testing.T, kubeClient *k8sfake.Clientset, expectedUnschedulable bool, expectedAnnotationValue string) {
	// Collect all patch actions
	patchActions := []core.PatchAction{}
	for _, action := range kubeClient.Actions() {
		if patchAction, ok := action.(core.PatchAction); ok {
			patchActions = append(patchActions, patchAction)
		}
	}

	// Verify exactly 2 patch operations occurred
	assert.Len(t, patchActions, 2, "should have made exactly two patch requests")

	// Verify the first patch sets the correct scheduling state
	firstPatchBytes := patchActions[0].GetPatch()
	var firstPatch map[string]any
	err := json.Unmarshal(firstPatchBytes, &firstPatch)
	assert.NoError(t, err, "unmarshalling first patch failed")
	if spec, ok := firstPatch["spec"].(map[string]any); ok {
		if unschedulable, ok := spec["unschedulable"].(bool); ok {
			assert.Equal(t, expectedUnschedulable, unschedulable, "first patch should set node schedulable state correctly")
		}
	}

	// Verify the second patch sets the LastAppliedDrainerAnnotationKey annotation
	secondPatchBytes := patchActions[1].GetPatch()
	var secondPatch map[string]any
	err = json.Unmarshal(secondPatchBytes, &secondPatch)
	assert.NoError(t, err, "unmarshalling second patch failed")
	if metadata, ok := secondPatch["metadata"].(map[string]any); ok {
		if annotations, ok := metadata["annotations"].(map[string]any); ok {
			if lastApplied, ok := annotations[daemonconsts.LastAppliedDrainerAnnotationKey].(string); ok {
				assert.Equal(t, expectedAnnotationValue, lastApplied, "LastAppliedDrainerAnnotationKey should be set correctly")
			}
		}
	}
}

func TestSyncNode(t *testing.T) {

	t.Run("uncordon requested", func(t *testing.T) {
		node := createDrainTestNode(testNodeName, true, testUncordonState, "")
		_, kubeClient, err := setupControllerAndSync(node, nil)
		assert.NoError(t, err, "syncNode should not error for uncordon action")

		// Verify patch operations: uncordon (schedulable=false) + completion annotation
		verifyDrainPatches(t, kubeClient, false, testUncordonState)

	})

	t.Run("drain requested", func(t *testing.T) {
		node := createDrainTestNode(testNodeName, false, testDrainState, "")
		_, kubeClient, err := setupControllerAndSync(node, nil)
		assert.NoError(t, err, "syncNode should not error for drain action")

		// Verify patch operations: cordon (unschedulable=true) + completion annotation
		verifyDrainPatches(t, kubeClient, true, testDrainState)
	})

	t.Run("re-cordon required", func(t *testing.T) {
		node := createDrainTestNode(testNodeName, false, testDrainState, "")
		// Simulate ongoing drain (but node is not cordoned - external uncordon)
		ongoingDrains := map[string]time.Time{
			testNodeName: time.Now().Add(-5 * time.Minute),
		}
		_, kubeClient, err := setupControllerAndSync(node, ongoingDrains)
		assert.NoError(t, err, "syncNode should not error for re-cordon action")

		// Verify patch operations: re-cordon (unschedulable=true) + completion annotation
		verifyDrainPatches(t, kubeClient, true, testDrainState)
	})
}
