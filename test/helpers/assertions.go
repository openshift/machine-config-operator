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
	batchv1 "k8s.io/api/batch/v1"
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
	// Keep count of how many times we've polled.
	keepCount bool
	// How many times we've polled.
	pollCount int
	// Maximum attempts before we return an error.
	maxAttempts int
}

// Using this allows us to mock *testing.T for tests for this implementation.
type TestingT interface {
	assert.TestingT
	Helper()
	FailNow()
	Cleanup(func())
	Failed() bool
}

// Instantiates the Assertions struct using a ClientSet object.
func AssertClientSet(t TestingT, cs *framework.ClientSet) *Assertions {
	return Assert(t, cs.GetKubeclient(), cs.GetMcfgclient())
}

// Instantiates the Assertions struct using the provided clients.
func Assert(t TestingT, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface) *Assertions {
	return newAssertions(t, kubeclient, mcfgclient)
}

// Constructs an Assertions struct with initialized but zeroed values.
func newAssertions(t TestingT, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface) *Assertions {
	return &Assertions{
		t:           t,
		kubeclient:  kubeclient,
		mcfgclient:  mcfgclient,
		poll:        false,
		keepCount:   false,
		pollCount:   0,
		maxAttempts: 0,
		timeout:     0,
	}
}

// Constructs an Assertions struct with initialized but zeroed values and a context.
func newAssertionsWithContext(ctx context.Context, t TestingT, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface) *Assertions {
	a := newAssertions(t, kubeclient, mcfgclient)
	a.ctx = ctx
	return a
}

// Returns a deep copy of the Assertion struct with zeroed polling values.
func (a *Assertions) deepcopy() *Assertions {
	return newAssertionsWithContext(a.ctx, a.t, a.kubeclient, a.mcfgclient)
}

// Returns a deep copy of the Assertion struct while preserving values related to polling.
func (a *Assertions) deepcopyWithPollingValues() *Assertions {
	newAssertion := a.deepcopy()
	newAssertion.poll = a.poll
	newAssertion.pollInterval = a.pollInterval
	newAssertion.timeout = a.timeout
	newAssertion.keepCount = a.keepCount
	newAssertion.maxAttempts = a.maxAttempts
	return newAssertion
}

func (a *Assertions) WithMaxAttempts(i int) *Assertions {
	newAssertion := a.deepcopyWithPollingValues()
	newAssertion.keepCount = true
	newAssertion.pollCount = 0
	newAssertion.maxAttempts = i
	newAssertion.poll = true
	return newAssertion
}

// Sets a timeout.
func (a *Assertions) WithTimeout(i time.Duration) *Assertions {
	newAssertion := a.deepcopyWithPollingValues()
	newAssertion.timeout = i
	newAssertion.poll = true
	return newAssertion
}

// Sets a polling interval. In the absence of a polling interval, this will
// default to one second.
func (a *Assertions) WithPollInterval(i time.Duration) *Assertions {
	newAssertion := a.deepcopyWithPollingValues()
	newAssertion.pollInterval = i
	newAssertion.poll = true
	return newAssertion
}

// Will poll until the desired state or timeout has been met.
func (a *Assertions) Eventually() *Assertions {
	newAssertion := a.deepcopyWithPollingValues()
	newAssertion.poll = true
	return newAssertion
}

// Will not poll and will fail immediately if the current state does not equal
// the desired state.
func (a *Assertions) Now() *Assertions {
	return a.deepcopy()
}

// Supplies a context for use on all API queries.
func (a *Assertions) WithContext(ctx context.Context) *Assertions {
	newAssertion := a.deepcopyWithPollingValues()
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

// Asserts that a Pod is running.
func (a *Assertions) PodIsRunning(podName string, msgAndArgs ...interface{}) {
	a.t.Helper()

	isPodRunning := func(pod *corev1.Pod) bool {
		if pod == nil {
			return false
		}

		return pod.Status.Phase == corev1.PodRunning
	}

	isPodContainersRunning := func(pod *corev1.Pod) bool {
		if pod == nil {
			return false
		}
		for _, ctr := range pod.Status.ContainerStatuses {
			if ctr.State.Running == nil || ctr.State.Waiting != nil || ctr.State.Terminated != nil {
				return false
			}
		}
		return true
	}
	stateFunc := func(pod *corev1.Pod, err error) (bool, error) {
		if a.poll {
			exists, err := a.created(err)
			if exists && err == nil {
				return isPodRunning(pod) && isPodContainersRunning(pod), nil
			}
			return exists, err
		}
		return err == nil, err
	}

	a.podReachesState(podName, stateFunc, msgAndArgs...)
}

// Asserts that a Job is created.
func (a *Assertions) JobExists(jobName string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *batchv1.Job, err error) (bool, error) {
		return a.created(err)
	}

	a.jobReachesState(jobName, stateFunc, msgAndArgs...)
}

// Asserts that a Job is deleted.
func (a *Assertions) JobDoesNotExist(jobName string, msgAndArgs ...interface{}) {
	a.t.Helper()
	stateFunc := func(_ *batchv1.Job, err error) (bool, error) {
		return a.deleted(err)
	}

	a.jobReachesState(jobName, stateFunc, msgAndArgs...)
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

// Asserts that a MachineOSBuild is interrupted.
func (a *Assertions) MachineOSBuildIsInterrupted(mosb *mcfgv1alpha1.MachineOSBuild, msgAndArgs ...interface{}) {
	a.machineOSBuildHasConditionTrue(mosb, mcfgv1alpha1.MachineOSBuildInterrupted, msgAndArgs...)
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

	err := wait.PollUntilContextCancel(ctx, a.getPollInterval(), true, func(ctx context.Context) (bool, error) {
		secret, err := a.kubeclient.CoreV1().Secrets(mcoNamespace).Get(ctx, name, metav1.GetOptions{})
		return a.handleStateFuncResult(stateFunc(secret, err))
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("Secret %s did not reach specified state", name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a ConfigMap reaches the desired state.
func (a *Assertions) configMapReachesState(name string, stateFunc func(*corev1.ConfigMap, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollUntilContextCancel(ctx, a.getPollInterval(), true, func(ctx context.Context) (bool, error) {
		cm, err := a.kubeclient.CoreV1().ConfigMaps(mcoNamespace).Get(ctx, name, metav1.GetOptions{})
		return a.handleStateFuncResult(stateFunc(cm, err))
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("ConfigMap %s did not reach specified state", name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a Pod reaches the desired state.
func (a *Assertions) podReachesState(podName string, stateFunc func(*corev1.Pod, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollUntilContextCancel(ctx, a.getPollInterval(), true, func(ctx context.Context) (bool, error) {
		pod, err := a.kubeclient.CoreV1().Pods(mcoNamespace).Get(ctx, podName, metav1.GetOptions{})
		return a.handleStateFuncResult(stateFunc(pod, err))
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("Build pod %s did not reach specified state", podName), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a Job reaches the desired state.
func (a *Assertions) jobReachesState(jobName string, stateFunc func(*batchv1.Job, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollUntilContextCancel(ctx, a.getPollInterval(), true, func(ctx context.Context) (bool, error) {
		pod, err := a.kubeclient.BatchV1().Jobs(mcoNamespace).Get(ctx, jobName, metav1.GetOptions{})
		return a.handleStateFuncResult(stateFunc(pod, err))
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("Build job %s did not reach specified state", jobName), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a MachineOSConfig reaches the desired state.
func (a *Assertions) machineOSConfigReachesState(mosc *mcfgv1alpha1.MachineOSConfig, stateFunc func(*mcfgv1alpha1.MachineOSConfig, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollUntilContextCancel(ctx, a.getPollInterval(), true, func(ctx context.Context) (bool, error) {
		apiMosc, err := a.mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Get(ctx, mosc.Name, metav1.GetOptions{})
		return a.handleStateFuncResult(stateFunc(apiMosc, err))
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineOSConfig %s did not reach specified state", mosc.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a MachineOSBuild reaches the desired state.
func (a *Assertions) machineOSBuildReachesState(mosc *mcfgv1alpha1.MachineOSBuild, stateFunc func(*mcfgv1alpha1.MachineOSBuild, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollUntilContextCancel(ctx, a.getPollInterval(), true, func(ctx context.Context) (bool, error) {
		apiMosc, err := a.mcfgclient.MachineconfigurationV1alpha1().MachineOSBuilds().Get(ctx, mosc.Name, metav1.GetOptions{})
		return a.handleStateFuncResult(stateFunc(apiMosc, err))
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineOSBuild %s did not reach specified state", mosc.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Asserts that a MachineConfigPool reaches the desired state.
func (a *Assertions) MachineConfigPoolReachesState(mcp *mcfgv1.MachineConfigPool, stateFunc func(*mcfgv1.MachineConfigPool, error) (bool, error), msgAndArgs ...interface{}) {
	a.t.Helper()

	ctx, cancel := a.getContextAndCancel()
	defer cancel()

	err := wait.PollUntilContextCancel(ctx, a.getPollInterval(), true, func(ctx context.Context) (bool, error) {
		apiMCP, err := a.mcfgclient.MachineconfigurationV1().MachineConfigPools().Get(ctx, mcp.Name, metav1.GetOptions{})
		return a.handleStateFuncResult(stateFunc(apiMCP, err))
	})

	msgAndArgs = prefixMsgAndArgs(fmt.Sprintf("MachineConfigPool %s did not reach specified state", mcp.Name), msgAndArgs)
	require.NoError(a.t, err, msgAndArgs...)
}

// Parses the statefunc results, potentially injecting an error if we've
// reached the max count.
func (a *Assertions) handleStateFuncResult(b bool, e error) (bool, error) {
	// If we're not keeping count, just return the results, as-is.
	if !a.keepCount {
		return b, e
	}

	// If we're keeping count but we have not reached the maximum tries yet, return the results as-is.
	if !a.isMaxCountReached() {
		return b, e
	}

	// If we've reached the maximum number of tries, inject an error.
	return false, fmt.Errorf("max attempts %d has been reached", a.maxAttempts)
}

// Determines if the maximum number of attempts has been reached.
func (a *Assertions) isMaxCountReached() bool {
	// If we're not keeping count, just return false.
	if !a.keepCount {
		return false
	}

	// Determine if we've reached our maximum.
	result := a.maxAttempts <= a.pollCount

	// If not, increment the counter.
	if !result {
		a.pollCount++
	}

	// Return the result.
	return result
}

// Creates a new context using either the parent context or the background
// context as well as a default or provided timeout value.
func (a *Assertions) getContextAndCancel() (context.Context, func()) {
	if a.ctx == nil {
		if a.timeout == 0 {
			// If no context or timeout is provided, use the background context.
			return context.WithTimeout(context.Background(), time.Minute*5)
		}

		// If no context was provided, but a timeout was, use the background context
		// and apply the provided timeout to it.
		return context.WithTimeout(context.Background(), a.timeout)
	}

	// If a timeout and a parent context were provided, construct a child
	// context with the provided parent context and set the timeout on the child
	// context equal to the provided timeout value.
	//
	// If the parent context has a lower timeout value than provided here,
	// cancelation will propagate to the children.
	if a.timeout != 0 {
		return context.WithTimeout(a.ctx, a.timeout)
	}

	// If a context is provided, but no timeout is found, don't use the default
	// timeout value because the supplied context potentially has a higher
	// timeout or longer deadline than our default. Note: In this mode, the onus
	// is on the caller to provide a context with a timeout or deadline because
	// otherwise, this will poll infinitely.
	return context.WithCancel(a.ctx)
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
