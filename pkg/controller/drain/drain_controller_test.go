package drain

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/client-go/informers"

	fakeclientmachineconfigv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	mcfgv1informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/envtest"
	"github.com/openshift/machine-config-operator/test/helpers"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8sTesting "k8s.io/client-go/testing"
	"k8s.io/kubectl/pkg/drain"
)

type drainControllerTestCase struct {
	Name             string
	SimulateEviction bool
	TestFunc         func(ctx context.Context, t *testing.T, clientSet *fakecorev1client.Clientset)
}

func TestDrainController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go ctrlcommon.StartMetricsListener(ctrlcommon.DefaultBindAddress, ctx.Done(), ctrlcommon.RegisterMCCMetrics)

	testCases := []drainControllerTestCase{
		{
			Name:             "Happy Path",
			SimulateEviction: true,
			TestFunc:         testHappyPath,
		},
		{
			Name:     "Hit Max Retries",
			TestFunc: testHitMaxRetries,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			t.Parallel()
			clientSet := setupDrainControllerForTest(ctx, t, testCase.SimulateEviction)
			testCase.TestFunc(ctx, t, clientSet)
		})
	}
}

// This tests that the DrainController drains the node, causing the pods to be
// evicted, and then ensures that it uncordons the node when it is finished.
func testHappyPath(ctx context.Context, t *testing.T, clientSet *fakecorev1client.Clientset) {
	tf := envtest.NewTestFixtures()

	// The assertion funcs will poll infinitely, so we should set a timeout using a context.
	assertCtx, assertCancel := context.WithTimeout(ctx, 1*time.Minute)
	t.Cleanup(assertCancel)

	// Set all pods to running phase before we begin so they can be identified for eviction.
	applyPhaseToPods(ctx, t, clientSet, tf.Pods, corev1.PodRunning)

	for _, node := range tf.Nodes {
		// Initiate the node drain
		initiateDrain(ctx, t, clientSet, node)

		// Set the pod status to "Succeeded" to simulate a successful eviction
		applyPhaseToPods(ctx, t, clientSet, tf.PodsByNodeName[node.Name], corev1.PodSucceeded)
	}

	// Wait for all of our conditions to become true
	helpers.AssertConditionsAreReached(assertCtx, t, []wait.ConditionWithContextFunc{
		// Check that the nodes are drained
		func(ctx context.Context) (bool, error) {
			return isNodesInDesiredState(ctx, clientSet, daemonconsts.DrainerStateDrain)
		},
		// Check that the pods on the nodes are evicted from the node
		func(ctx context.Context) (bool, error) {
			return isPodsEvictedFromNodes(ctx, clientSet, tf.Nodes)
		},
		// Check that Prometheus is showing no drain errors
		func(_ context.Context) (bool, error) {
			return checkPrometheus("mcc_drain_err 0")
		},
	})

	// At this point, we can expect that our nodes have been drained and all of
	// their pods have been evicted. Now, we apply the Uncordon annotation to the
	// node object and wait for all of the nodes to be available again.
	for _, node := range tf.Nodes {
		initiateUncordon(t, ctx, clientSet, node)
	}

	helpers.AssertConditionsAreReached(assertCtx, t, []wait.ConditionWithContextFunc{
		// Check that our nodes are uncordoned
		func(ctx context.Context) (bool, error) {
			return isNodesInDesiredState(ctx, clientSet, daemonconsts.DrainerStateUncordon)
		},
		// Check that Prometheus is showing no drain errors
		func(_ context.Context) (bool, error) {
			return checkPrometheus("mcc_drain_err 0")
		},
	})
}

// This tests that the nodes are attempted to be drained, but eviction
// eventually times out. For the puropses of simulating the eviction failure,
// we purposely do not have our FakeClient wired up to be aware of how to
// handle evictions.
func testHitMaxRetries(ctx context.Context, t *testing.T, clientSet *fakecorev1client.Clientset) {
	tf := envtest.NewTestFixtures()

	for _, node := range tf.Nodes {
		initiateDrain(ctx, t, clientSet, node)
	}

	// The assertion funcs will poll infinitely, so we should set a timeout using a context.
	assertCtx, assertCancel := context.WithTimeout(ctx, 1*time.Minute)
	t.Cleanup(assertCancel)

	helpers.AssertConditionsAreReached(assertCtx, t, []wait.ConditionWithContextFunc{
		// Checks that our nodes do not reach the desired state.
		func(ctx context.Context) (bool, error) {
			result, err := isNodesInDesiredState(ctx, clientSet, daemonconsts.DrainerStateDrain)
			return negate(result, err)
		},
		// Checks that our nodes aren't actually uncordoned either.
		func(ctx context.Context) (bool, error) {
			result, err := isNodesInDesiredState(ctx, clientSet, daemonconsts.DrainerStateUncordon)
			return negate(result, err)
		},
		// Checks that our pods continue to be present on the nodes.
		func(ctx context.Context) (bool, error) {
			result, err := isPodsEvictedFromNodes(ctx, clientSet, tf.Nodes)
			return negate(result, err)
		},
		// Checks that Prometheus shows a drain error.
		func(_ context.Context) (bool, error) {
			return checkPrometheus("mcc_drain_err 1")
		},
	})
}

// This is needed because we don't have a Kubelet. So whenever an eviction
// object is created, we want to delete the pod in question that is trying to
// be evicted. Naturally, for cases where we're doing failure testing, not
// doing this will cause the evictions to hang.
func setupEvictionSimulation(ctx context.Context, t *testing.T, fakeCoreClient *fake.Clientset) {
	// Teach the FakeClient about evictions, otherwise they completely fail
	// because it cannot find the subresource and we get the following error via
	// the drainer:
	// the server could not find the requested resource, GroupVersion "v1" not found
	fakeCoreClient.Fake.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{
					Name:    drain.EvictionSubresource,
					Kind:    drain.EvictionKind,
					Group:   policyv1.SchemeGroupVersion.Group,
					Version: policyv1.SchemeGroupVersion.Version,
				},
			},
		},
	}

	fakeCoreClient.PrependReactor("create", "pods/eviction", func(a k8sTesting.Action) (bool, runtime.Object, error) {
		createAction := a.(k8sTesting.CreateActionImpl)

		eviction := createAction.Object.(*policyv1.Eviction)

		// This needs to run in a goroutine because the object tracker inside the
		// fake client has a mutex. When inside any of the reactor functions, the
		// mutex will be locked. When you attempt to call any of the faked methods
		// or call directly into the object tracker, the reactor function will
		// deadlock waiting for the mutex to unlock.
		go func() {
			err := fakeCoreClient.CoreV1().Pods(eviction.Namespace).Delete(ctx, eviction.Name, metav1.DeleteOptions{})

			// This is needed because the drainer might delete the pod before we can.
			if !apierrors.IsNotFound(err) {
				require.NoError(t, err)
			}
		}()

		return false, eviction, nil
	})
}

// This sets up the DrainController for the test and instantiates the
// FakeClient which is then used throughout this test suite.
func setupDrainControllerForTest(ctx context.Context, t *testing.T, simulateEviction bool) *fakecorev1client.Clientset {
	kubeObjs := envtest.NewTestFixtures().AsRuntimeObjects()

	fakeCoreClient := fakecorev1client.NewSimpleClientset(kubeObjs...)

	fakeMCclient := fakeclientmachineconfigv1.NewSimpleClientset()

	kubeInformer := informers.NewSharedInformerFactory(fakeCoreClient, 0)
	mcInformer := mcfgv1informers.NewSharedInformerFactory(fakeMCclient, 0)

	if simulateEviction {
		setupEvictionSimulation(ctx, t, fakeCoreClient)
	}

	// Set our timeouts to very low limits so that our test suite executes fast.
	dcConfig := &Config{
		MaxRetries:                   0,
		UpdateDelay:                  1 * time.Second,
		DrainTimeoutDuration:         1 * time.Second,
		DrainRequeueDelay:            1 * time.Second,
		DrainRequeueFailingThreshold: 1 * time.Second,
		DrainRequeueFailingDelay:     1 * time.Second,
		DrainHelperTimeout:           1 * time.Second,
		CordonOrUncordonBackoff: wait.Backoff{
			Steps:    5,
			Duration: 10 * time.Second,
			Factor:   2,
		},
		WaitUntil: time.Second,
	}

	dc := New(
		*dcConfig,
		kubeInformer.Core().V1().Nodes(),
		fakeCoreClient,
		fakeMCclient, // TODO: Remove this from the DrainController as it is not used anywhere.
	)

	kubeInformer.Start(ctx.Done())
	mcInformer.Start(ctx.Done())

	// Start the drain controller
	go dc.Run(5, ctx.Done())

	return fakeCoreClient
}

// Sets the desired drainer on a given node.
func setDesiredDrainer(ctx context.Context, t *testing.T, clientSet *fakecorev1client.Clientset, node *corev1.Node, desiredDrainer string) {
	n, err := clientSet.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
	require.NoError(t, err)

	n.Annotations = map[string]string{
		daemonconsts.DesiredDrainerAnnotationKey: desiredDrainer,
	}

	_, err = clientSet.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// Sets the uncordon drainer on a given node.
func initiateUncordon(t *testing.T, ctx context.Context, clientSet *fakecorev1client.Clientset, node *corev1.Node) {
	t.Log("Setting", constants.DrainerStateUncordon, "on", node.Name)
	desiredDrainer := fmt.Sprintf("%s-%s", constants.DrainerStateUncordon, node.Name)
	setDesiredDrainer(ctx, t, clientSet, node, desiredDrainer)
}

// Sets the drain drainer on a given node.
func initiateDrain(ctx context.Context, t *testing.T, clientSet *fakecorev1client.Clientset, node *corev1.Node) {
	t.Log("Setting", constants.DrainerStateDrain, "on", node.Name)
	desiredDrainer := fmt.Sprintf("%s-%s", constants.DrainerStateDrain, node.Name)
	setDesiredDrainer(ctx, t, clientSet, node, desiredDrainer)
}

// Checks if all given nodes have had their pods evicted.
func isPodsEvictedFromNodes(ctx context.Context, clientSet *fakecorev1client.Clientset, nodes []*corev1.Node) (bool, error) {
	for _, node := range nodes {
		evicted, err := isPodsEvictedFromNode(ctx, clientSet, node)
		if err != nil {
			return false, err
		}

		if !evicted {
			return false, nil
		}
	}

	return true, nil
}

/// Checks if a given node has had its pods evicted.
func isPodsEvictedFromNode(ctx context.Context, clientSet *fakecorev1client.Clientset, node *corev1.Node) (bool, error) {
	podList, err := clientSet.CoreV1().Pods("default").List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", node.Name),
	})

	if err != nil {
		return false, err
	}

	return len(podList.Items) == 0, nil
}

// Checks if the nodes are in the desired state.
func isNodesInDesiredState(ctx context.Context, clientSet *fakecorev1client.Clientset, expectedDrainer string) (bool, error) {
	nodeList, err := clientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, nil
	}

	for _, node := range nodeList.Items {
		desiredDrainer := node.Annotations[daemonconsts.DesiredDrainerAnnotationKey]
		lastAppliedDrainer := node.Annotations[daemonconsts.LastAppliedDrainerAnnotationKey]

		if desiredDrainer != lastAppliedDrainer && !strings.Contains(lastAppliedDrainer, expectedDrainer) {
			return false, nil
		}
	}

	return true, nil
}

// Applies a PodPhase to the given pods.
func applyPhaseToPods(ctx context.Context, t *testing.T, clientSet *fakecorev1client.Clientset, pods []*corev1.Pod, podPhase corev1.PodPhase) {
	t.Helper()

	for _, pod := range pods {
		copiedPod := pod.DeepCopy()
		copiedPod.Status.Phase = podPhase
		_, err := clientSet.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, copiedPod, metav1.UpdateOptions{})
		require.NoError(t, err)
	}
}

// Inverts the results from a polling function.
func negate(result bool, err error) (bool, error) {
	if err != nil {
		return false, err
	}

	return !result, nil
}

// Calls Prometheus and verifies that the supplied data is found in its results.
func checkPrometheus(data string) (bool, error) {
	u := url.URL{
		Scheme: "http",
		Host:   "127.0.0.1" + ctrlcommon.DefaultBindAddress,
		Path:   "/metrics",
	}

	r, err := http.Get(u.String())
	if err != nil {
		return false, err
	}

	bodyBuf := bytes.NewBuffer([]byte{})
	_, err = io.Copy(bodyBuf, r.Body)
	if err != nil {
		return false, err
	}

	return strings.Contains(bodyBuf.String(), data), nil
}
