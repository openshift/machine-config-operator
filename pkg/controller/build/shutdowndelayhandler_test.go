package build

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	fakecorev1client "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	fakeclock "k8s.io/utils/clock/testing"
)

// Tests with higher granularity for determining whether the shutdown delay is needed.
func TestShutdown(t *testing.T) {
	t.Parallel()

	type expectedObjects struct {
		machineOSConfigs []string
		machineOSBuilds  []string
		jobs             []string
		configmaps       []string
		secrets          []string
	}

	testCases := []struct {
		// Name of testcase
		name string
		// Expected return value of isDelayNeeded method.
		isDelayNeeded bool
		// Test objects to construct for the test.
		shutdownTestObjects
		// The names of the object(s) we expect to get back. If we aren't expecting
		// any objects, this should be nil.
		expected *expectedObjects
	}{
		{
			name: "MachineOSConfigs and MachineOSBuilds not pending deletion",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1"}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
			},
		},
		{
			name: "MachineOSConfig pending deletion with MachineOSBuild not pending",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
			},
			expected: &expectedObjects{
				machineOSBuilds: []string{"mosb-1"},
			},
		},
		{
			name: "MachineOSConfig not pending deletion with MachineOSBuild pending",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1"}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
			},
		},
		{
			name: "MachineOSConfig and MachineOSBuild pending deletions and pending job",
			shutdownTestObjects: shutdownTestObjects{
				allPendingDeletion: true,
				machineOSConfigs:   []shutdownTestObject{{name: "mosc-1"}},
				machineOSBuilds:    []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
				jobs:               []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
		},
		{
			name: "MachineOSConfig not pending deletion with MachineOSBuild pending and job not pending",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1"}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
				jobs:             []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
			expected: &expectedObjects{
				jobs: []string{"job-1"},
			},
		},
		{
			name: "MachineOSConfig and MachineOSBuild pending deletions and job not pending",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
				jobs:             []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
			expected: &expectedObjects{
				jobs: []string{"job-1"},
			},
		},
		{
			name: "MachineOSConfig and MachineOSBuild and job pending deletions and configmap not pending",

			shutdownTestObjects: shutdownTestObjects{
				allEphemeral:     true,
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
				jobs:             []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1", pendingDeletion: true}},
				configmaps:       []shutdownTestObject{{name: "configmap-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"}},
			},
			expected: &expectedObjects{
				configmaps: []string{"configmap-1"},
			},
		},
		{
			name: "MachineOSConfig and MachineOSBuild and job pending deletions and secret not pending",
			shutdownTestObjects: shutdownTestObjects{
				allEphemeral:     true,
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
				jobs:             []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1", pendingDeletion: true, jobName: "job-1"}},
				secrets:          []shutdownTestObject{{name: "secret-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"}},
			},
			expected: &expectedObjects{
				secrets: []string{"secret-1"},
			},
		},
		{
			name: "Orphaned job not pending deletion",
			shutdownTestObjects: shutdownTestObjects{
				jobs: []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
			expected: &expectedObjects{
				jobs: []string{"job-1"},
			},
		},
		{
			name: "Orphaned job pending deletion",
			shutdownTestObjects: shutdownTestObjects{
				jobs: []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1", pendingDeletion: true, ephemeral: true}},
			},
		},
		{
			name: "Orphaned configmap not pending deletion",
			shutdownTestObjects: shutdownTestObjects{
				configmaps: []shutdownTestObject{{name: "configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true, jobName: "job-1"}},
			},
			expected: &expectedObjects{
				configmaps: []string{"configmap-1"},
			},
		},
		{
			name: "Orphaned configmap pending deletion",
			shutdownTestObjects: shutdownTestObjects{
				configmaps: []shutdownTestObject{{name: "configmap-1", moscName: "mosc-1", mosbName: "mosb-1", pendingDeletion: true, ephemeral: true, jobName: "job-1"}},
			},
		},
		{
			name: "Orphaned secret not pending deletion",
			shutdownTestObjects: shutdownTestObjects{
				secrets: []shutdownTestObject{{name: "secret-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true, jobName: "job-1"}},
			},
			expected: &expectedObjects{
				secrets: []string{"secret-1"},
			},
		},
		{
			name: "Orphaned secret pending deletion",
			shutdownTestObjects: shutdownTestObjects{
				secrets: []shutdownTestObject{{name: "secret-1", moscName: "mosc-1", mosbName: "mosb-1", pendingDeletion: true, jobName: "job-1"}},
			},
		},
		{
			name: "Non-ephemeral objects not pending deletion",
			shutdownTestObjects: shutdownTestObjects{
				jobs:       []shutdownTestObject{{name: "job-1"}, {name: "job-2"}},
				configmaps: []shutdownTestObject{{name: "configmap-1"}, {name: "configmap-2"}},
				secrets:    []shutdownTestObject{{name: "secret-1"}, {name: "secret-2"}},
			},
		},
		{
			name: "Child objects are not orphaned",
			shutdownTestObjects: shutdownTestObjects{
				allEphemeral:     true,
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1"}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
				jobs:             []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1"}},
				configmaps:       []shutdownTestObject{{name: "configmap-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"}},
				secrets:          []shutdownTestObject{{name: "secret-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"}},
			},
		},
		{
			name: "Multiple MachineOSConfigs and MachineOSBuilds with nothing pending",
			shutdownTestObjects: shutdownTestObjects{
				allEphemeral: true,
				machineOSConfigs: []shutdownTestObject{
					{name: "mosc-1"},
					{name: "mosc-2"},
				},
				machineOSBuilds: []shutdownTestObject{
					{name: "mosb-1", moscName: "mosc-1"},
					{name: "mosb-2", moscName: "mosc-2"},
				},
				jobs: []shutdownTestObject{
					{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1"},
					{name: "job-2", moscName: "mosc-2", mosbName: "mosb-2"},
				},
				configmaps: []shutdownTestObject{
					{name: "configmap-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"},
					{name: "configmap-2", moscName: "mosc-2", mosbName: "mosb-2", jobName: "job-2"},
				},
				secrets: []shutdownTestObject{
					{name: "secret-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"},
					{name: "secret-2", moscName: "mosc-2", mosbName: "mosb-2", jobName: "job-2"},
				},
			},
		},
		{
			name: "Multiple MachineOSConfigs and MachineOSBuilds all pending",
			shutdownTestObjects: shutdownTestObjects{
				allEphemeral:       true,
				allPendingDeletion: true,
				machineOSConfigs: []shutdownTestObject{
					{name: "mosc-1"},
					{name: "mosc-2"},
				},
				machineOSBuilds: []shutdownTestObject{
					{name: "mosb-1", moscName: "mosc-1"},
					{name: "mosb-2", moscName: "mosb-2"},
				},
				jobs: []shutdownTestObject{
					{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1"},
					{name: "job-2", moscName: "mosc-2", mosbName: "mosb-2"},
				},
				configmaps: []shutdownTestObject{
					{name: "configmap-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"},
					{name: "configmap-2", moscName: "mosc-2", mosbName: "mosb-2", jobName: "job-2"},
				},
				secrets: []shutdownTestObject{
					{name: "secret-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"},
					{name: "secret-2", moscName: "mosc-2", mosbName: "mosb-2", jobName: "job-2"},
				},
			},
		},
		{
			name: "Orphaned MachineOSBuild with child objects not pending",
			shutdownTestObjects: shutdownTestObjects{
				allEphemeral:    true,
				machineOSBuilds: []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
				jobs:            []shutdownTestObject{{name: "job-1", moscName: "mosc-1", mosbName: "mosb-1"}},
				configmaps:      []shutdownTestObject{{name: "configmap-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"}},
				secrets:         []shutdownTestObject{{name: "secret-1", moscName: "mosc-1", mosbName: "mosb-1", jobName: "job-1"}},
			},
			expected: &expectedObjects{
				machineOSBuilds: []string{"mosb-1"},
				jobs:            []string{"job-1"},
				configmaps:      []string{"configmap-1"},
				secrets:         []string{"secret-1"},
			},
		},
		{
			name: "Pending MachineOSConfig has non pending MachineOSBuild",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
			},
			expected: &expectedObjects{
				machineOSBuilds: []string{"mosb-1"},
			},
		},
		{
			name: "Orphaned non-pending digest ConfigMap",

			shutdownTestObjects: shutdownTestObjects{
				configmaps: []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
			expected: &expectedObjects{
				configmaps: []string{"digest-configmap-1"},
			},
		},
		{
			name: "Orphaned pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				configmaps: []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true, pendingDeletion: true}},
			},
		},
		{
			name: "Pending MachineOSBuild with non-pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSBuilds: []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
				configmaps:      []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
			expected: &expectedObjects{
				configmaps: []string{"digest-configmap-1"},
			},
		},
		{
			name: "Pending MachineOSBuild with pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSBuilds: []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
				configmaps:      []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true, pendingDeletion: true}},
			},
		},
		{
			name: "Pending MachineOSConfig with non-pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				configmaps:       []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
			expected: &expectedObjects{
				configmaps: []string{"digest-configmap-1"},
			},
		},
		{
			name: "Pending MachineOSConfig with pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				configmaps:       []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true, pendingDeletion: true}},
			},
		},
		{
			name: "Pending MachineOSConfig and MachineOSBuild with non-pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
				configmaps:       []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
			expected: &expectedObjects{
				configmaps: []string{"digest-configmap-1"},
			},
		},
		{
			name: "Pending MachineOSConfig and MachineOSBuild with pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1", pendingDeletion: true}},
				configmaps:       []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true, pendingDeletion: true}},
			},
		},
		{
			name: "Pending MachineOSConfig and non-pending MachineOSBuild with non-pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
				configmaps:       []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
			expected: &expectedObjects{
				machineOSBuilds: []string{"mosb-1"},
				configmaps:      []string{"digest-configmap-1"},
			},
		},
		{
			name: "Pending MachineOSConfig and non-pending MachineOSBuild with pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1", pendingDeletion: true}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
				configmaps:       []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true, pendingDeletion: true}},
			},
			expected: &expectedObjects{
				machineOSBuilds: []string{"mosb-1"},
			},
		},
		{
			name: "Non-pending MachineOSConfig and non-pending MachineOSBuild with non-pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1"}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
				configmaps:       []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true}},
			},
		},
		{
			name: "Non-pending MachineOSConfig and non-pending MachineOSBuild with pending digest ConfigMap",
			shutdownTestObjects: shutdownTestObjects{
				machineOSConfigs: []shutdownTestObject{{name: "mosc-1"}},
				machineOSBuilds:  []shutdownTestObject{{name: "mosb-1", moscName: "mosc-1"}},
				configmaps:       []shutdownTestObject{{name: "digest-configmap-1", moscName: "mosc-1", mosbName: "mosb-1", ephemeral: true, pendingDeletion: true}},
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase

		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			t.Run("Objects", func(t *testing.T) {
				t.Parallel()

				ofs := newObjectsForShutdownFromRuntimeObjects(testCase.shutdownTestObjects.toRuntimeObjects())

				nonPending := ofs.findAllNonPendingObjects()

				if testCase.expected != nil {
					for _, mosc := range testCase.expected.machineOSConfigs {
						assert.True(t, nonPending.HasKey(utils.UniqueObjectKey{Name: mosc, UID: mosc + "-uid"}))
					}

					for _, mosb := range testCase.expected.machineOSBuilds {
						assert.True(t, nonPending.HasKey(utils.UniqueObjectKey{Name: mosb, UID: mosb + "-uid"}))
					}

					for _, job := range testCase.expected.jobs {
						assert.True(t, nonPending.HasKey(utils.UniqueObjectKey{Name: job, UID: job + "-uid"}))
					}

					for _, configmap := range testCase.expected.configmaps {
						assert.True(t, nonPending.HasKey(utils.UniqueObjectKey{Name: configmap, UID: configmap + "-uid"}))
					}

					for _, secret := range testCase.expected.secrets {
						assert.True(t, nonPending.HasKey(utils.UniqueObjectKey{Name: secret, UID: secret + "-uid"}))
					}
				} else {
					assert.Equal(t, 0, nonPending.Len())
				}
			})

			t.Run("Handler", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)

				sh := setupShutdownDelayHandlerForTest(ctx, cancel, t, testCase.shutdownTestObjects)

				err := sh.handleShutdown(ctx, time.Nanosecond)
				if testCase.expected != nil {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})
		})
	}
}

func newObjectsForShutdownFromRuntimeObjects(objs []runtime.Object) *objectsForShutdown {
	all := []metav1.Object{}

	moscMap := map[string]*mcfgv1.MachineOSConfig{}
	mosbMap := map[string]*mcfgv1.MachineOSBuild{}
	jobMap := map[string]*batchv1.Job{}

	for _, obj := range objs {
		switch typed := obj.(type) {
		case *mcfgv1.MachineOSConfig:
			moscMap[typed.GetName()] = typed
		case *mcfgv1.MachineOSBuild:
			mosbMap[typed.GetName()] = typed
		case *batchv1.Job:
			jobMap[typed.GetName()] = typed
		}

		objM := obj.(metav1.Object)

		all = append(all, objM)
	}

	return &objectsForShutdown{moscMap: moscMap, mosbMap: mosbMap, jobMap: jobMap, all: all}
}

func setupShutdownDelayHandlerForTest(ctx context.Context, cancelFunc context.CancelFunc, t *testing.T, shutdownTestObjects shutdownTestObjects) *shutdownDelayHandler {
	mcfgObjects := []runtime.Object{}
	other := []runtime.Object{}

	// Filter mcfgv1 objects out of the runtime.Object params.
	for _, obj := range shutdownTestObjects.toRuntimeObjects() {
		switch o := obj.(type) {
		case *mcfgv1.MachineOSConfig:
			mcfgObjects = append(mcfgObjects, o)
		case *mcfgv1.MachineOSBuild:
			mcfgObjects = append(mcfgObjects, o)
		default:
			other = append(other, o)
		}
	}

	mcfgclient := fakeclientmachineconfigv1.NewSimpleClientset(mcfgObjects...)
	kubeclient := fakecorev1client.NewSimpleClientset(other...)

	informers := newInformers(mcfgclient, kubeclient)
	listers := informers.listers()

	// Start the informers.
	informers.start(ctx)
	if !cache.WaitForCacheSync(ctx.Done(), informers.hasSynced...) {
		t.FailNow()
	}

	return newTestShutdownDelayHandler(ctx, cancelFunc, t, listers)
}

// Instantiates a shutdown handler with a fake clock for testing purposes as
// well as a goroutine that is used to advance the fake clock ahead in time.
//
// By doing this, we ensure that both the delay computation and the context
// cancellation paths of the shutdownDelayHandler event loop are targeted.
// Whether the test had the desired outcome or not is dependent upon the caller
// to verify.
//
// Note: This function accepts an unused *testing.T parameter to ensure that it
// is only used in the testing path.
func newTestShutdownDelayHandler(ctx context.Context, cancel context.CancelFunc, _ *testing.T, l *listers) *shutdownDelayHandler {
	// Instantiate the FakeClock implementation. This allows us to implement the
	// test suite without any complex sleep conditions which could cause a race
	// and/or flaky tests.
	fc := fakeclock.NewFakeClock(time.Now())

	// Instantiate the shutdownDelayHandler and embed the fake clock implementation.
	sh := newShutdownDelayHandler(l)
	sh.clock = fc

	// Spawn a goroutine that drives the fake clock.
	go func() {
		for {
			select {
			// Ensure that this goroutine shuts down cleanly if the context is
			// canceled.
			case <-ctx.Done():
				return
			default:
				// Whenever the fake clock reaches the point where it has something
				// waiting on it (i.e., whenever its After() method is called), we step
				// the clock ahead, which causes the channel to receive the event and
				// execute any code there. Since our code is in that particular path,
				// we can cancel the context afterward and exit.
				if fc.HasWaiters() {
					fc.Step(time.Second)
					cancel()
					return
				}
			}
		}
	}()

	return sh
}

// Holds shutdownTestObject instances that are used to construct kube objects of the
// named types.
type shutdownTestObjects struct {
	machineOSConfigs []shutdownTestObject
	machineOSBuilds  []shutdownTestObject
	jobs             []shutdownTestObject
	configmaps       []shutdownTestObject
	secrets          []shutdownTestObject
	// Sets ephemeral to true on all jobs, configmaps, and secrets.
	allEphemeral bool
	// Sets the deletion timestamp on all objects.
	allPendingDeletion bool
	// Additional objects to construct that may be too difficult to fit into our
	// constraints.
	objs []runtime.Object
}

// Constructs kube objects from all of the associated shutdownTestObjects.
func (s *shutdownTestObjects) toRuntimeObjects() []runtime.Object {
	objs := []runtime.Object{}

	for _, obj := range s.machineOSConfigs {
		objs = append(objs, s.instantiateTestObject(obj, &mcfgv1.MachineOSConfig{}))
	}

	for _, obj := range s.machineOSBuilds {
		objs = append(objs, s.instantiateTestObject(obj, &mcfgv1.MachineOSBuild{}))
	}

	for _, obj := range s.jobs {
		objs = append(objs, s.instantiateNamespacedTestObject(obj, &batchv1.Job{}))
	}

	for _, obj := range s.configmaps {
		objs = append(objs, s.instantiateNamespacedTestObject(obj, &corev1.ConfigMap{}))
	}

	for _, obj := range s.secrets {
		objs = append(objs, s.instantiateNamespacedTestObject(obj, &corev1.Secret{}))
	}

	return append(objs, s.objs...)
}

// Instantiates a namespaced test object
func (s *shutdownTestObjects) instantiateNamespacedTestObject(sObj shutdownTestObject, obj runtime.Object) runtime.Object {
	sObj.namespaced = true
	return s.instantiateTestObject(sObj, obj)
}

// Instantiates a test object, applying global values to each one, if needed.
func (s *shutdownTestObjects) instantiateTestObject(sObj shutdownTestObject, obj runtime.Object) runtime.Object {
	if s.allEphemeral {
		sObj.ephemeral = true
	}

	if s.allPendingDeletion {
		sObj.pendingDeletion = true
	}

	return sObj.apply(obj)
}

// Holds the various options needed to construct the metav1.ObjectMeta portion
// of a kube object and apply it to a given object that matches the
// runtime.Object interface.
type shutdownTestObject struct {
	// The name of the object.
	name string
	// The namespace of the object.
	namespace string
	// Whether the object is namespaced (see getNamespace()) method.
	namespaced bool
	// Whether to add the ephemeral object labels. When used with apply(), will
	// only apply to objects that are not MachineOSConfigs or MachineOSBuilds.
	ephemeral bool
	// Whether to mark the object as pending deletion.
	pendingDeletion bool
	// The MachineOSConfig name to associate the object with.
	moscName string
	// The MachineOSBuild name to associate the object with.
	mosbName string
	// The job name to associate the object with.
	jobName string
	// The object instance to apply the shutdownTestObject config to. May also be passed
	// into the apply() method.
	obj runtime.Object
}

// Applies the config values to either the passed object or to the object on
// the struct. Performs some checks to determine which object should receive
// the config values.
func (s *shutdownTestObject) apply(obj runtime.Object) runtime.Object {
	if s.obj == nil && obj == nil {
		panic("both objects are nil")
	}

	if s.obj != nil && obj == nil {
		return s.applyToObject(s.obj)
	}

	if s.obj == nil && obj != nil {
		return s.applyToObject(obj)
	}

	if s.obj != nil && obj != nil && reflect.TypeOf(s.obj) != reflect.TypeOf(obj) {
		panic(fmt.Sprintf("t.obj has type %T and obj has type %T", s.obj, obj))
	}

	// The passed in object wins.
	return s.applyToObject(obj)
}

// Converts the shutdownTestObject into a metav1.ObjectMeta instance and applies the
// relevant properties to the given object.
func (s *shutdownTestObject) applyToObject(obj runtime.Object) runtime.Object {
	accessor := meta.NewAccessor()

	accessor.SetNamespace(obj, s.getNamespace())
	accessor.SetLabels(obj, s.getLabelsForObject(obj))
	accessor.SetName(obj, s.name)
	accessor.SetUID(obj, apitypes.UID(s.name)+"-uid")

	// Need to convert this to metav1.Object since the accessor does not include
	// a method for setting the deletion timestamp.
	mobj := obj.(metav1.Object)
	mobj.SetDeletionTimestamp(s.getDeletionTimestamp())
	mobj.SetOwnerReferences(s.getOwnerReferences(obj))

	return obj
}

// Sets owner references for the given object based upon what type of object it is.
func (s *shutdownTestObject) getOwnerReferences(obj runtime.Object) []metav1.OwnerReference {
	if _, ok := obj.(*mcfgv1.MachineOSBuild); ok && s.moscName != "" {
		return []metav1.OwnerReference{
			{
				Name: s.moscName,
				UID:  apitypes.UID(s.moscName) + "-uid",
				Kind: "MachineOSConfig",
			},
		}
	}

	if _, ok := obj.(*batchv1.Job); ok && s.mosbName != "" && s.ephemeral {
		return []metav1.OwnerReference{
			{
				Name: s.mosbName,
				UID:  apitypes.UID(s.mosbName) + "-uid",
				Kind: "MachineOSBuild",
			},
		}
	}

	_, isConfigMap := obj.(*corev1.ConfigMap)
	_, isSecret := obj.(*corev1.Secret)

	if (isConfigMap || isSecret) && s.ephemeral && s.jobName != "" {
		return []metav1.OwnerReference{
			{
				Name: s.jobName,
				UID:  apitypes.UID(s.jobName) + "-uid",
				Kind: "Job",
			},
		}
	}

	return nil
}

// Gets the namespace for the object. If the namespace is not provided but a
// namespaced object is desired, will default to MCO namespace.
func (s *shutdownTestObject) getNamespace() string {
	if s.namespace != "" {
		s.namespaced = true
		return s.namespace
	}

	if s.namespaced {
		return ctrlcommon.MCONamespace
	}

	return ""
}

// Gets the labels for the specific object type we're constructing.
func (s *shutdownTestObject) getLabelsForObject(obj runtime.Object) map[string]string {
	empty := map[string]string{}

	// MachineOSConfigs do not get any labels.
	_, ok := obj.(*mcfgv1.MachineOSConfig)
	if ok {
		return empty
	}

	// MachineOSBuilds need to have a label referring to the MachineOSConfig.
	_, ok = obj.(*mcfgv1.MachineOSBuild)
	if ok {
		if s.moscName != "" {
			return map[string]string{
				constants.MachineOSConfigNameLabelKey: s.moscName,
			}
		}

		return empty
	}

	// For all other object types, return the labels according to our
	return s.getLabels()
}

// Gets the labels for the object we're constructing.
func (s *shutdownTestObject) getLabels() map[string]string {
	labels := map[string]string{}

	if s.ephemeral {
		// If this is an ephemeral object type, set the following labels.
		labels = map[string]string{
			constants.EphemeralBuildObjectLabelKey:    "",
			constants.OnClusterLayeringLabelKey:       "",
			constants.RenderedMachineConfigLabelKey:   "",
			constants.TargetMachineConfigPoolLabelKey: "",
		}
	}

	if s.moscName != "" {
		// Set the MachineOSConfig name label when a name is provided.
		labels[constants.MachineOSConfigNameLabelKey] = s.moscName
	}

	if s.mosbName != "" {
		// Set the MachineOSBuild name label when a name is provided.
		labels[constants.MachineOSBuildNameLabelKey] = s.mosbName
	}

	return labels
}

// Gets the deletion timestamp for the object; if desired.
func (s *shutdownTestObject) getDeletionTimestamp() *metav1.Time {
	if s.pendingDeletion {
		now := metav1.Now()
		return &now
	}

	return nil
}
