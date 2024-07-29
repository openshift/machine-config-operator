package build

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/davecgh/go-spew/spew"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	maxWait         time.Duration = time.Second * 5
	pollInterval    time.Duration = time.Millisecond
	testUpdateDelay time.Duration = time.Millisecond * 5
)

func newMachineOSConfig(pool *mcfgv1.MachineConfigPool) *mcfgv1alpha1.MachineOSConfig {
	return &mcfgv1alpha1.MachineOSConfig{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MachineOSConfig",
			APIVersion: "machineconfiguration.openshift.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: pool.Name,
		},
		Spec: mcfgv1alpha1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1alpha1.MachineConfigPoolReference{
				Name: pool.Name,
			},
			BuildInputs: mcfgv1alpha1.BuildInputs{
				ImageBuilder: &mcfgv1alpha1.MachineOSImageBuilder{
					ImageBuilderType: mcfgv1alpha1.MachineOSImageBuilderType("PodImageBuilder"),
				},
				BaseImagePullSecret: mcfgv1alpha1.ImageSecretObjectReference{
					Name: "base-image-pull-secret",
				},
				RenderedImagePushSecret: mcfgv1alpha1.ImageSecretObjectReference{
					Name: "final-image-push-secret",
				},
				RenderedImagePushspec: expectedImagePullspecWithTag,
			},
			BuildOutputs: mcfgv1alpha1.BuildOutputs{
				CurrentImagePullSecret: mcfgv1alpha1.ImageSecretObjectReference{
					Name: "current-image-pull-secret",
				},
			},
		},
	}
}

func newMachineOSBuild(config *mcfgv1alpha1.MachineOSConfig, pool *mcfgv1.MachineConfigPool) *mcfgv1alpha1.MachineOSBuild {
	return &mcfgv1alpha1.MachineOSBuild{
		TypeMeta: metav1.TypeMeta{
			Kind:       "MachineOSBuild",
			APIVersion: "machineconfiguration.openshift.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%s-builder", config.Spec.MachineConfigPool.Name, pool.Spec.Configuration.Name),
		},
		Spec: mcfgv1alpha1.MachineOSBuildSpec{
			RenderedImagePushspec: expectedImagePullspecWithTag,
			Version:               1,
			ConfigGeneration:      1,
			DesiredConfig: mcfgv1alpha1.RenderedMachineConfigReference{
				Name: pool.Spec.Configuration.Name,
			},
			MachineOSConfig: mcfgv1alpha1.MachineOSConfigReference{
				Name: config.Name,
			},
		},
	}
}

func getMachineOSConfig(ctx context.Context, cs *Clients, config *mcfgv1alpha1.MachineOSConfig, pool *mcfgv1.MachineConfigPool) (*mcfgv1alpha1.MachineOSConfig, error) {
	var ourConfig *mcfgv1alpha1.MachineOSConfig
	if config == nil {
		configList, err := cs.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return nil, err
		}

		for _, config := range configList.Items {
			if config.Spec.MachineConfigPool.Name == pool.Name {
				ourConfig = &config
				break
			}
		}

		if ourConfig == nil {
			return nil, nil
		}
	} else {
		ourConfig = config
	}
	return ourConfig, nil
}

func getMachineOSBuild(ctx context.Context, cs *Clients, config *mcfgv1alpha1.MachineOSConfig, pool *mcfgv1.MachineConfigPool) (*mcfgv1alpha1.MachineOSBuild, error) {
	var ourBuild *mcfgv1alpha1.MachineOSBuild

	ourConfig, err := getMachineOSConfig(ctx, cs, config, pool)
	if err != nil {
		return nil, err
	}

	err = wait.PollImmediateInfiniteWithContext(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		buildList, err := cs.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		if len(buildList.Items) >= 1 {
			for _, build := range buildList.Items {
				if build.Spec.MachineOSConfig.Name == ourConfig.Name {
					if build.Spec.DesiredConfig.Name == pool.Spec.Configuration.Name {
						ourBuild = &build
						return true, nil
					}
				}
			}
		}
		return false, nil
	})

	if err != nil {
		return nil, err
	}
	return ourBuild, nil
}

// Gets an example machine-config-osimageurl ConfigMap.
func getOSImageURLConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.MachineConfigOSImageURLConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"baseOSContainerImage":           "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:12e89d631c0ca1700262583acfb856b6e7dbe94800cb38035d68ee5cc912411c",
			"baseOSExtensionsContainerImage": "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:5b6d901069e640fc53d2e971fa1f4802bf9dea1a4ffba67b8a17eaa7d8dfa336",
			"osImageURL":                     "",
			"releaseVersion":                 "4.14.0-0.ci-2023-05-29-125629",
		},
	}
}

// Gets an example machine.config.operator-images ConfigMap.
func getImagesConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.MachineConfigOperatorImagesConfigMapName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"images.json": `{"machineConfigOperator": "mco.image.pullspec"}`,
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

// Polls until a MachineConfigPool reaches a desired state.
func assertMachineConfigPoolReachesState(ctx context.Context, t *testing.T, cs *Clients, poolName string, checkFunc func(*mcfgv1.MachineConfigPool) bool) bool {
	t.Helper()

	pollCtx, cancel := context.WithTimeout(ctx, maxWait)
	t.Cleanup(cancel)

	err := wait.PollImmediateUntilWithContext(pollCtx, pollInterval, func(c context.Context) (bool, error) {
		mcp, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(c, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return checkFunc(mcp), nil
	})

	return assert.NoError(t, err, "MachineConfigPool %s never reached expected state", poolName)
}

// Polls until a MOSB reaches a desired state.
func assertMachineConfigPoolReachesStateWithMsg(ctx context.Context, t *testing.T, cs *Clients, poolName string, checkFunc func(*mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild, *mcfgv1.MachineConfigPool) bool, msgFunc func(*mcfgv1alpha1.MachineOSConfig, *mcfgv1alpha1.MachineOSBuild, *mcfgv1.MachineConfigPool) string) bool {
	t.Helper()

	pollCtx, cancel := context.WithTimeout(ctx, maxWait)
	t.Cleanup(cancel)

	var (
		targetPool *mcfgv1.MachineConfigPool
		mosc       *mcfgv1alpha1.MachineOSConfig
		mosb       *mcfgv1alpha1.MachineOSBuild
	)

	err := wait.PollImmediateUntilWithContext(pollCtx, pollInterval, func(c context.Context) (bool, error) {
		mcp, err := cs.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(c, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		targetPool = mcp

		mosc, err = getMachineOSConfig(ctx, cs, mosc, mcp)
		if err != nil {
			return false, err
		}
		mosb, err = getMachineOSBuild(ctx, cs, mosc, mcp)
		if err != nil {
			return false, err
		}
		return checkFunc(mosc, mosb, mcp), nil
	})

	sb := &strings.Builder{}
	fmt.Fprintf(sb, "MachineOSBuild %s did not reach expected state\n", mosb.Name)
	spew.Fdump(sb, mosb)
	fmt.Fprintln(sb, msgFunc(mosc, mosb, targetPool))

	return assert.NoError(t, err, sb.String())
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

// Asserts that ConfigMaps were created.
func assertConfigMapsCreated(ctx context.Context, t *testing.T, cs *Clients, mosb *mcfgv1alpha1.MachineOSBuild) bool {
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
		buildrequest.GetContainerfileConfigMapName(mosb): false,
		buildrequest.GetMCConfigMapName(mosb):            false,
	}

	err := wait.PollImmediateInfiniteWithContext(ctx, pollInterval, func(ctx context.Context) (bool, error) {
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

// Polls until a build pod is created.
func assertBuildPodIsCreated(ctx context.Context, t *testing.T, cs *Clients, buildPodName string) bool {
	t.Helper()

	var podNames []string

	err := wait.PollImmediateInfiniteWithContext(ctx, pollInterval, func(ctx context.Context) (bool, error) {
		podList, err := cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		podNames = []string{}

		for _, pod := range podList.Items {
			podNames = append(podNames, pod.Name)
			if pod.Name == buildPodName && hasAllRequiredOSBuildLabels(pod.Labels) {
				return true, nil
			}
		}

		return false, nil
	})

	return assert.NoError(t, err, "build pod %s was not created, have: %v", buildPodName, podNames)
}

// Simulates a pod being scheduled and reaching various states. Verifies that
// the target MachineConfigPool reaches the expected states as it goes.
func assertMOSBFollowsBuildPodStatus(ctx context.Context, t *testing.T, cs *Clients, mcp *mcfgv1.MachineConfigPool, mosc *mcfgv1alpha1.MachineOSConfig, endingPhase corev1.PodPhase) bool { //nolint:unparam // This param is actually used.
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

	// Each pod phase is correllated to a MachineOSBuildConditionType.
	podPhaseToMOSBCondition := map[corev1.PodPhase]mcfgv1alpha1.BuildProgress{
		corev1.PodPending:   mcfgv1alpha1.MachineOSBuildPrepared,
		corev1.PodRunning:   mcfgv1alpha1.MachineOSBuilding,
		corev1.PodFailed:    mcfgv1alpha1.MachineOSBuildFailed,
		corev1.PodSucceeded: mcfgv1alpha1.MachineOSBuildSucceeded,
	}

	// Determine if the MachineOSBuild should have a reference to the build pod.
	shouldHaveBuildPodRef := map[corev1.PodPhase]bool{
		corev1.PodPending:   true,
		corev1.PodRunning:   true,
		corev1.PodFailed:    true,
		corev1.PodSucceeded: false,
	}

	mosb, err := getMachineOSBuild(ctx, cs, mosc, mcp)
	outcome = assert.NoError(t, err)

	buildPodName := buildrequest.GetBuildPodName(mosb)

	// Wait for the build pod to be created.
	outcome = assertBuildPodIsCreated(ctx, t, cs, buildPodName)
	if !outcome {
		return outcome
	}

	outcome = assertConfigMapsCreated(ctx, t, cs, mosb)
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
					Name:      buildrequest.GetDigestConfigMapName(mosb),
					Namespace: ctrlcommon.MCONamespace,
				},
				Data: map[string]string{
					"digest": expectedImageSHA,
				},
			}
			_, cmErr := cs.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(ctx, cm, metav1.CreateOptions{})
			require.NoError(t, cmErr)
		}

		_, err = cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).UpdateStatus(ctx, buildPod, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Look up the expected MCP condition for our current pod phase.
		expectedMOSBCondition := podPhaseToMOSBCondition[phase]

		// Look up the expected build pod condition for our current pod phase.
		expectedBuildPodRefPresence := shouldHaveBuildPodRef[phase]

		if outcome = assertMOSBReachedExpectedStateForBuild(ctx, t, cs, mcp, expectedBuildPodRefPresence, expectedMOSBCondition, phase); !outcome {
			return false
		}
	}

	// Find out what happened to the build pod.
	_, err = cs.kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, buildPodName, metav1.GetOptions{})
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

func assertMOSBReachedExpectedStateForBuild(ctx context.Context, t *testing.T, cs *Clients, pool *mcfgv1.MachineConfigPool, expectedToHaveBuildRef bool, expectedMOSBCondition mcfgv1alpha1.BuildProgress, phase interface{}) bool {
	t.Helper()

	checkFunc := func(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild, pool *mcfgv1.MachineConfigPool) bool {
		return mosbReachesExpectedBuildState(pool, mosb, expectedToHaveBuildRef, expectedMOSBCondition)
	}

	msgFunc := func(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild, pool *mcfgv1.MachineConfigPool) string {
		return getMOSBFailureMsg(pool, mosb, expectedToHaveBuildRef, expectedMOSBCondition, phase)
	}

	return assertMachineConfigPoolReachesStateWithMsg(ctx, t, cs, pool.Name, checkFunc, msgFunc)
}

func getMOSBFailureMsg(mcp *mcfgv1.MachineConfigPool, mosb *mcfgv1alpha1.MachineOSBuild, expectedToHaveBuildRef bool, expectedMOSBCondition mcfgv1alpha1.BuildProgress, phase interface{}) string {
	msg := &strings.Builder{}

	fmt.Fprintf(msg, "Has expected condition (%s) for phase (%s)? %v\n", expectedMOSBCondition, phase, apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, expectedMOSBCondition))

	configsEqual := reflect.DeepEqual(mcp.Spec.Configuration, mosb.Status.Conditions)
	fmt.Fprintf(msg, "Spec.Configuration and Status.Configuration equal? %v\n", configsEqual)
	if !configsEqual {
		fmt.Fprintf(msg, "Spec.Configuration: %s\n", spew.Sdump(mcp.Spec.Configuration))
		fmt.Fprintf(msg, "Status.Configuration: %s\n", spew.Sdump(mcp.Status.Configuration))
	}

	return msg.String()
}

// TODO: Fix this to actually return on a check
func mosbReachesExpectedBuildState(mcp *mcfgv1.MachineConfigPool, mosb *mcfgv1alpha1.MachineOSBuild, expectedToHaveBuildRef bool, expectedMOSBCondition mcfgv1alpha1.BuildProgress) bool {

	if !apihelpers.IsMachineOSBuildConditionTrue(mosb.Status.Conditions, expectedMOSBCondition) {
		return false
	}

	return true

}

func newSecret(name string, secretType corev1.SecretType, data []byte) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string][]byte{},
		Type: secretType,
	}

	if secretType == corev1.SecretTypeDockercfg {
		s.Data[corev1.DockerConfigKey] = data
	}

	if secretType == corev1.SecretTypeDockerConfigJson {
		s.Data[corev1.DockerConfigJsonKey] = data
	}

	return s
}
