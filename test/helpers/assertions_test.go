package helpers

import (
	"context"
	"testing"
	"time"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	clientset "k8s.io/client-go/kubernetes"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
)

type mockTesting struct {
	failed          bool
	isFailNowCalled bool
	isHelperCalled  bool
	isErrorfCalled  bool
	cleanupFunc     func()
	errorfFormat    string
	errorfArgs      []interface{}
}

func (m *mockTesting) Errorf(format string, args ...interface{}) {
	m.isErrorfCalled = true
	m.errorfFormat = format
	m.errorfArgs = args
}

func (m *mockTesting) Helper()          { m.isHelperCalled = true }
func (m *mockTesting) FailNow()         { m.isFailNowCalled = true }
func (m *mockTesting) Cleanup(f func()) { m.cleanupFunc = f }
func (m *mockTesting) Failed() bool     { return m.failed }

// For now, this test will primarily concern itself with whether the various
// objects on the Assertion struct are set correctly. This is because failed
// assertions will cause the test suite to fail.
func TestAssertions(t *testing.T) {
	t.Parallel()

	t.Run("Reaches the desired state with Now", func(t *testing.T) {
		t.Parallel()

		a, mock, _, _ := getAssertionsForTest()
		start := time.Now()

		a = a.Now()
		assert.False(t, a.poll)
		assert.Nil(t, a.ctx)
		assert.Equal(t, a.timeout, time.Duration(0))

		a.PodDoesNotExist("nonexistent-pod")
		assert.False(t, mock.isFailNowCalled)
		assert.True(t, time.Since(start) <= time.Millisecond)
	})

	t.Run("Reaches the desired state in a single attempt", func(t *testing.T) {
		t.Parallel()

		a, mock, _, _ := getAssertionsForTest()
		start := time.Now()

		a = a.WithMaxAttempts(1)
		assert.True(t, a.poll)
		assert.Nil(t, a.ctx)
		assert.Equal(t, a.timeout, time.Duration(0))
		assert.Equal(t, a.maxAttempts, 1)

		a.PodDoesNotExist("nonexistent-pod")
		assert.False(t, mock.isFailNowCalled)
		assert.True(t, time.Since(start) <= time.Millisecond)
		assert.Equal(t, a.pollCount, 1)
	})

	t.Run("Fails immediately with Now", func(t *testing.T) {
		t.Parallel()

		a, mock, _, _ := getAssertionsForTest()
		start := time.Now()

		a = a.Now()
		assert.False(t, a.poll)
		assert.Nil(t, a.ctx)
		assert.Equal(t, a.timeout, time.Duration(0))

		a.PodExists("nonexistent-pod")
		assert.True(t, mock.isFailNowCalled)
		assert.True(t, time.Since(start) <= time.Millisecond)
	})

	t.Run("Polls with timeout", func(t *testing.T) {
		t.Parallel()

		timeout := time.Millisecond * 5
		a, mock, _, _ := getAssertionsForTest()
		start := time.Now()
		a = a.WithTimeout(timeout)
		assert.Equal(t, a.timeout, timeout)
		assert.True(t, a.poll)
		assert.Nil(t, a.ctx)

		a.PodExists("nonexistent-pod")
		assert.True(t, time.Since(start) >= timeout)
		assert.True(t, mock.isFailNowCalled)
	})

	t.Run("Polls at interval with timeout", func(t *testing.T) {
		t.Parallel()

		interval := time.Millisecond

		a, mock, _, _ := getAssertionsForTest()

		a = a.WithPollInterval(interval)
		assert.Equal(t, a.pollInterval, interval)
		assert.True(t, a.poll)

		a = a.WithTimeout(time.Millisecond * 5)
		assert.Equal(t, a.pollInterval, interval)
		assert.Equal(t, a.timeout, time.Millisecond*5)

		a.PodExists("nonexistent-pod")
		assert.True(t, mock.isFailNowCalled)
	})

	t.Run("Polls at interval with max attempts reached", func(t *testing.T) {
		t.Parallel()

		interval := time.Millisecond

		a, mock, _, _ := getAssertionsForTest()

		a = a.WithPollInterval(interval)
		assert.Equal(t, a.pollInterval, interval)
		assert.True(t, a.poll)

		a = a.WithTimeout(time.Millisecond * 5)
		assert.Equal(t, a.pollInterval, interval)
		assert.Equal(t, a.timeout, time.Millisecond*5)

		a = a.WithMaxAttempts(2)
		assert.Equal(t, a.maxAttempts, 2)
		assert.True(t, a.keepCount)
		assert.Equal(t, a.pollInterval, interval)
		assert.True(t, a.poll)

		a.PodExists("nonexistent-pod")

		assert.True(t, mock.isFailNowCalled)
		assert.Equal(t, 2, a.pollCount)
	})

	t.Run("Runs and is canceled with a context", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*5)
		go func() {
			time.Sleep(time.Millisecond * 5)
			cancel()
		}()

		a, mock, _, _ := getAssertionsForTest()
		start := time.Now()

		a = a.WithContext(ctx)
		assert.Equal(t, a.ctx, ctx)

		a = a.WithTimeout(time.Millisecond * 10)
		assert.Equal(t, a.timeout, time.Millisecond*10)

		a.PodExists("nonexistent-pod")

		assert.True(t, time.Since(start) > time.Millisecond*4)
		assert.True(t, time.Since(start) < time.Millisecond*10)
		assert.True(t, mock.isFailNowCalled)
	})
}

func TestAssertionsChains(t *testing.T) {
	t.Parallel()

	t.Run("Are immutable", func(t *testing.T) {
		t.Parallel()

		a, _, _ := getAssertionsForTestWithRealT(t)

		b := a.WithMaxAttempts(5)
		assert.Equal(t, 0, a.maxAttempts)
		assert.Equal(t, 5, b.maxAttempts)
		assert.NotEqual(t, a, b)
	})

	t.Run("Preserve polling options as each new option is added", func(t *testing.T) {
		t.Parallel()

		a, _, _ := getAssertionsForTestWithRealT(t)

		a = a.WithMaxAttempts(5)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.poll)

		a = a.WithPollInterval(time.Millisecond)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.poll)
		assert.Equal(t, time.Millisecond, a.pollInterval)

		a = a.WithMaxAttempts(5)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.poll)
		assert.Equal(t, time.Millisecond, a.pollInterval)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.keepCount)

		a = a.WithTimeout(time.Millisecond * 5)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.poll)
		assert.Equal(t, time.Millisecond, a.pollInterval)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.keepCount)
		assert.Equal(t, time.Millisecond*5, a.timeout)

		a = a.Eventually()
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.poll)
		assert.Equal(t, time.Millisecond, a.pollInterval)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.keepCount)
		assert.Equal(t, time.Millisecond*5, a.timeout)

		ctx := context.Background()

		a = a.WithContext(ctx)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.poll)
		assert.Equal(t, time.Millisecond, a.pollInterval)
		assert.Equal(t, 5, a.maxAttempts)
		assert.Equal(t, time.Millisecond*5, a.timeout)
		assert.True(t, a.keepCount)
		assert.Equal(t, ctx, a.ctx)
	})

	t.Run("Poll counts are not preserved but max attempts are", func(t *testing.T) {
		t.Parallel()

		a, _, _ := getAssertionsForTestWithRealT(t)

		a = a.WithMaxAttempts(5)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.poll)
		assert.True(t, a.keepCount)

		a.pollCount = 3

		a = a.WithTimeout(time.Millisecond)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.poll)
		assert.True(t, a.keepCount)
		assert.Equal(t, 0, a.pollCount)
	})

	t.Run("Now clears all poll options", func(t *testing.T) {
		t.Parallel()

		a, _, _ := getAssertionsForTestWithRealT(t)

		a = a.Eventually().WithTimeout(time.Millisecond).WithMaxAttempts(5).Now()
		assert.Equal(t, 0, a.maxAttempts)
		assert.False(t, a.keepCount)
		assert.False(t, a.poll)
	})

	t.Run("Now clears all poll options but keeps context", func(t *testing.T) {
		t.Parallel()

		a, _, _ := getAssertionsForTestWithRealT(t)

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		a = a.Eventually().WithTimeout(time.Millisecond).WithMaxAttempts(5).WithContext(ctx).Now()

		assert.Equal(t, 0, a.maxAttempts)
		assert.False(t, a.keepCount)
		assert.False(t, a.poll)
		assert.Equal(t, a.ctx, ctx)
	})

	t.Run("Eventually overrides Now but keeps context", func(t *testing.T) {
		t.Parallel()

		a, _, _ := getAssertionsForTestWithRealT(t)

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		a = a.Now().Eventually().WithTimeout(time.Millisecond).WithMaxAttempts(5).WithContext(ctx)
		assert.Equal(t, 5, a.maxAttempts)
		assert.True(t, a.keepCount)
		assert.True(t, a.poll)
		assert.Equal(t, a.ctx, ctx)
	})
}

func getAssertionsForTest() (*Assertions, *mockTesting, clientset.Interface, mcfgclientset.Interface) {
	mock := &mockTesting{}
	kubeclient := fakecorev1client.NewSimpleClientset()
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	a := Assert(mock, kubeclient, mcfgclient)

	return a, mock, kubeclient, mcfgclient
}

func getAssertionsForTestWithRealT(t *testing.T) (*Assertions, clientset.Interface, mcfgclientset.Interface) {
	kubeclient := fakecorev1client.NewSimpleClientset()
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	return Assert(t, kubeclient, mcfgclient), kubeclient, mcfgclient
}
