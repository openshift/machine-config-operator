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
	isFailNowCalled bool
	isHelperCalled  bool
	isErrorfCalled  bool
}

func (m *mockTesting) Errorf(format string, args ...interface{}) { m.isErrorfCalled = true }
func (m *mockTesting) Helper()                                   { m.isHelperCalled = true }
func (m *mockTesting) FailNow()                                  { m.isFailNowCalled = true }

// For now, this test will primarily concern itself with whether the various
// objects on the Assertion struct are set correctly. This is because failed
// assertions will cause the test suite to fail.
func TestAssertions(t *testing.T) {
	t.Parallel()

	t.Run("Fails immediately with Now", func(t *testing.T) {
		a, mock, _, _ := getAssertionsForTest()
		start := time.Now()
		a.Now().PodExists("nonexistent-pod")
		assert.True(t, mock.isFailNowCalled)
		assert.True(t, time.Since(start) <= time.Millisecond)
	})

	t.Run("Polls with timeout", func(t *testing.T) {
		a, mock, _, _ := getAssertionsForTest()
		start := time.Now()
		a.WithTimeout(time.Millisecond * 5).PodExists("nonexistent-pod")
		assert.True(t, time.Since(start) >= time.Millisecond*5)
		assert.True(t, mock.isFailNowCalled)
	})

	t.Run("Runs and is canceled with a context", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*5)
		go func() {
			time.Sleep(time.Millisecond * 5)
			cancel()
		}()

		a, mock, _, _ := getAssertionsForTest()
		start := time.Now()
		a.WithContext(ctx).WithTimeout(time.Millisecond * 10).PodExists("nonexistent-pod")
		assert.True(t, time.Since(start) > time.Millisecond*4)
		assert.True(t, time.Since(start) < time.Millisecond*10)
		assert.True(t, mock.isFailNowCalled)
	})
}

func getAssertionsForTest() (*Assertions, *mockTesting, clientset.Interface, mcfgclientset.Interface) {
	mock := &mockTesting{}
	kubeclient := fakecorev1client.NewSimpleClientset()
	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset()
	a := Assert(mock, kubeclient, mcfgclient)

	return a, mock, kubeclient, mcfgclient
}
