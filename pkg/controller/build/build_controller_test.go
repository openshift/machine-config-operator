package build

import (
	"context"
	"fmt"
	"os"
	"strings"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	buildv1 "github.com/openshift/api/build/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientbuildv1 "github.com/openshift/client-go/build/clientset/versioned/fake"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"

	corev1 "k8s.io/api/core/v1"

	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"testing"
)

const (
	expectedImageSHA             string = "sha256:628e4e8f0a78d91015c6cebeee95931ae2e8defe5dfb4ced4a82830e08937573"
	expectedImagePullspecWithTag string = "registry.hostname.com/org/repo:latest"
	expectedImagePullspecWithSHA string = "registry.hostname.com/org/repo@" + expectedImageSHA
)

type optInFunc func(context.Context, *testing.T, *Clients, string)

func TestMain(m *testing.M) {
	klog.InitFlags(nil)
	os.Exit(m.Run())
}

func TestBuildControllerNoPoolsOptedIn(t *testing.T) {
	t.Parallel()

	fixture := newBuildControllerTestFixture(t)
	fixture.runTestFuncs(t, testFuncs{
		imageBuilder:     testNoMCPsOptedIn,
		customPodBuilder: testNoMCPsOptedIn,
	})
}

func TestBuildControllerSingleOptedInPool(t *testing.T) {
	t.Parallel()

	pool := "worker"

	t.Run("Happy Path", func(t *testing.T) {
		t.Parallel()

		newBuildControllerTestFixture(t).runTestFuncs(t, testFuncs{
			imageBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				testOptInMCPImageBuilder(ctx, t, cs, pool)
			},
			customPodBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				testOptInMCPCustomBuildPod(ctx, t, cs, pool)
			},
		})
	})

	t.Run("Happy Path Multiple Configs", func(t *testing.T) {
		t.Parallel()

		newBuildControllerTestFixture(t).runTestFuncs(t, testFuncs{
			imageBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				testOptInMCPImageBuilder(ctx, t, cs, pool)
			},
			customPodBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				testOptInMCPCustomBuildPod(ctx, t, cs, pool)
			},
		})
	})

	t.Run("Build Failure", func(t *testing.T) {
		t.Parallel()

		newBuildControllerTestFixture(t).runTestFuncs(t, testFuncs{
			imageBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				mcp := optInMCP(ctx, t, cs, pool)
				assertMCPFollowsImageBuildStatus(ctx, t, cs, mcp, buildv1.BuildPhaseFailed)
				assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, pool, isMCPBuildFailure, isMCPBuildFailureMsg)
			},
			customPodBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				mcp := optInMCP(ctx, t, cs, pool)
				assertMCPFollowsBuildPodStatus(ctx, t, cs, mcp, corev1.PodFailed)
				assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, pool, isMCPBuildFailure, isMCPBuildFailureMsg)
			},
		})
	})

	t.Run("Degraded Pool", func(t *testing.T) {
		t.Parallel()

		newBuildControllerTestFixture(t).runTestFuncs(t, testFuncs{
			imageBuilder:     testMCPIsDegraded,
			customPodBuilder: testMCPIsDegraded,
		})
	})

	t.Run("Opted-in pool opts out", func(t *testing.T) {
		t.Parallel()

		newBuildControllerTestFixture(t).runTestFuncs(t, testFuncs{
			imageBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				testOptedInMCPOptsOut(ctx, t, cs, testOptInMCPImageBuilder)
			},
			customPodBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				testOptedInMCPOptsOut(ctx, t, cs, testOptInMCPCustomBuildPod)
			},
		})
	})

	t.Run("Built pool gets unrelated update", func(t *testing.T) {
		t.Parallel()

		newBuildControllerTestFixture(t).runTestFuncs(t, testFuncs{
			imageBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				testOptedInMCPOptsOut(ctx, t, cs, testOptInMCPImageBuilder)
			},
			customPodBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
				testOptedInMCPOptsOut(ctx, t, cs, testOptInMCPCustomBuildPod)
			},
		})
	})
}

func TestBuildControllerMultipleOptedInPools(t *testing.T) {
	t.Parallel()

	pools := []string{"master", "worker"}

	// Tests that a single config is rolled out to the target MachineConfigPools.
	t.Run("Happy Path", func(t *testing.T) {
		t.Parallel()

		fixture := newBuildControllerTestFixture(t)

		for _, pool := range pools {
			pool := pool
			t.Run(pool, func(t *testing.T) {
				fixture.runTestFuncs(t, testFuncs{
					imageBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
						t.Logf("Running in pool %s", pool)
						testOptInMCPImageBuilder(ctx, t, cs, pool)
					},
					customPodBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
						t.Logf("Running in pool %s", pool)
						testOptInMCPCustomBuildPod(ctx, t, cs, pool)
					},
				})
			})
		}
	})

	// Tests that multiple configs are serially rolled out to the target
	// MachineConfigPool and ensures that each config is rolled out before moving
	// onto the next one.
	t.Run("Happy Path Multiple Configs", func(t *testing.T) {
		t.Parallel()

		fixture := newBuildControllerTestFixture(t)

		for _, pool := range pools {
			pool := pool
			t.Run(pool, func(t *testing.T) {
				fixture.runTestFuncs(t, testFuncs{
					imageBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
						testMultipleConfigsAreRolledOut(ctx, t, cs, pool, testOptInMCPImageBuilder)
					},
					customPodBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
						testMultipleConfigsAreRolledOut(ctx, t, cs, pool, testOptInMCPCustomBuildPod)
					},
				})
			})
		}
	})

	// Tests that a build failure degrades the MachineConfigPool
	t.Run("Build Failure", func(t *testing.T) {
		t.Parallel()

		fixture := newBuildControllerTestFixture(t)

		for _, pool := range pools {
			pool := pool
			t.Run(pool, func(t *testing.T) {
				fixture.runTestFuncs(t, testFuncs{
					imageBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
						mcp := optInMCP(ctx, t, cs, pool)
						assertMCPFollowsImageBuildStatus(ctx, t, cs, mcp, buildv1.BuildPhaseFailed)
						assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, pool, isMCPBuildFailure, isMCPBuildFailureMsg)
					},
					customPodBuilder: func(ctx context.Context, t *testing.T, cs *Clients) {
						mcp := optInMCP(ctx, t, cs, pool)
						assertMCPFollowsBuildPodStatus(ctx, t, cs, mcp, corev1.PodFailed)
						assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, pool, isMCPBuildFailure, isMCPBuildFailureMsg)
					},
				})
			})
		}
	})
}

// Holds a name and function to implement a given BuildController test.
type buildControllerTestFixture struct {
	ctx                    context.Context
	t                      *testing.T
	imageBuilderClient     *Clients
	customPodBuilderClient *Clients
}

type testFuncs struct {
	imageBuilder     func(context.Context, *testing.T, *Clients)
	customPodBuilder func(context.Context, *testing.T, *Clients)
}

func newBuildControllerTestFixtureWithContext(ctx context.Context, t *testing.T) *buildControllerTestFixture {
	b := &buildControllerTestFixture{
		ctx: ctx,
		t:   t,
	}

	b.imageBuilderClient = b.startBuildControllerWithImageBuilder()
	b.customPodBuilderClient = b.startBuildControllerWithCustomPodBuilder()

	return b
}

func newBuildControllerTestFixture(t *testing.T) *buildControllerTestFixture {
	ctx, cancel := context.WithTimeout(context.Background(), maxWait)
	t.Cleanup(cancel)

	return newBuildControllerTestFixtureWithContext(ctx, t)
}

func (b *buildControllerTestFixture) runTestFuncs(t *testing.T, tf testFuncs) {
	t.Run("CustomBuildPod", func(t *testing.T) {
		t.Parallel()
		tf.customPodBuilder(b.ctx, t, b.customPodBuilderClient)
	})

	t.Run("ImageBuilder", func(t *testing.T) {
		t.Parallel()
		tf.imageBuilder(b.ctx, t, b.imageBuilderClient)
	})
}

func (b *buildControllerTestFixture) setupClients() *Clients {
	objects := newMachineConfigPoolAndConfigs("master", "rendered-master-1")
	objects = append(objects, newMachineConfigPoolAndConfigs("worker", "rendered-worker-1")...)
	objects = append(objects, &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "machine-config-controller",
		},
	})

	onClusterBuildConfigMap := getOnClusterBuildConfigMap()

	legacyPullSecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`

	pullSecret := `{"auths":{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}}`

	return &Clients{
		mcfgclient: fakeclientmachineconfigv1.NewSimpleClientset(objects...),
		kubeclient: fakecorev1client.NewSimpleClientset(
			getOSImageURLConfigMap(),
			onClusterBuildConfigMap,
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      onClusterBuildConfigMap.Data["finalImagePushSecretName"],
					Namespace: ctrlcommon.MCONamespace,
				},
				Data: map[string][]byte{
					corev1.DockerConfigKey: []byte(legacyPullSecret),
				},
				Type: corev1.SecretTypeDockercfg,
			},
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      onClusterBuildConfigMap.Data["baseImagePullSecretName"],
					Namespace: ctrlcommon.MCONamespace,
				},
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(pullSecret),
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
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
		),
		buildclient: fakeclientbuildv1.NewSimpleClientset(),
	}
}

func (b *buildControllerTestFixture) getConfig() BuildControllerConfig {
	return BuildControllerConfig{
		MaxRetries:  1,
		UpdateDelay: testUpdateDelay,
	}
}

// Instantiates all of the initial objects and starts the BuildController.
func (b *buildControllerTestFixture) startBuildControllerWithImageBuilder() *Clients {
	clients := b.setupClients()

	ctrl := NewWithImageBuilder(b.getConfig(), clients)

	go ctrl.Run(b.ctx, 5)

	return clients
}

func (b *buildControllerTestFixture) startBuildControllerWithCustomPodBuilder() *Clients {
	clients := b.setupClients()

	ctrl := NewWithCustomPodBuilder(b.getConfig(), clients)

	go ctrl.Run(b.ctx, 5)

	return clients
}

func machineConfigPoolHasMachineConfigRefs(pool *mcfgv1.MachineConfigPool) bool {
	expectedMCP := newMachineConfigPool(pool.Name)
	ps := newPoolState(pool)

	for _, ref := range expectedMCP.Spec.Configuration.Source {
		if !ps.HasObjectRef(ref) {
			return false
		}
	}

	return true
}

// Opts a given MachineConfigPool into layering and asserts that the MachineConfigPool reaches the desired state.
func testOptInMCPCustomBuildPod(ctx context.Context, t *testing.T, cs *Clients, poolName string) {
	mcp := optInMCP(ctx, t, cs, poolName)
	assertMCPFollowsBuildPodStatus(ctx, t, cs, mcp, corev1.PodSucceeded)
	assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, poolName, isMCPBuildSuccess, isMCPBuildSuccessMsg)
}

// Opts a given MachineConfigPool into layering and asserts that the MachineConfigPool reaches the desired state.
func testOptInMCPImageBuilder(ctx context.Context, t *testing.T, cs *Clients, poolName string) {
	mcp := optInMCP(ctx, t, cs, poolName)
	assertMCPFollowsImageBuildStatus(ctx, t, cs, mcp, buildv1.BuildPhaseComplete)
	assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, poolName, isMCPBuildSuccess, isMCPBuildSuccessMsg)
}

func testRebuildMCPImageBuilder(ctx context.Context, t *testing.T, cs *Clients, poolName string) {
	mcp := optInMCP(ctx, t, cs, poolName)
	assertMCPFollowsImageBuildStatus(ctx, t, cs, mcp, buildv1.BuildPhaseComplete)
	assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, poolName, isMCPBuildSuccess, isMCPBuildSuccessMsg)
	// wait for an initial build to finish, then rebuild
	mcp.Labels[ctrlcommon.RebuildPoolLabel] = ""
	assertMachineConfigPoolReachesState(ctx, t, cs, poolName, isMCPBuildInProgress)
	assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, poolName, isMCPBuildSuccess, isMCPBuildSuccessMsg)
}

func testRebuildDoesNothing(ctx context.Context, t *testing.T, cs *Clients, poolName string) {

	// Set an unrelated label to force a sync.
	mcpList, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, mcp := range mcpList.Items {
		mcp := mcp
		mcp.Labels[ctrlcommon.RebuildPoolLabel] = ""
		_, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, &mcp, metav1.UpdateOptions{})
		require.NoError(t, err)
	}

	mcpList, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, mcp := range mcpList.Items {
		mcp := mcp
		ps := newPoolState(&mcp)
		assert.False(t, ps.IsLayered())
		assert.False(t, ps.HasOSImage())
	}

}

// Mutates all MachineConfigPools that are not opted in to ensure they are ignored.
func testNoMCPsOptedIn(ctx context.Context, t *testing.T, cs *Clients) {
	// Set an unrelated label to force a sync.
	mcpList, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, mcp := range mcpList.Items {
		mcp := mcp
		mcp.Labels["a-label-key"] = ""
		_, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, &mcp, metav1.UpdateOptions{})
		require.NoError(t, err)
	}

	mcpList, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, mcp := range mcpList.Items {
		mcp := mcp
		ps := newPoolState(&mcp)
		assert.False(t, ps.IsLayered())
		assert.False(t, ps.HasOSImage())
	}
}

// Rolls out multiple configs to a given pool, asserting that each config is completely rolled out before moving onto the next.
func testMultipleConfigsAreRolledOut(ctx context.Context, t *testing.T, cs *Clients, poolName string, optInFunc optInFunc) {
	for i := 1; i < 10; i++ {
		config := fmt.Sprintf("rendered-%s-%d", poolName, i)

		mcp, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
		require.NoError(t, err)

		mcp.Spec.Configuration.Name = config
		mcp.Status.Configuration.Name = config

		renderedMC := testhelpers.NewMachineConfig(
			config,
			map[string]string{
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: "version-number",
				"machineconfiguration.openshift.io/role":             poolName,
			},
			"",
			[]ign3types.File{})

		_, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigs().Create(ctx, renderedMC, metav1.CreateOptions{})
		if err != nil && !k8serrors.IsAlreadyExists(err) {
			require.NoError(t, err)
		}

		_, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
		require.NoError(t, err)

		_, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().UpdateStatus(ctx, mcp, metav1.UpdateOptions{})
		require.NoError(t, err)

		optInFunc(ctx, t, cs, poolName)

		checkFunc := func(pool *mcfgv1.MachineConfigPool) bool {
			return pool.Spec.Configuration.Name == config && isMCPBuildSuccess(pool)
		}

		msgFunc := func(pool *mcfgv1.MachineConfigPool) string {
			sb := &strings.Builder{}
			fmt.Fprintln(sb, isMCPBuildFailureMsg(pool))
			fmt.Fprintf(sb, "Configuration name equals config? %v. Expected: %s\n, Actual: %s\n", pool.Spec.Configuration.Name == config, config, pool.Spec.Configuration.Name)
			return sb.String()
		}

		assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, poolName, checkFunc, msgFunc)
	}
}

// Tests that an opted-in MachineConfigPool is able to opt back out.
func testOptedInMCPOptsOut(ctx context.Context, t *testing.T, cs *Clients, optInFunc optInFunc) {
	optInFunc(ctx, t, cs, "worker")

	optOutMCP(ctx, t, cs, "worker")

	checkFunc := func(mcp *mcfgv1.MachineConfigPool) bool {
		ps := newPoolState(mcp)

		//if ps.IsLayered() {
		//	return false
		//}

		//if ps.HasBuildObjectForCurrentMachineConfig() {
		//	return false
		//}

		if len(ps.GetAllBuildConditions()) != 0 {
			return false
		}

		return true
	}

	msgFunc := func(mcp *mcfgv1.MachineConfigPool) string {
		sb := &strings.Builder{}

		ps := newPoolState(mcp)
		//fmt.Fprintf(sb, "Is layered? %v\n", ps.IsLayered())
		//fmt.Fprintf(sb, "Has build object for current MachineConfig? %v\n", ps.HasBuildObjectForCurrentMachineConfig())
		fmt.Fprintf(sb, "Build objects: %v\n", ps.GetBuildObjectRefs())
		buildConditions := ps.GetAllBuildConditions()
		fmt.Fprintf(sb, "Has no build conditions? %v. Build conditions: %v\n", len(buildConditions) == 0, buildConditions)
		return sb.String()
	}

	assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, "worker", checkFunc, msgFunc)
}

// Tests that if a MachineConfigPool is degraded, that a build (object / pod) is not created.
func testMCPIsDegraded(ctx context.Context, t *testing.T, cs *Clients) {
	mcp, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	mcp.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""

	condition := apihelpers.NewMachineConfigPoolCondition(mcfgv1.MachineConfigPoolDegraded, corev1.ConditionTrue, "", "")
	apihelpers.SetMachineConfigPoolCondition(&mcp.Status, *condition)

	_, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)

	assertMachineConfigPoolReachesState(ctx, t, cs, "worker", func(mcp *mcfgv1.MachineConfigPool) bool {
		// TODO: Should we fail the build without even starting it if the pool is degraded?
		for _, condition := range getMachineConfigPoolBuildConditions() {
			if apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, condition) {
				return false
			}
		}

		return apihelpers.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) &&
			assertNoBuildPods(ctx, t, cs) &&
			assertNoBuilds(ctx, t, cs)
	})
}

// Tests that a label update or similar does not cause a build to occur.
func testBuiltPoolGetsUnrelatedUpdate(ctx context.Context, t *testing.T, cs *Clients, optInFunc optInFunc) {
	optInFunc(ctx, t, cs, "worker")

	pool, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, "worker", metav1.GetOptions{})
	require.NoError(t, err)

	pool.Annotations["unrelated-annotation"] = "hello"
	pool.Labels["unrelated-label"] = ""
	_, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, pool, metav1.UpdateOptions{})
	require.NoError(t, err)

	assertMachineConfigPoolReachesState(ctx, t, cs, "worker", func(mcp *mcfgv1.MachineConfigPool) bool {
		return assert.Equal(t, mcp.Status.Conditions, pool.Status.Conditions) &&
			assertNoBuildPods(ctx, t, cs) &&
			assertNoBuilds(ctx, t, cs)
	})
}

// Mocks whether a given build is running.
type mockIsBuildRunning bool

func (m *mockIsBuildRunning) IsBuildRunning(*mcfgv1.MachineConfigPool) (bool, error) {
	return bool(*m), nil
}

// Tests if we should do a build for a variety of edge-cases and circumstances.
func TestShouldWeDoABuild(t *testing.T) {
	// Mutators which mutate the given MachineConfigPool.
	toLayeredPool := func(mcp *mcfgv1.MachineConfigPool) *mcfgv1.MachineConfigPool {
		mcp.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""
		return mcp
	}

	toLayeredPoolWithImagePullspec := func(mcp *mcfgv1.MachineConfigPool) *mcfgv1.MachineConfigPool {
		mcp = toLayeredPool(mcp)
		ps := newPoolState(mcp)
		ps.SetImagePullspec("image-pullspec")
		return ps.MachineConfigPool()
	}

	toLayeredPoolWithConditionsSet := func(mcp *mcfgv1.MachineConfigPool, conditions []mcfgv1.MachineConfigPoolCondition) *mcfgv1.MachineConfigPool {
		mcp = toLayeredPoolWithImagePullspec(mcp)
		ps := newPoolState(mcp)
		ps.SetBuildConditions(conditions)
		return ps.MachineConfigPool()
	}

	type shouldWeBuildTestCase struct {
		name         string
		oldPool      *mcfgv1.MachineConfigPool
		curPool      *mcfgv1.MachineConfigPool
		buildRunning bool
		expected     bool
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
			name:         "Layered pool with build pod",
			oldPool:      toLayeredPoolWithImagePullspec(newMachineConfigPool("worker", "rendered-worker-1")),
			curPool:      toLayeredPoolWithImagePullspec(newMachineConfigPool("worker", "rendered-worker-1")),
			buildRunning: true,
			expected:     false,
		},
		{
			name: "Layered pool with prior successful build and config change",
			oldPool: toLayeredPoolWithConditionsSet(newMachineConfigPool("worker", "rendered-worker-1"), []mcfgv1.MachineConfigPoolCondition{
				{
					Type:   mcfgv1.MachineConfigPoolBuildSuccess,
					Status: corev1.ConditionTrue,
				},
			}),
			curPool:  toLayeredPoolWithImagePullspec(newMachineConfigPool("worker", "rendered-worker-2")),
			expected: true,
		},
	}

	// Generate additional test cases programmatically.
	buildStates := map[mcfgv1.MachineConfigPoolConditionType]string{
		mcfgv1.MachineConfigPoolBuildFailed:    "failed",
		mcfgv1.MachineConfigPoolBuildPending:   "pending",
		mcfgv1.MachineConfigPoolBuilding:       "in progress",
		mcfgv1.MachineConfigPoolDegraded:       "degraded",
		mcfgv1.MachineConfigPoolNodeDegraded:   "node degraded",
		mcfgv1.MachineConfigPoolRenderDegraded: "render degraded",
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

			mb := mockIsBuildRunning(testCase.buildRunning)

			doABuild, err := shouldWeDoABuild(&mb, testCase.oldPool, testCase.curPool)
			assert.NoError(t, err)
			assert.Equal(t, testCase.expected, doABuild)
		})
	}
}
