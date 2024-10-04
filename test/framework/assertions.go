package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
)

const (
	pollInterval time.Duration = time.Millisecond

	// Importing ctrlcommon will cause a circular import issue. To avoid that, we set / use this constant instead.
	mcoNamespace string = "openshift-machine-config-operator"
)

type Assertions struct {
	t            *testing.T
	pollInterval time.Duration
	kubeclient   clientset.Interface
	mcfgclient   mcfgclientset.Interface
}

func AssertClientSet(t *testing.T, pi time.Duration, cs *ClientSet) *Assertions {
	return Assert(t, pi, cs.GetKubeclient(), cs.GetMcfgclient())
}

func Assert(t *testing.T, pi time.Duration, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface) *Assertions {
	return &Assertions{
		t:            t,
		pollInterval: pi,
		kubeclient:   kubeclient,
		mcfgclient:   mcfgclient,
	}
}

type nameableKubeObject interface {
	GetName() string
	runtime.Object
}

func (a *Assertions) SecretIsCreated(ctx context.Context, name string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.Secret, err error) (bool, error) {
		return isCreated(err)
	}

	a.SecretReachesState(ctx, name, stateFunc, msgAndArgs...)
}

func (a *Assertions) SecretIsDeleted(ctx context.Context, name string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.Secret, err error) (bool, error) {
		return isDeleted(err)
	}

	a.SecretReachesState(ctx, name, stateFunc, msgAndArgs...)
}

func (a *Assertions) ConfigMapIsCreated(ctx context.Context, name string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.ConfigMap, err error) (bool, error) {
		return isCreated(err)
	}

	a.ConfigMapReachesState(ctx, name, stateFunc, msgAndArgs...)
}

func (a *Assertions) ConfigMapIsDeleted(ctx context.Context, name string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.ConfigMap, err error) (bool, error) {
		return isDeleted(err)
	}

	a.ConfigMapReachesState(ctx, name, stateFunc, msgAndArgs...)
}

func (a *Assertions) BuildPodIsCreated(ctx context.Context, podName string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.Pod, err error) (bool, error) {
		return isCreated(err)
	}

	a.BuildPodReachesState(ctx, podName, stateFunc, msgAndArgs...)
}

func (a *Assertions) BuildPodIsDeleted(ctx context.Context, podName string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.Pod, err error) (bool, error) {
		return isDeleted(err)
	}

	a.BuildPodReachesState(ctx, podName, stateFunc, msgAndArgs...)
}

func (a *Assertions) MachineOSConfigIsCreated(ctx context.Context, mosb *mcfgv1alpha1.MachineOSConfig, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *mcfgv1alpha1.MachineOSConfig, err error) (bool, error) {
		return isCreated(err)
	}

	a.MachineOSConfigReachesState(ctx, mosb, stateFunc, msgAndArgs...)
}

func (a *Assertions) MachineOSConfigIsDeleted(ctx context.Context, mosb *mcfgv1alpha1.MachineOSConfig, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *mcfgv1alpha1.MachineOSConfig, err error) (bool, error) {
		return isDeleted(err)
	}

	a.MachineOSConfigReachesState(ctx, mosb, stateFunc, msgAndArgs...)
}

func (a *Assertions) MachineOSBuildIsFailure(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.machineOSBuildHasConditionTrue(ctx, mosb, mcfgv1alpha1.MachineOSBuildFailed, msgAndArgs...)
}

func (a *Assertions) MachineOSBuildIsSuccessful(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.machineOSBuildHasConditionTrue(ctx, mosb, mcfgv1alpha1.MachineOSBuildSucceeded, msgAndArgs...)
}

func (a *Assertions) MachineOSBuildIsRunning(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.machineOSBuildHasConditionTrue(ctx, mosb, mcfgv1alpha1.MachineOSBuilding, msgAndArgs...)
}

func (a *Assertions) MachineOSBuildIsCreated(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		return isCreated(err)
	}

	a.MachineOSBuildReachesState(ctx, mosb, stateFunc, msgAndArgs...)
}

func (a *Assertions) MachineOSBuildIsDeleted(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		return isDeleted(err)
	}

	a.MachineOSBuildReachesState(ctx, mosb, stateFunc, msgAndArgs...)
}

func (a *Assertions) SecretReachesState(ctx context.Context, name string, stateFunc func(*corev1.Secret, error) (bool, error), msgAndArgs ...interface{}) {
	err := wait.PollImmediateInfiniteWithContext(ctx, a.pollInterval, func(ctx context.Context) (bool, error) {
		secret, err := a.kubeclient.CoreV1().Secrets(mcoNamespace).Get(ctx, name, metav1.GetOptions{})
		return stateFunc(secret, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("Secret %s did not reach specified state", name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

func (a *Assertions) ConfigMapReachesState(ctx context.Context, name string, stateFunc func(*corev1.ConfigMap, error) (bool, error), msgAndArgs ...interface{}) {
	err := wait.PollImmediateInfiniteWithContext(ctx, a.pollInterval, func(ctx context.Context) (bool, error) {
		cm, err := a.kubeclient.CoreV1().ConfigMaps(mcoNamespace).Get(ctx, name, metav1.GetOptions{})
		return stateFunc(cm, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("ConfigMap %s did not reach specified state", name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

func (a *Assertions) BuildPodReachesState(ctx context.Context, podName string, stateFunc func(*corev1.Pod, error) (bool, error), msgAndArgs ...interface{}) {
	err := wait.PollImmediateInfiniteWithContext(ctx, a.pollInterval, func(ctx context.Context) (bool, error) {
		pod, err := a.kubeclient.CoreV1().Pods(mcoNamespace).Get(ctx, podName, metav1.GetOptions{})
		return stateFunc(pod, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("Build pod %s did not reach specified state", podName), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

func (a *Assertions) MachineOSConfigReachesState(ctx context.Context, mosc *mcfgv1alpha1.MachineOSConfig, stateFunc func(*mcfgv1alpha1.MachineOSConfig, error) (bool, error), msgAndArgs ...interface{}) {
	err := wait.PollImmediateInfiniteWithContext(ctx, a.pollInterval, func(ctx context.Context) (bool, error) {
		apiMosc, err := a.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		return stateFunc(apiMosc, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineOSConfig %s did not reach specified state", mosc.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

func (a *Assertions) MachineOSBuildReachesState(ctx context.Context, mosc *mcfgv1alpha1.MachineOSBuild, stateFunc func(*mcfgv1alpha1.MachineOSBuild, error) (bool, error), msgAndArgs ...interface{}) {
	err := wait.PollImmediateInfiniteWithContext(ctx, a.pollInterval, func(ctx context.Context) (bool, error) {
		apiMosc, err := a.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Get(ctx, mosc.Name, metav1.GetOptions{})
		return stateFunc(apiMosc, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineOSBuild %s did not reach specified state", mosc.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

func (a *Assertions) MachineConfigPoolReachesState(ctx context.Context, mcp *mcfgv1.MachineConfigPool, stateFunc func(*mcfgv1.MachineConfigPool, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	err := wait.PollImmediateInfiniteWithContext(ctx, a.pollInterval, func(ctx context.Context) (bool, error) {
		apiMCP, err := a.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, mcp.Name, metav1.GetOptions{})
		return stateFunc(apiMCP, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineConfigPool %s did not reach specified state", mcp.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

func (a *Assertions) machineOSBuildHasConditionTrue(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild, condition mcfgv1alpha1.BuildProgress, msgAndArgs ...interface{}) {
	a.t.Helper()

	stateFunc := func(apiMosb *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		if err != nil {
			return false, err
		}

		return apihelpers.IsMachineOSBuildConditionTrue(apiMosb.Status.Conditions, condition), nil
	}

	a.MachineOSBuildReachesState(ctx, mosb, stateFunc, msgAndArgs...)
}

func ObjectHasAnnotation(obj metav1.Object, key, val string) bool {
	return mapHasKey(key, val, obj.GetAnnotations())
}

func ObjectHasLabel(obj metav1.Object, key, val string) bool {
	return mapHasKey(key, val, obj.GetLabels())
}

func mapHasKey(key, val string, inMap map[string]string) bool {
	result, ok := inMap[key]
	return ok && val == result
}

func isCreated(err error) (bool, error) {
	if err == nil {
		return true, nil
	}

	if k8serrors.IsNotFound(err) {
		return false, nil
	}

	return false, err
}

func isDeleted(err error) (bool, error) {
	if err == nil {
		return false, nil
	}

	if k8serrors.IsNotFound(err) {
		return true, nil
	}

	return false, err
}

func prefixMsgAndArgs(item interface{}, items []interface{}) []interface{} {
	return append([]interface{}{item}, items...)
}
