package helpers

import (
	"context"
	"fmt"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
)

const (
	// Importing ctrlcommon will cause a circular import issue. To avoid that, we set / use this constant instead.
	mcoNamespace string = "openshift-machine-config-operator"
)

// Holds the basic items needed to perform an assertion on a Kube object. Each
// assertion that is part of this struct will poll until either the desired
// state is reached, or the provided context is cancelled.
type Assertions struct {
	// The testing object
	t TestingT
	// t *testing.T
	// The polling interval to use (default: one second)
	pollInterval time.Duration
	// The max time to poll for
	timeout time.Duration
	// Kubeclient; may be real or fake.
	kubeclient clientset.Interface
	// Mcfgclient; may be real or fake.
	mcfgclient mcfgclientset.Interface
	// The context to use for all API requests.
	ctx context.Context
	// Should we poll for results or use the first result we get.
	poll bool
}

// Using this allows us to mock *testing.T for tests for this implementation.
type TestingT interface {
	assert.TestingT
	Helper()
	FailNow()
}

// Instantiates the Assertions struct using a ClientSet object.
func AssertClientSet(t TestingT, cs *framework.ClientSet) *Assertions {
	return Assert(t, cs.GetKubeclient(), cs.GetMcfgclient())
}

// Instantiates the Assertions struct using the provided clients.
func Assert(t TestingT, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface) *Assertions {
	return &Assertions{
		t:          t,
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		ctx:        context.Background(),
		poll:       false,
	}
}

// Sets a timeout.
func (a *Assertions) WithTimeout(i time.Duration) *Assertions {
	newAssertion := a.deepcopy()
	newAssertion.timeout = i
	newAssertion.poll = true
	return newAssertion
}

// Sets a polling interval. In the absence of a polling interval, this will
// default to one second.
func (a *Assertions) WithPollInterval(i time.Duration) *Assertions {
	newAssertion := a.deepcopy()
	newAssertion.pollInterval = i
	newAssertion.poll = true
	return newAssertion
}

// Will poll until the desired state or timeout has been met.
func (a *Assertions) Eventually() *Assertions {
	newAssertion := a.deepcopy()
	newAssertion.poll = true
	return newAssertion
}

// Will not poll and will fail immediately if the current state does not equal
// the desired state.
func (a *Assertions) Now() *Assertions {
	newAssertion := a.deepcopy()
	newAssertion.poll = false
	return newAssertion
}

// Supplies a context for use on all API queries.
func (a *Assertions) WithContext(ctx context.Context) *Assertions {
	newAssertion := a.deepcopy()
	newAssertion.ctx = ctx
	return newAssertion
}

// Asserts that a Secret is created.
func (a *Assertions) SecretExists(name string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.Secret, err error) (bool, error) {
		return a.created(err)
	}

	a.secretReachesState(name, stateFunc, msgAndArgs...)
}

// Asserts that a Secret is deleted.
func (a *Assertions) SecretDoesNotExist(name string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.Secret, err error) (bool, error) {
		return a.deleted(err)
	}

	a.secretReachesState(name, stateFunc, msgAndArgs...)
}

// Asserts that a ConfigMap is created.
func (a *Assertions) ConfigMapExists(name string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.ConfigMap, err error) (bool, error) {
		return a.created(err)
	}

	a.configMapReachesState(name, stateFunc, msgAndArgs...)
}

// Asserts that a ConfigMap is deleted.
func (a *Assertions) ConfigMapDoesNotExist(name string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.ConfigMap, err error) (bool, error) {
		return a.deleted(err)
	}

	a.configMapReachesState(name, stateFunc, msgAndArgs...)
}

// Asserts that a Pod is created.
func (a *Assertions) PodExists(podName string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.Pod, err error) (bool, error) {
		return a.created(err)
	}

	a.podReachesState(podName, stateFunc, msgAndArgs...)
}

// Asserts that a Pod is deleted.
func (a *Assertions) PodDoesNotExist(podName string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *corev1.Pod, err error) (bool, error) {
		return a.deleted(err)
	}

	a.podReachesState(podName, stateFunc, msgAndArgs...)
}

// Asserts that a MachineOSConfig is created.
func (a *Assertions) MachineOSConfigExists(mosb *mcfgv1alpha1.MachineOSConfig, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *mcfgv1alpha1.MachineOSConfig, err error) (bool, error) {
		return a.created(err)
	}

	a.machineOSConfigReachesState(mosb, stateFunc, msgAndArgs...)
}

// Asserts that a MachineOSConfig is deleted.
func (a *Assertions) MachineOSConfigDoesNotExist(mosb *mcfgv1alpha1.MachineOSConfig, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *mcfgv1alpha1.MachineOSConfig, err error) (bool, error) {
		return a.deleted(err)
	}

	a.machineOSConfigReachesState(mosb, stateFunc, msgAndArgs...)
}

// Asserts that a MachineOSBuild has failed.
func (a *Assertions) MachineOSBuildIsFailure(mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.machineOSBuildHasConditionTrue(mosb, mcfgv1alpha1.MachineOSBuildFailed, msgAndArgs...)
}

// Asserts that a MachineOSBuild has succeeded.
func (a *Assertions) MachineOSBuildIsSuccessful(mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.machineOSBuildHasConditionTrue(mosb, mcfgv1alpha1.MachineOSBuildSucceeded, msgAndArgs...)
}

// Asserts that a MachineOSBuild is running.
func (a *Assertions) MachineOSBuildIsRunning(mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.machineOSBuildHasConditionTrue(mosb, mcfgv1alpha1.MachineOSBuilding, msgAndArgs...)
}

// Asserts that a MachineOSBuild is created.
func (a *Assertions) MachineOSBuildExists(mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		return a.created(err)
	}

	a.machineOSBuildReachesState(mosb, stateFunc, msgAndArgs...)
}

// Asserts that a MachineOSBuild is deleted.
func (a *Assertions) MachineOSBuildDoesNotExist(mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		return a.deleted(err)
	}

	a.machineOSBuildReachesState(mosb, stateFunc, msgAndArgs...)
}

// Asserts that a Secret reaches the desired state.
func (a *Assertions) secretReachesState(name string, stateFunc func(*corev1.Secret, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollImmediateInfiniteWithContext(ctx, a.getPollInterval(), func(ctx context.Context) (bool, error) {
		secret, err := a.kubeclient.CoreV1().Secrets(mcoNamespace).Get(ctx, name, metav1.GetOptions{})
		return stateFunc(secret, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("Secret %s did not reach specified state", name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a ConfigMap reaches the desired state.
func (a *Assertions) configMapReachesState(name string, stateFunc func(*corev1.ConfigMap, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollImmediateInfiniteWithContext(ctx, a.getPollInterval(), func(ctx context.Context) (bool, error) {
		cm, err := a.kubeclient.CoreV1().ConfigMaps(mcoNamespace).Get(ctx, name, metav1.GetOptions{})
		return stateFunc(cm, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("ConfigMap %s did not reach specified state", name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a Pod reaches the desired state.
func (a *Assertions) podReachesState(podName string, stateFunc func(*corev1.Pod, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollImmediateInfiniteWithContext(ctx, a.getPollInterval(), func(ctx context.Context) (bool, error) {
		pod, err := a.kubeclient.CoreV1().Pods(mcoNamespace).Get(ctx, podName, metav1.GetOptions{})
		return stateFunc(pod, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("Build pod %s did not reach specified state", podName), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a MachineOSConfig reaches the desired state.
func (a *Assertions) machineOSConfigReachesState(mosc *mcfgv1alpha1.MachineOSConfig, stateFunc func(*mcfgv1alpha1.MachineOSConfig, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollImmediateInfiniteWithContext(ctx, a.getPollInterval(), func(ctx context.Context) (bool, error) {
		apiMosc, err := a.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		return stateFunc(apiMosc, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineOSConfig %s did not reach specified state", mosc.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a MachineOSBuild reaches the desired state.
func (a *Assertions) machineOSBuildReachesState(mosc *mcfgv1alpha1.MachineOSBuild, stateFunc func(*mcfgv1alpha1.MachineOSBuild, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollImmediateInfiniteWithContext(ctx, a.getPollInterval(), func(ctx context.Context) (bool, error) {
		apiMosc, err := a.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Get(ctx, mosc.Name, metav1.GetOptions{})
		return stateFunc(apiMosc, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineOSBuild %s did not reach specified state", mosc.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a MachineConfigPool reaches the desired state.
func (a *Assertions) MachineConfigPoolReachesState(mcp *mcfgv1.MachineConfigPool, stateFunc func(*mcfgv1.MachineConfigPool, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollImmediateInfiniteWithContext(ctx, a.getPollInterval(), func(ctx context.Context) (bool, error) {
		apiMCP, err := a.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, mcp.Name, metav1.GetOptions{})
		return stateFunc(apiMCP, err)
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineConfigPool %s did not reach specified state", mcp.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Creates a new context using either the parent context or the background
// context.
func (a *Assertions) getContextAndCancel() (context.Context, func()) {
	timeout := a.getTimeout()

	if a.ctx == nil {
		return context.WithTimeout(context.Background(), timeout)
	}

	return context.WithTimeout(a.ctx, timeout)
}

// Get the timeout value or default to five minutes.
func (a *Assertions) getTimeout() time.Duration {
	if a.timeout != 0 {
		return a.timeout
	}

	return time.Minute * 5
}

// Determines if the MachineOSBuild has reached the desired state.
func (a *Assertions) machineOSBuildHasConditionTrue(mosb *mcfgv1alpha1.MachineOSBuild, condition mcfgv1alpha1.BuildProgress, msgAndArgs ...interface{}) {
	a.t.Helper()

	stateFunc := func(apiMosb *mcfgv1alpha1.MachineOSBuild, err error) (bool, error) {
		if err != nil {
			return false, err
		}

		isReached := apihelpers.IsMachineOSBuildConditionTrue(apiMosb.Status.Conditions, condition)

		if a.poll {
			return isReached, nil
		}

		if isReached {
			return true, nil
		}

		return false, fmt.Errorf("MachineOSBuild %q condition %q is not true", apiMosb.Name, condition)
	}

	a.machineOSBuildReachesState(mosb, stateFunc, msgAndArgs...)
}

// Gets the polling interval, defaulting to one second if not otherwise set.
func (a *Assertions) getPollInterval() time.Duration {
	if a.pollInterval != 0 {
		return a.pollInterval
	}

	return time.Second
}

// Determines if a given object is deleted based upon the error provided as
// well as whether is to be done..
func (a *Assertions) deleted(err error) (bool, error) {
	if a.poll {
		return isDeleted(err)
	}

	return isDeletedNoPoll(err)
}

// Determines if a given object is created based upon the error provided as
// well as whether polling is to be done.
func (a *Assertions) created(err error) (bool, error) {
	if a.poll {
		return isCreated(err)
	}

	return isCreatedNoPoll(err)
}

// Returns a deep copy of the Assertion struct.
func (a *Assertions) deepcopy() *Assertions {
	return &Assertions{
		t:            a.t,
		pollInterval: a.pollInterval,
		timeout:      a.timeout,
		kubeclient:   a.kubeclient,
		mcfgclient:   a.mcfgclient,
		ctx:          a.ctx,
	}
}

// Determines if an object is created right now. This is determined by the API
// query not returning any errors.
func isCreatedNoPoll(err error) (bool, error) {
	return err == nil, err
}

// Determines if an object is deleted right now. This is determined by the API
// returning an IsNotFound error which is checked for. Any other errors are
// returned to the caller.
func isDeletedNoPoll(err error) (bool, error) {
	if k8serrors.IsNotFound(err) {
		return true, nil
	}

	return false, err
}

// Determines if an object is eventually created. This is more forgiving
// because we avoid returning the IsNotFound error if found. Instead, we return
// false.
func isCreated(err error) (bool, error) {
	if err == nil {
		return true, nil
	}

	if k8serrors.IsNotFound(err) {
		return false, nil
	}

	return false, err
}

// Determines if an object is eventually deleted. This is more forgiving
// because we first check whether the returned error is nil and then check for
// IsNotFound.
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
