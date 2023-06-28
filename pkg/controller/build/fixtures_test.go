package build

import (
	"context"
	"fmt"
	"io/ioutil"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/davecgh/go-spew/spew"
	"github.com/ghodss/yaml"
	buildv1 "github.com/openshift/api/build/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Gets an example machine-config-osimageurl ConfigMap.
func getOSImageURLConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      machineConfigOSImageURLConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			baseOSContainerImageConfigKey:           "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:12e89d631c0ca1700262583acfb856b6e7dbe94800cb38035d68ee5cc912411c",
			baseOSExtensionsContainerImageConfigKey: "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:5b6d901069e640fc53d2e971fa1f4802bf9dea1a4ffba67b8a17eaa7d8dfa336",
			osImageURLConfigKey:                     "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:4f7792412d1559bf0a996edeff5e836e210f6d77df94b552a3866144d043bce1",
			releaseVersionConfigKey:                 "4.14.0-0.ci-2023-05-29-125629",
		},
	}
}

// Gets an example on-cluster-build-config ConfigMap.
func getOnClusterBuildConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      onClusterBuildConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			baseImagePullSecretNameConfigKey:  "base-image-pull-secret",
			finalImagePushSecretNameConfigKey: "final-image-push-secret",
			finalImagePullspecConfigKey:       expectedImagePullspecWithTag,
		},
	}
}

// Creates a new MachineConfigPool and the corresponding MachineConfigs.
func newMachineConfigPoolAndConfigs(name string, params ...string) []runtime.Object {
	mcp := newMachineConfigPool(name, params...)

	out := []runtime.Object{mcp}

	files := []ign3types.File{}

	// Create individual MachineConfigs to accompany the child MachineConfigs referred to by our MachineConfigPool.
	for _, childConfig := range mcp.Spec.Configuration.Source {
		if childConfig.Kind != "MachineConfig" {
			continue
		}

		filename := fmt.Sprintf("/etc/%s", childConfig.Name)
		file := ctrlcommon.NewIgnFile(filename, childConfig.Name)
		files = append(files, file)

		out = append(out, testhelpers.NewMachineConfig(
			childConfig.Name,
			map[string]string{
				"machineconfiguration.openshift.io/role": name,
			},
			"",
			[]ign3types.File{file}))
	}

	// Create a rendered MachineConfig to accompany our MachineConfigPool.
	out = append(out, testhelpers.NewMachineConfig(
		mcp.Spec.Configuration.Name,
		map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: "version-number",
			"machineconfiguration.openshift.io/role":             name,
		},
		"",
		files))

	return out
}

// Creates a simple MachineConfigPool object for testing. Requires a name for
// the MachineConfigPool, optionally accepts a name for the rendered config.
func newMachineConfigPool(name string, params ...string) *mcfgv1.MachineConfigPool {
	renderedConfigName := ""
	if len(params) >= 1 {
		renderedConfigName = params[0]
	} else {
		renderedConfigName = fmt.Sprintf("rendered-%s-1", name)
	}

	childConfigs := []corev1.ObjectReference{}
	for i := 1; i <= 5; i++ {
		childConfigs = append(childConfigs, corev1.ObjectReference{
			Name: fmt.Sprintf("%s-config-%d", name, i),
			Kind: "MachineConfig",
		})
	}

	nodeRoleLabel := fmt.Sprintf("node-role.kubernetes.io/%s", name)
	nodeSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, nodeRoleLabel, "")

	poolSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, mcfgv1.MachineConfigRoleLabelKey, name)

	mcp := testhelpers.NewMachineConfigPool(name, poolSelector, nodeSelector, renderedConfigName)
	mcp.Spec.Configuration.Source = append(mcp.Spec.Configuration.Source, childConfigs...)
	mcp.Status.Configuration.Source = append(mcp.Status.Configuration.Source, childConfigs...)

	return mcp
}

// Opts a MachineConfigPool into layering.
func optInMCP(ctx context.Context, t *testing.T, cs *Clients, poolName string) *mcfgv1.MachineConfigPool {
	t.Helper()

	mcp, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	mcp.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""

	mcp, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)

	return mcp
}

// Opts a MachineConfigPool out of layering.
func optOutMCP(ctx context.Context, t *testing.T, cs *Clients, poolName string) {
	t.Helper()

	mcp, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
	require.NoError(t, err)

	delete(mcp.Labels, ctrlcommon.LayeringEnabledPoolLabel)

	_, err = cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Update(ctx, mcp, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// Polls until a MachineConfigPool reaches a desired state.
func assertMachineConfigPoolReachesState(ctx context.Context, t *testing.T, cs *Clients, poolName string, checkFunc func(*mcfgv1.MachineConfigPool) bool) bool {
	t.Helper()

	pollCtx, cancel := context.WithTimeout(ctx, time.Second*10)
	t.Cleanup(cancel)

	err := wait.PollImmediateUntilWithContext(pollCtx, time.Millisecond, func(c context.Context) (bool, error) {
		mcp, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(c, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return checkFunc(mcp), nil
	})

	return assert.NoError(t, err, "MachineConfigPool %s never reached desired state", poolName)
}

// Asserts that there are no build pods.
func assertNoBuildPods(ctx context.Context, t *testing.T, cs *Clients) bool {
	t.Helper()

	foundBuildPods := false

	buildPodNames := []string{}

	podList, err := cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, pod := range podList.Items {
		pod := pod
		if hasAllRequiredOSBuildLabels(pod.Labels) {
			foundBuildPods = true
			buildPodNames = append(buildPodNames, pod.Name)
		}
	}

	return assert.False(t, foundBuildPods, "expected not to find build pods, found: %v", buildPodNames)
}

// Asserts that there are no builds.
func assertNoBuilds(ctx context.Context, t *testing.T, cs *Clients) bool {
	t.Helper()

	foundBuilds := false

	buildNames := []string{}

	buildList, err := cs.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	for _, build := range buildList.Items {
		build := build
		if hasAllRequiredOSBuildLabels(build.Labels) {
			foundBuilds = true
			buildNames = append(buildNames, build.Name)
		}
	}

	return assert.False(t, foundBuilds, "expected not to find builds, found: %v", buildNames)
}

// Asserts that ConfigMaps were created.
func assertConfigMapsCreated(ctx context.Context, t *testing.T, cs *Clients, ibr ImageBuildRequest) bool {
	t.Helper()

	isFound := func(name string, configmapList *corev1.ConfigMapList) bool {
		for _, item := range configmapList.Items {
			if item.Name == name && hasAllRequiredOSBuildLabels(item.Labels) {
				return true
			}
		}

		return false
	}

	expectedConfigmaps := map[string]bool{
		ibr.getDockerfileConfigMapName(): false,
		ibr.getMCConfigMapName():         false,
	}

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		configmapList, err := cs.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for expected := range expectedConfigmaps {
			if isFound(expected, configmapList) {
				expectedConfigmaps[expected] = true
			} else {
				return false, nil
			}
		}

		return true, nil
	})

	return assert.NoError(t, err, "configmap(s) was not created %v", expectedConfigmaps)
}

// Polls until a build is created.
func assertBuildIsCreated(ctx context.Context, t *testing.T, cs *Clients, ibr ImageBuildRequest) bool {
	t.Helper()

	buildName := ibr.getBuildName()

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		buildList, err := cs.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, build := range buildList.Items {
			if build.Name == buildName {
				return true, nil
			}
		}

		return false, nil
	})

	return assert.NoError(t, err, "build %s was not created", buildName)
}

// Polls until a build pod is created.
func assertBuildPodIsCreated(ctx context.Context, t *testing.T, cs *Clients, ibr ImageBuildRequest) bool {
	t.Helper()

	buildPodName := ibr.getBuildName()

	err := wait.PollImmediateInfiniteWithContext(ctx, time.Millisecond, func(ctx context.Context) (bool, error) {
		podList, err := cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, pod := range podList.Items {
			if pod.Name == buildPodName {
				return true, nil
			}
		}

		return false, nil
	})

	return assert.NoError(t, err, "build pod %s was not created", buildPodName)
}

// Simulates a pod being scheduled and reaching various states. Verifies that
// the target MachineConfigPool reaches the expected states as it goes.
func assertMCPFollowsImageBuildStatus(ctx context.Context, t *testing.T, cs *Clients, mcp *mcfgv1.MachineConfigPool, endingPhase buildv1.BuildPhase) bool { //nolint:unparam // This param is actually used.
	t.Helper()

	var outcome bool

	defer func() {
		assert.True(t, outcome)
	}()

	// Each of the various pod phases we're interested in.
	buildPhases := []buildv1.BuildPhase{
		buildv1.BuildPhaseNew,
		buildv1.BuildPhasePending,
		buildv1.BuildPhaseRunning,
		endingPhase,
	}

	// Each pod phase is correllated to a MachineConfigPoolConditionType.
	buildPhaseToMCPCondition := map[buildv1.BuildPhase]mcfgv1.MachineConfigPoolConditionType{
		buildv1.BuildPhaseNew:       mcfgv1.MachineConfigPoolBuildPending,
		buildv1.BuildPhasePending:   mcfgv1.MachineConfigPoolBuildPending,
		buildv1.BuildPhaseRunning:   mcfgv1.MachineConfigPoolBuilding,
		buildv1.BuildPhaseComplete:  mcfgv1.MachineConfigPoolBuildSuccess,
		buildv1.BuildPhaseError:     mcfgv1.MachineConfigPoolBuildFailed,
		buildv1.BuildPhaseFailed:    mcfgv1.MachineConfigPoolBuildFailed,
		buildv1.BuildPhaseCancelled: mcfgv1.MachineConfigPoolBuildFailed,
	}

	// Determine if the MachineConfigPool should have a reference to the build pod.
	shouldHaveBuildRef := map[buildv1.BuildPhase]bool{
		buildv1.BuildPhaseNew:       true,
		buildv1.BuildPhasePending:   true,
		buildv1.BuildPhaseRunning:   true,
		buildv1.BuildPhaseComplete:  false,
		buildv1.BuildPhaseError:     true,
		buildv1.BuildPhaseFailed:    true,
		buildv1.BuildPhaseCancelled: true,
	}

	ibr := newImageBuildRequest(mcp)

	buildName := ibr.getBuildName()

	// Wait for the build pod to be created.
	outcome = assertBuildIsCreated(ctx, t, cs, ibr)
	if !outcome {
		return false
	}

	outcome = assertConfigMapsCreated(ctx, t, cs, ibr)
	if !outcome {
		return false
	}

	// Cycle through each of the build pod phases.
	for _, phase := range buildPhases {
		// Get the build pod by name.
		build, err := cs.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Get(ctx, buildName, metav1.GetOptions{})
		require.NoError(t, err)

		// Set the pod phase and update it.
		build.Status.Phase = phase

		// If we're successful, the build object should have an image pullspec attached to it.
		// TODO: Need to figure out how / where to set this on the custom pod builder.
		if phase == buildv1.BuildPhaseComplete {
			build.Status.OutputDockerImageReference = expectedImagePullspecWithTag
			build.Status.Output.To = &buildv1.BuildStatusOutputTo{
				ImageDigest: expectedImageSHA,
			}
		}

		_, err = cs.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Update(ctx, build, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Look up the expected MCP condition for our current pod phase.
		expectedMCPCondition := buildPhaseToMCPCondition[phase]

		// Look up the expected build pod condition for our current pod phase.
		expectedBuildRefPresence := shouldHaveBuildRef[phase]

		var targetPool *mcfgv1.MachineConfigPool

		// Wait for the MCP condition to reach the expected state.
		outcome = assertMachineConfigPoolReachesState(ctx, t, cs, mcp.Name, func(mcp *mcfgv1.MachineConfigPool) bool {
			targetPool = mcp
			return mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, expectedMCPCondition) &&
				expectedBuildRefPresence == machineConfigPoolHasBuildRef(mcp) &&
				machineConfigPoolHasMachineConfigRefs(mcp)
		})

		if !outcome {
			spew.Dump(targetPool)
			t.Logf("Has expected condition (%s) for phase (%s)? %v", expectedMCPCondition, phase, mcfgv1.IsMachineConfigPoolConditionTrue(targetPool.Status.Conditions, expectedMCPCondition))
			t.Logf("Has ref? %v. Expected: %v. Actual: %v.", expectedBuildRefPresence == machineConfigPoolHasBuildRef(targetPool), expectedBuildRefPresence, machineConfigPoolHasBuildRef(targetPool))
			t.Logf("Has MachineConfig refs? %v", machineConfigPoolHasMachineConfigRefs(targetPool))
			return false
		}
	}

	// Find out what happened to the build and its objects.
	_, err := cs.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).Get(ctx, buildName, metav1.GetOptions{})
	switch endingPhase {
	case buildv1.BuildPhaseComplete:
		// If the build pod was successful, looking it up should fail because it should have been deleted.
		outcome = assert.Error(t, err)
	default:
		// If the build pod failed, looking it up should succeed since we leave it around for debugging.
		outcome = assert.NoError(t, err)
	}

	return outcome
}

// Simulates a pod being scheduled and reaching various states. Verifies that
// the target MachineConfigPool reaches the expected states as it goes.
func assertMCPFollowsBuildPodStatus(ctx context.Context, t *testing.T, cs *Clients, mcp *mcfgv1.MachineConfigPool, endingPhase corev1.PodPhase) bool { //nolint:unparam // This param is actually used.
	t.Helper()

	var outcome bool

	defer func() {
		assert.True(t, outcome)
	}()

	// Each of the various pod phases we're interested in.
	podPhases := []corev1.PodPhase{
		corev1.PodPending,
		corev1.PodRunning,
		endingPhase,
	}

	// Each pod phase is correllated to a MachineConfigPoolConditionType.
	podPhaseToMCPCondition := map[corev1.PodPhase]mcfgv1.MachineConfigPoolConditionType{
		corev1.PodPending:   mcfgv1.MachineConfigPoolBuildPending,
		corev1.PodRunning:   mcfgv1.MachineConfigPoolBuilding,
		corev1.PodFailed:    mcfgv1.MachineConfigPoolBuildFailed,
		corev1.PodSucceeded: mcfgv1.MachineConfigPoolBuildSuccess,
	}

	// Determine if the MachineConfigPool should have a reference to the build pod.
	shouldHaveBuildPodRef := map[corev1.PodPhase]bool{
		corev1.PodPending:   true,
		corev1.PodRunning:   true,
		corev1.PodFailed:    true,
		corev1.PodSucceeded: false,
	}

	ibr := newImageBuildRequest(mcp)
	buildPodName := ibr.getBuildName()

	// Wait for the build pod to be created.
	outcome = assertBuildPodIsCreated(ctx, t, cs, ibr)
	if !outcome {
		return outcome
	}

	outcome = assertConfigMapsCreated(ctx, t, cs, ibr)
	if !outcome {
		return false
	}

	// Cycle through each of the build pod phases.
	for _, phase := range podPhases {
		// Get the build pod by name.
		buildPod, err := cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, buildPodName, metav1.GetOptions{})
		require.NoError(t, err)

		// Set the pod phase and update it.
		buildPod.Status.Phase = phase

		// If we've reached the successful pod phase, create the ConfigMap that the
		// build pod does which has the resulting image digest.
		if phase == corev1.PodSucceeded {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ibr.getDigestConfigMapName(),
					Namespace: ctrlcommon.MCONamespace,
				},
				Data: map[string]string{
					"digest": expectedImageSHA,
				},
			}
			_, cmErr := cs.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(ctx, cm, metav1.CreateOptions{})
			require.NoError(t, cmErr)
		}

		_, err = cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Update(ctx, buildPod, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Look up the expected MCP condition for our current pod phase.
		expectedMCPCondition := podPhaseToMCPCondition[phase]

		// Look up the expected build pod condition for our current pod phase.
		expectedBuildPodRefPresence := shouldHaveBuildPodRef[phase]

		var targetPool *mcfgv1.MachineConfigPool

		// Wait for the MCP condition to reach the expected state.
		outcome = assertMachineConfigPoolReachesState(ctx, t, cs, mcp.Name, func(mcp *mcfgv1.MachineConfigPool) bool {
			targetPool = mcp
			return mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, expectedMCPCondition) &&
				expectedBuildPodRefPresence == machineConfigPoolHasBuildRef(mcp) &&
				machineConfigPoolHasMachineConfigRefs(mcp)
		})

		if !outcome {
			spew.Dump(targetPool)
			t.Logf("Has expected condition (%s) for phase (%s)? %v", expectedMCPCondition, phase, mcfgv1.IsMachineConfigPoolConditionTrue(targetPool.Status.Conditions, expectedMCPCondition))
			t.Logf("Has ref? %v. Expected: %v. Actual: %v.", expectedBuildPodRefPresence == machineConfigPoolHasBuildRef(targetPool), expectedBuildPodRefPresence, machineConfigPoolHasBuildRef(targetPool))
			t.Logf("Has MachineConfig refs? %v", machineConfigPoolHasMachineConfigRefs(targetPool))
			return false
		}
	}

	// Find out what happened to the build pod.
	_, err := cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, buildPodName, metav1.GetOptions{})
	switch endingPhase {
	case corev1.PodSucceeded:

		// If the build pod was successful, looking it up should fail because it should have been deleted.
		outcome = assert.Error(t, err)
	case corev1.PodFailed:
		// If the build pod failed, looking it up should succeed since we leave it around for debugging.
		outcome = assert.NoError(t, err)
	}

	return outcome
}

// Dumps all the objects within each of the fake clients to a YAML file for easy debugging.
func dumpObjects(ctx context.Context, t *testing.T, cs *Clients, filenamePrefix string) {
	if cs.mcfgclient != nil {
		mcp, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, mcp, filenamePrefix+"-machineconfigpools.yaml")

		machineconfigs, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigs().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, machineconfigs, filenamePrefix+"-machineconfigs.yaml")
	}

	if cs.kubeclient != nil {
		pods, err := cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, pods, filenamePrefix+"-pods.yaml")

		configmaps, err := cs.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		dumpToYAMLFile(t, configmaps, filenamePrefix+"-configmaps.yaml")
	}

	if cs.buildclient != nil {
		buildconfigs, err := cs.buildclient.BuildV1().BuildConfigs(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		dumpToYAMLFile(t, buildconfigs, filenamePrefix+"-buildconfigs.yaml")

		builds, err := cs.buildclient.BuildV1().Builds(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		dumpToYAMLFile(t, builds, filenamePrefix+"-builds.yaml")
	}
}

// Dumps the provided object to the given filename.
func dumpToYAMLFile(t *testing.T, obj interface{}, filename string) {
	out, err := yaml.Marshal(obj)
	require.NoError(t, err)

	filename = strings.ReplaceAll(filename, "/", "_")

	require.NoError(t, ioutil.WriteFile(filename, out, 0755))
}
