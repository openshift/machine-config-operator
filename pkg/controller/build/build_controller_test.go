package build

import (
	"context"
	"fmt"
	"time"

	imageclientset "github.com/openshift/client-go/image/clientset/versioned"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	buildclientset "github.com/openshift/client-go/build/clientset/versioned"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	fakeclientmachineconfigv1 "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	mcfgv1informers "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreinformers "k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"

	corev1 "k8s.io/api/core/v1"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	"testing"
)

// Holds hte fake clients used for running the BuildController tests.
type buildControllerClientset struct {
	ImageClient imageclientset.Interface
	BuildClient buildclientset.Interface
	KubeClient  clientset.Interface
	McfgClient  mcfgclientset.Interface
}

// Holds a name and function to implement a given BuildController test.
type buildControllerTestCase struct {
	name     string
	testFunc func(context.Context, *testing.T, *buildControllerClientset)
}

// Instantiates all of the initial objects and starts the BuildController.
func (b *buildControllerTestCase) startBuildController(ctx context.Context, t *testing.T) *buildControllerClientset {
	bcCtx, bcCtxCancel := context.WithCancel(ctx)
	t.Cleanup(bcCtxCancel)

	kubeClient := fakecorev1client.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "etc-pki-entitlement",
				Namespace: "openshift-config-managed",
			},
			Data: map[string][]byte{
				"entitlement-key.pem": []byte("abc"),
				"entitlement.pem":     []byte("123"),
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "machine-config-operator",
				Namespace: ctrlcommon.MCONamespace,
			},
		},
	)

	fakeMCclient := fakeclientmachineconfigv1.NewSimpleClientset(
		newMachineConfigPool("master", "rendered-master-1"),
		newMachineConfigPool("worker", "rendered-worker-1"),
		&mcfgv1.ControllerConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: "machine-config-controller",
			},
		})

	ccInformer := mcfgv1informers.NewSharedInformerFactory(fakeMCclient, 0)
	mcpInformer := mcfgv1informers.NewSharedInformerFactory(fakeMCclient, 0)
	coreInformer := coreinformers.NewSharedInformerFactoryWithOptions(kubeClient, 0, coreinformers.WithNamespace(ctrlcommon.MCONamespace))

	config := BuildControllerConfig{
		MaxRetries:  5,
		UpdateDelay: time.Millisecond * 100,
	}

	ctrl := New(
		config,
		coreInformer.Core().V1().Pods(),
		ccInformer.Machineconfiguration().V1().ControllerConfigs(),
		mcpInformer.Machineconfiguration().V1().MachineConfigPools(),
		fakeMCclient,
		kubeClient,
	)

	coreInformer.Start(bcCtx.Done())
	ccInformer.Start(bcCtx.Done())
	mcpInformer.Start(bcCtx.Done())

	go ctrl.Run(5, bcCtx.Done())

	return &buildControllerClientset{
		KubeClient: kubeClient,
		McfgClient: fakeMCclient,
	}
}

// Runs the attached test function in parallel.
func (b *buildControllerTestCase) run(ctx context.Context, t *testing.T) {
	t.Run(b.name, func(t *testing.T) {
		t.Parallel()
		t.Cleanup(func() {
			// dumpObjects(ctx, t, bcc, strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")))
		})

		testCtx, testCtxCancel := context.WithTimeout(ctx, time.Second*15)
		t.Cleanup(testCtxCancel)

		bcc := b.startBuildController(testCtx, t)
		b.testFunc(ctx, t, bcc)
	})
}

// Helper that determines if the build is a success.
func isMCPBuildSuccess(mcp *mcfgv1.MachineConfigPool) bool {
	configAnnotation, hasConfigAnnotation := mcp.Labels[ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey]

	expectedConfigAnnotation := "new-image-pullspec"

	return hasConfigAnnotation &&
		ctrlcommon.IsLayeredPool(mcp) &&
		configAnnotation == expectedConfigAnnotation &&
		mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolBuildSuccess) &&
		!machineConfigPoolHasBuildPodRef(mcp)
}

// Opts a given MachineConfigPool into layering and asserts that the MachineConfigPool reaches the desired state.
func testOptInMCP(ctx context.Context, t *testing.T, bcc *buildControllerClientset, poolName string) {
	mcp := optInMCP(ctx, t, bcc, poolName)
	assertMCPFollowsBuildPodStatus(ctx, t, bcc, mcp, corev1.PodSucceeded)
	assertMachineConfigPoolReachesState(ctx, t, bcc, poolName, isMCPBuildSuccess)
}

// Mutates all MachineConfigPools that are not opted in to ensure they are ignored.
func testNoMCPsOptedIn(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	// Set an unrelated label to force a sync.
	mcpList, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, mcp := range mcpList.Items {
		mcp := mcp
		mcp.Labels["a-label-key"] = ""
		_, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Update(ctx, &mcp, metav1.UpdateOptions{})
		require.NoError(t, err)
	}

	mcpList, err = bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, mcp := range mcpList.Items {
		mcp := mcp
		assert.False(t, ctrlcommon.IsLayeredPool(&mcp))
		assert.NotContains(t, mcp.Labels, ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey)
	}
}

// Opts in a single MachineConfigPool and ensures that it reaches the desired state.
func testSingleMCPOptedIn(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	testOptInMCP(ctx, t, bcc, "worker")
}

// Opts multiple MachineConfigPools in and ensures that they reach the desired states.
func testMultipleMCPsOptedIn(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	poolNames := []string{
		"master",
		"worker",
	}

	for _, poolName := range poolNames {
		poolName := poolName

		t.Run(poolName, func(t *testing.T) {
			t.Parallel()
			testOptInMCP(ctx, t, bcc, poolName)
		})
	}
}

// Tests that a failed build degrades the target MachineConfigPool.
func testMCPBuildFailure(ctx context.Context, t *testing.T, bcc *buildControllerClientset, poolName string) {
	mcp := optInMCP(ctx, t, bcc, poolName)

	assertMCPFollowsBuildPodStatus(ctx, t, bcc, mcp, corev1.PodFailed)

	assertMachineConfigPoolReachesState(ctx, t, bcc, poolName, func(mcp *mcfgv1.MachineConfigPool) bool {
		return mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolBuildFailed) &&
			mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded)
	})
}

// Tests that a failed build degrades the target MachineConfigPool.
func testSingleMCPBuildFailure(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	testMCPBuildFailure(ctx, t, bcc, "worker")
}

// Tests that a failed build degrades all target MachineConfigPools.
func testMultipleMCPBuildFailures(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	poolNames := []string{
		"master",
		"worker",
	}

	for _, poolName := range poolNames {
		poolName := poolName

		t.Run(poolName, func(t *testing.T) {
			testMCPBuildFailure(ctx, t, bcc, poolName)
		})
	}
}

// Tests that multiple configs are serially rolled out to the target
// MachineConfigPool and ensures that each config is rolled out before moving
// onto the next one.
func testMultipleConfigsAreRolledOutDriver(ctx context.Context, t *testing.T, bcc *buildControllerClientset, poolName string) {
	for i := 1; i < 10; i++ {
		config := fmt.Sprintf("rendered-%s-%d", poolName, i)

		t.Run(config, func(t *testing.T) {
			workerMCP, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
			require.NoError(t, err)

			workerMCP.Spec.Configuration.Name = config

			_, err = bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Update(ctx, workerMCP, metav1.UpdateOptions{})
			require.NoError(t, err)
			testOptInMCP(ctx, t, bcc, poolName)

			assertMachineConfigPoolReachesState(ctx, t, bcc, poolName, func(mcp *mcfgv1.MachineConfigPool) bool {
				return mcp.Spec.Configuration.Name == config && isMCPBuildSuccess(mcp)
			})
		})
	}
}

// Tests that multiple configs are serially rolled out to the target
// MachineConfigPool and ensures that each config is rolled out before moving
// onto the next one.
func testMultipleConfigsAreRolledOut(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	testMultipleConfigsAreRolledOutDriver(ctx, t, bcc, "worker")
}

// Tests that multiple configs are serially rolled out to all
// MachineConfigPools and ensures that each config is rolled out before moving
// onto the next one. Note: This is parallelized across all of the
// MachineConfigPools.
func testMultipleConfigsAreRolledOutToAllMCPs(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	poolNames := []string{
		"master",
		"worker",
	}

	for _, poolName := range poolNames {
		poolName := poolName
		t.Run(poolName, func(t *testing.T) {
			t.Parallel()
			testMultipleConfigsAreRolledOutDriver(ctx, t, bcc, poolName)
		})
	}
}

// Tests that an opted-in MachineConfigPool is able to opt back out.
func testOptedInMCPOptsOut(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	testOptInMCP(ctx, t, bcc, "worker")

	optOutMCP(ctx, t, bcc, "worker")

	assertMachineConfigPoolReachesState(ctx, t, bcc, "worker", func(mcp *mcfgv1.MachineConfigPool) bool {
		layeringLabels := []string{
			ctrlcommon.LayeringEnabledPoolLabel,
		}

		for _, label := range layeringLabels {
			if _, ok := mcp.Labels[label]; ok {
				return false
			}
		}

		for _, condition := range getMachineConfigPoolBuildConditions() {
			if mcfgv1.IsMachineConfigPoolConditionPresentAndEqual(mcp.Status.Conditions, condition, corev1.ConditionTrue) ||
				mcfgv1.IsMachineConfigPoolConditionPresentAndEqual(mcp.Status.Conditions, condition, corev1.ConditionFalse) {
				return false
			}
		}

		return !machineConfigPoolHasBuildPodRef(mcp)
	})
}

// Tests that if a MachineConfigPool is degraded, that a build pod is not created.
func testMcpIsDegraded(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	mcp, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	mcp.Labels = map[string]string{
		ctrlcommon.LayeringEnabledPoolLabel: "",
	}

	condition := mcfgv1.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolDegraded, corev1.ConditionTrue, "", "")
	mcfgv1.SetMachineConfigPoolCondition(&mcp.Status, *condition)

	_, err = bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)

	assertMachineConfigPoolReachesState(ctx, t, bcc, "worker", func(mcp *mcfgv1.MachineConfigPool) bool {
		// TODO: Should we fail the build without even starting it if the pool is degraded?
		for _, condition := range getMachineConfigPoolBuildConditions() {
			if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, condition) {
				return false
			}
		}

		return mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) &&
			assertNoBuildPods(ctx, t, bcc)
	})
}

// Tests that a label update or similar does not cause a build to occur.
func testBuiltPoolGetsUnrelatedUpdate(ctx context.Context, t *testing.T, bcc *buildControllerClientset) {
	testOptInMCP(ctx, t, bcc, "worker")

	pool, err := bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	pool.Annotations["unrelated-annotation"] = "hello"
	pool.Labels["unrelated-label"] = ""
	_, err = bcc.McfgClient.MachineconfigurationV1().MachineConfigPools().Update(ctx, pool, metav1.UpdateOptions{})
	require.NoError(t, err)

	assertMachineConfigPoolReachesState(ctx, t, bcc, "worker", func(mcp *mcfgv1.MachineConfigPool) bool {
		return assert.Equal(t, mcp.Status.Conditions, pool.Status.Conditions) &&
			assertNoBuildPods(ctx, t, bcc)
	})
}

// Tests all of the major BuildController functionalities.
func TestBuildController(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	testCases := []buildControllerTestCase{
		{
			name:     "No MCPs opted in",
			testFunc: testNoMCPsOptedIn,
		},
		{
			name:     "Single MCP opted in",
			testFunc: testSingleMCPOptedIn,
		},
		{
			name:     "Multiple MCPs opted in",
			testFunc: testMultipleMCPsOptedIn,
		},
		{
			name:     "Single MCP build failure",
			testFunc: testSingleMCPBuildFailure,
		},
		{
			name:     "Multiple MCP build failures",
			testFunc: testMultipleMCPBuildFailures,
		},
		{
			name:     "Multiple configs are rolled out to single MCP",
			testFunc: testMultipleConfigsAreRolledOut,
		},
		{
			name:     "Multiple configs rolled out to all MCPs",
			testFunc: testMultipleConfigsAreRolledOutToAllMCPs,
		},
		{
			name:     "Opted in MCP opts out",
			testFunc: testOptedInMCPOptsOut,
		},
		{
			name:     "MCP is degraded",
			testFunc: testMcpIsDegraded,
		},
		{
			name:     "Built pool gets unrelated update",
			testFunc: testBuiltPoolGetsUnrelatedUpdate,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		testCase.run(ctx, t)
	}
}

// Tests if we should do a build for a variety of edge-cases and circumstances.
func TestShouldWeDoABuild(t *testing.T) {
	t.Parallel()

	// Mutators which mutate the given MachineConfigPool.
	toLayeredPool := func(mcp *mcfgv1.MachineConfigPool) *mcfgv1.MachineConfigPool {
		mcp.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""
		return mcp
	}

	toLayeredPoolWithImagePullspec := func(mcp *mcfgv1.MachineConfigPool) *mcfgv1.MachineConfigPool {
		mcp = toLayeredPool(mcp)
		mcp.Labels[ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey] = "image-pullspec"
		return mcp
	}

	toLayeredPoolWithConditionsSet := func(mcp *mcfgv1.MachineConfigPool, conditions []mcfgv1.MachineConfigPoolCondition) *mcfgv1.MachineConfigPool {
		mcp = toLayeredPoolWithImagePullspec(mcp)
		setMCPBuildConditions(mcp, conditions)
		return mcp
	}

	type shouldWeBuildTestCase struct {
		name     string
		oldPool  *mcfgv1.MachineConfigPool
		curPool  *mcfgv1.MachineConfigPool
		buildPod *corev1.Pod
		expected bool
	}

	testCases := []shouldWeBuildTestCase{
		{
			name:     "Non-layered pool",
			oldPool:  newMachineConfigPool("worker", "rendered-worker-1"),
			curPool:  newMachineConfigPool("worker", "rendered-worker-1"),
			expected: false,
		},
		{
			name:     "Layered pool config change with missing image pullspec",
			oldPool:  toLayeredPool(newMachineConfigPool("worker", "rendered-worker-1")),
			curPool:  toLayeredPool(newMachineConfigPool("worker", "rendered-worker-2")),
			expected: true,
		},
		{
			name:     "Layered pool with no config change and missing image pullspec",
			oldPool:  toLayeredPool(newMachineConfigPool("worker", "rendered-worker-1")),
			curPool:  toLayeredPool(newMachineConfigPool("worker", "rendered-worker-1")),
			expected: true,
		},
		{
			name:    "Layered pool with image pullspec",
			oldPool: toLayeredPoolWithImagePullspec(newMachineConfigPool("worker", "rendered-worker-1")),
			curPool: toLayeredPoolWithImagePullspec(newMachineConfigPool("worker", "rendered-worker-1")),
		},
		{
			name:     "Layered pool with build pod",
			oldPool:  toLayeredPoolWithImagePullspec(newMachineConfigPool("worker", "rendered-worker-1")),
			curPool:  toLayeredPoolWithImagePullspec(newMachineConfigPool("worker", "rendered-worker-1")),
			buildPod: newBuildPod(newMachineConfigPool("worker", "rendered-worker-1")),
			expected: false,
		},
	}

	// Generate additional test cases programmatically.
	buildStates := map[mcfgv1.MachineConfigPoolConditionType]string{
		mcfgv1.MachineConfigPoolBuildFailed:  "failed",
		mcfgv1.MachineConfigPoolBuildPending: "pending",
		mcfgv1.MachineConfigPoolBuildSuccess: "successful",
		mcfgv1.MachineConfigPoolBuilding:     "in progress",
	}

	for conditionType, name := range buildStates {
		conditions := []mcfgv1.MachineConfigPoolCondition{
			{
				Type:   conditionType,
				Status: corev1.ConditionTrue,
			},
		}

		testCases = append(testCases, shouldWeBuildTestCase{
			name:     fmt.Sprintf("Layered pool with %s build", name),
			oldPool:  toLayeredPoolWithConditionsSet(newMachineConfigPool("worker", "rendered-worker-1"), conditions),
			curPool:  toLayeredPoolWithConditionsSet(newMachineConfigPool("worker", "rendered-worker-1"), conditions),
			expected: false,
		})
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			kubeClient := fakecorev1client.NewSimpleClientset()
			if testCase.buildPod != nil {
				kubeClient = fakecorev1client.NewSimpleClientset(testCase.buildPod)
			}

			doABuild, err := shouldWeDoABuild(kubeClient, testCase.oldPool, testCase.curPool)
			assert.NoError(t, err)
			assert.Equal(t, testCase.expected, doABuild)
		})
	}
}
