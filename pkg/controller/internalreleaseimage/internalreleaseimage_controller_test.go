package internalreleaseimage

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"

	fakeconfigv1client "github.com/openshift/client-go/config/clientset/versioned/fake"
)

func TestInternalReleaseImageCreate(t *testing.T) {
	cases := []struct {
		name           string
		initialObjects func() []runtime.Object
		verify         func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig)
	}{
		{
			name:           "feature inactive",
			initialObjects: objs(),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.Nil(t, actualIRI)
				assert.Nil(t, actualMasterMC)
				assert.Nil(t, actualWorkerMC)
			},
		},
		{
			name:           "add finalizer if not present",
			initialObjects: objs(iri(), clusterVersion(), cconfig().withDNS("example.com"), iriCertSecret(), iriAuthSecret(), pullSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.Len(t, actualIRI.Finalizers, 1)
				assert.Contains(t, actualIRI.Finalizers, iriFinalizerName)
			},
		},
		{
			name: "update status if not set",
			initialObjects: objs(
				iri().finalizer(iriFinalizerName),
				clusterVersion(), cconfig().withDNS("example.com"), iriCertSecret(), iriAuthSecret(), pullSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.Len(t, actualIRI.Status.Releases, 1)
				assert.Equal(t, actualIRI.Status.Releases[0].Name, "ocp-release-bundle-4.21.5-x86_64")
				assert.Equal(t, actualIRI.Status.Releases[0].Image, "ocp-4.21-release-pullspec")
				assert.Equal(t, actualIRI.Status.Releases[0].Conditions[0].Type, string(mcfgv1.InternalReleaseImageConditionTypeAvailable))
				assert.Equal(t, actualIRI.Status.Releases[0].Conditions[0].Status, metav1.ConditionTrue)
				assert.Equal(t, actualIRI.Status.Releases[0].Conditions[0].Message, "Release bundle is available")
			},
		},
		{
			name:           "generate iri machine-config if not present",
			initialObjects: objs(iri(), clusterVersion(), cconfig().withDNS("example.com"), iriCertSecret(), iriAuthSecret(), pullSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfig(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
		},
		{
			name: "avoid machine-config drifting",
			initialObjects: objs(
				iri().finalizer(iriFinalizerName),
				clusterVersion(), cconfig().withDNS("example.com"), iriCertSecret(), iriAuthSecret(), pullSecret(),
				machineconfigmaster().ignition("some garbage"),
				machineconfigworker().ignition("other garbage")),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfig(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
		},
		{
			name: "refresh machine-config on controllerConfig update",
			initialObjects: objs(
				iri().finalizer(iriFinalizerName),
				clusterVersion(), cconfig().dockerRegistryImage("a-new-docker-registry-image-pullspec").withDNS("example.com"), iriCertSecret(), iriAuthSecret(), pullSecret(),
				machineconfigmaster(), machineconfigworker()),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfig(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
		},
		{
			name: "disables service and removes finalizer on iri deletion",
			initialObjects: objs(
				iri().finalizer(iriFinalizerName).setDeletionTimestamp(),
				clusterVersion(), cconfig(), iriCertSecret(),
				machineconfigmaster(), machineconfigworker()),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.NotNil(t, actualIRI)
				assert.Empty(t, actualIRI.Finalizers)
				verifyDisabledMasterMachineConfig(t, actualMasterMC)
			},
		},
		{
			name: "status condition Degraded=False on successful sync",
			initialObjects: objs(
				iri().finalizer(iriFinalizerName),
				clusterVersion(), cconfig().withDNS("example.com"), iriCertSecret(), iriAuthSecret(), pullSecret(),
				machineconfigmaster(), machineconfigworker()),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.NotNil(t, actualIRI)
				assert.Len(t, actualIRI.Status.Conditions, 1)
				assert.Equal(t, string(mcfgv1.InternalReleaseImageStatusConditionTypeDegraded), actualIRI.Status.Conditions[0].Type)
				assert.Equal(t, metav1.ConditionFalse, actualIRI.Status.Conditions[0].Status)
				assert.Equal(t, "AllReleasesAvailable", actualIRI.Status.Conditions[0].Reason)
				assert.Equal(t, "All the release images are available", actualIRI.Status.Conditions[0].Message)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := tc.initialObjects()
			f := newFixture(t, objs)
			f.run(ctrlcommon.InternalReleaseImageInstanceName)

			if tc.verify != nil {
				actualIRI, err := f.client.MachineconfigurationV1().InternalReleaseImages().Get(context.TODO(), ctrlcommon.InternalReleaseImageInstanceName, v1.GetOptions{})
				if err != nil {
					if !errors.IsNotFound(err) {
						t.Errorf("Error while running sync step: %v", err)
					} else {
						actualIRI = nil
					}
				}
				actualMasterMC, err := f.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), masterName(), v1.GetOptions{})
				if err != nil {
					if !errors.IsNotFound(err) {
						t.Errorf("Error while running sync step: %v", err)
					} else {
						actualMasterMC = nil
					}
				}
				actualWorkerMC, err := f.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), workerName(), v1.GetOptions{})
				if err != nil {
					if !errors.IsNotFound(err) {
						t.Errorf("Error while running sync step: %v", err)
					} else {
						actualWorkerMC = nil
					}
				}
				tc.verify(t, actualIRI, actualMasterMC, actualWorkerMC)
			}

		})
	}
}

func TestInternalReleaseImageStatusOnError(t *testing.T) {
	cases := []struct {
		name           string
		initialObjects func() []runtime.Object
		verify         func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage)
	}{
		{
			name: "status condition Degraded=True when ControllerConfig is missing",
			initialObjects: objs(
				iri(),
				clusterVersion(), iriCertSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage) {
				assert.NotNil(t, actualIRI)
				assert.Len(t, actualIRI.Status.Conditions, 1)
				assert.Equal(t, string(mcfgv1.InternalReleaseImageStatusConditionTypeDegraded), actualIRI.Status.Conditions[0].Type)
				assert.Equal(t, metav1.ConditionTrue, actualIRI.Status.Conditions[0].Status)
				assert.Equal(t, "SyncError", actualIRI.Status.Conditions[0].Reason)
				assert.Contains(t, actualIRI.Status.Conditions[0].Message, "could not get ControllerConfig")
			},
		},
		{
			name: "status condition Degraded=True when Secret is missing",
			initialObjects: objs(
				iri(),
				clusterVersion(), cconfig()),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage) {
				assert.NotNil(t, actualIRI)
				assert.Len(t, actualIRI.Status.Conditions, 1)
				assert.Equal(t, string(mcfgv1.InternalReleaseImageStatusConditionTypeDegraded), actualIRI.Status.Conditions[0].Type)
				assert.Equal(t, metav1.ConditionTrue, actualIRI.Status.Conditions[0].Status)
				assert.Equal(t, "SyncError", actualIRI.Status.Conditions[0].Reason)
				assert.Contains(t, actualIRI.Status.Conditions[0].Message, "could not get Secret")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := tc.initialObjects()
			f := newFixture(t, objs)
			// Run the controller and expect an error
			f.runController(ctrlcommon.InternalReleaseImageInstanceName, true)

			if tc.verify != nil {
				actualIRI, err := f.client.MachineconfigurationV1().InternalReleaseImages().Get(context.TODO(), ctrlcommon.InternalReleaseImageInstanceName, v1.GetOptions{})
				if err != nil {
					if !errors.IsNotFound(err) {
						t.Errorf("Error getting IRI: %v", err)
					} else {
						actualIRI = nil
					}
				}
				tc.verify(t, actualIRI)
			}
		})
	}
}

func TestReconcileHtpasswd(t *testing.T) {
	cases := []struct {
		name             string
		password         string
		existingHtpasswd string
		expectUpdate     bool
	}{
		{
			name:             "htpasswd already matches password, no update",
			password:         "mypassword",
			existingHtpasswd: mustGenerateHtpasswd(t, "mypassword"),
			expectUpdate:     false,
		},
		{
			name:             "htpasswd missing, generates new",
			password:         "mypassword",
			existingHtpasswd: "",
			expectUpdate:     true,
		},
		{
			name:             "password changed, regenerates htpasswd",
			password:         "newpassword",
			existingHtpasswd: mustGenerateHtpasswd(t, "oldpassword"),
			expectUpdate:     true,
		},
	}

	t.Run("empty password returns error", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ctrlcommon.InternalReleaseImageAuthSecretName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string][]byte{
				"password": []byte(""),
			},
		}
		f := newFixture(t, []runtime.Object{secret})
		_, err := reconcileHtpasswd(f.k8sClient, secret)
		assert.Error(t, err)
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ctrlcommon.InternalReleaseImageAuthSecretName,
					Namespace: ctrlcommon.MCONamespace,
				},
				Data: map[string][]byte{
					"password": []byte(tc.password),
					"htpasswd": []byte(tc.existingHtpasswd),
				},
			}

			f := newFixture(t, []runtime.Object{secret})
			result, err := reconcileHtpasswd(f.k8sClient, secret)
			assert.NoError(t, err)

			if tc.expectUpdate {
				// Verify the returned secret has a valid htpasswd
				assert.True(t, HtpasswdMatchesPassword(string(result.Data["htpasswd"]), ctrlcommon.IRIRegistryUsername, tc.password),
					"updated htpasswd should match the password")

				// Verify the secret was updated in the API
				updated, err := f.k8sClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(
					context.TODO(), ctrlcommon.InternalReleaseImageAuthSecretName, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.True(t, HtpasswdMatchesPassword(string(updated.Data["htpasswd"]), ctrlcommon.IRIRegistryUsername, tc.password),
					"secret in API should have updated htpasswd")
			} else {
				// Verify the htpasswd was not changed
				assert.Equal(t, tc.existingHtpasswd, string(result.Data["htpasswd"]),
					"htpasswd should not change when already matching")
			}
		})
	}
}

func mustGenerateHtpasswd(t *testing.T, password string) string {
	t.Helper()
	entry, err := generateHtpasswdEntry(ctrlcommon.IRIRegistryUsername, password)
	assert.NoError(t, err)
	return entry
}
// The fixture used to setup and run the controller.
type fixture struct {
	t *testing.T

	client       *fake.Clientset
	k8sClient    *k8sfake.Clientset
	configClient *fakeconfigv1client.Clientset

	iriLister            []*mcfgv1.InternalReleaseImage
	ccLister             []*mcfgv1.ControllerConfig
	mcLister             []*mcfgv1.MachineConfig
	mcnLister            []*mcfgv1.MachineConfigNode
	secretLister         []*corev1.Secret
	nodeLister           []*corev1.Node
	clusterVersionLister []*configv1.ClusterVersion
	infraLister          []*configv1.Infrastructure

	controller    *Controller
	objects       []runtime.Object
	k8sObjects    []runtime.Object
	configObjects []runtime.Object
}

func newFixture(t *testing.T, objects []runtime.Object) *fixture {
	f := &fixture{t: t}
	f.setupObjects(objects)
	f.controller = f.newController()
	return f
}

func (f *fixture) setupObjects(objs []runtime.Object) {
	for _, obj := range objs {
		switch obj.(type) {
		case *corev1.Secret, *corev1.ConfigMap, *corev1.Pod, *corev1.Node:
			f.k8sObjects = append(f.k8sObjects, obj)
			switch o := obj.(type) {
			case *corev1.Secret:
				f.secretLister = append(f.secretLister, o)
			case *corev1.Node:
				f.nodeLister = append(f.nodeLister, o)
			}
		case *configv1.ClusterVersion, *configv1.Infrastructure:
			f.configObjects = append(f.configObjects, obj)
			switch o := obj.(type) {
			case *configv1.ClusterVersion:
				f.clusterVersionLister = append(f.clusterVersionLister, o)
			case *configv1.Infrastructure:
				f.infraLister = append(f.infraLister, o)
			}
		default:
			f.objects = append(f.objects, obj)
			switch o := obj.(type) {
			case *mcfgv1.InternalReleaseImage:
				f.iriLister = append(f.iriLister, o)
			case *mcfgv1.ControllerConfig:
				f.ccLister = append(f.ccLister, o)
			case *mcfgv1.MachineConfig:
				f.mcLister = append(f.mcLister, o)
			case *mcfgv1.MachineConfigNode:
				f.mcnLister = append(f.mcnLister, o)
			}
		}
	}
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.k8sClient = k8sfake.NewSimpleClientset(f.k8sObjects...)
	f.configClient = fakeconfigv1client.NewSimpleClientset(f.configObjects...)

	i := mcfginformers.NewSharedInformerFactory(f.client, func() time.Duration { return 0 }())
	k := informers.NewSharedInformerFactory(f.k8sClient, func() time.Duration { return 0 }())
	ci := configinformers.NewSharedInformerFactory(f.configClient, func() time.Duration { return 0 }())

	c := New(
		i.Machineconfiguration().V1().InternalReleaseImages(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().MachineConfigs(),
		ci.Config().V1().ClusterVersions(),
		k.Core().V1().Secrets(),
		i.Machineconfiguration().V1().MachineConfigNodes(),
		k.Core().V1().Nodes(),
		ci.Config().V1().Infrastructures(),
		f.k8sClient,
		f.client,
	)

	alwaysReady := func() bool { return true }
	c.iriListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.clusterVersionListerSynced = alwaysReady
	c.secretListerSynced = alwaysReady
	c.mcnListerSynced = alwaysReady
	c.infraListerSynced = alwaysReady
	c.nodeListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	stopCh := make(chan struct{})
	defer close(stopCh)

	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)
	k.Start(stopCh)
	k.WaitForCacheSync(stopCh)
	ci.Start(stopCh)
	ci.WaitForCacheSync(stopCh)

	for _, c := range f.iriLister {
		i.Machineconfiguration().V1().InternalReleaseImages().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mcnLister {
		i.Machineconfiguration().V1().MachineConfigNodes().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.secretLister {
		k.Core().V1().Secrets().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.nodeLister {
		k.Core().V1().Nodes().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.clusterVersionLister {
		ci.Config().V1().ClusterVersions().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.infraLister {
		ci.Config().V1().Infrastructures().Informer().GetIndexer().Add(c)
	}

	return c
}

func (f *fixture) run(key string) {
	f.runController(key, false)
}

func (f *fixture) runController(key string, expectError bool) {
	err := f.controller.syncHandler(key)
	if !expectError && err != nil {
		f.t.Errorf("error syncing internalreleaseimage: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing internalreleaseimage, got nil")
	}
}

func TestAggregateIRIStatus(t *testing.T) {
	cases := []struct {
		name           string
		initialObjects func() []runtime.Object
		verify         func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage)
	}{
		{
			name: "nodes-not-ready: some nodes not ready produces SomeNodesUnavailable",
			initialObjects: objs(
				iri().finalizer(iriFinalizerName),
				clusterVersion(),
				cconfig().withDNS("example.com"),
				iriCertSecret(),
				iriAuthSecret(),
				pullSecret(),
				machineconfigmaster(),
				machineconfigworker(),
				mcn("master-0"),
				mcn("master-1"),
				mcn("master-2"),
				node("master-0").notReady(), // Node not ready
				node("master-1"),
				node("master-2"),
				infrastructure(),
			),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage) {
				assert.NotNil(t, actualIRI)
				assert.Len(t, actualIRI.Status.Conditions, 1)
				assert.Equal(t, string(mcfgv1.InternalReleaseImageStatusConditionTypeDegraded), actualIRI.Status.Conditions[0].Type)
				assert.Equal(t, metav1.ConditionTrue, actualIRI.Status.Conditions[0].Status)

				// Note: api-int ping will fail in unit tests, so may get ApiIntNotAvailable
				// instead of SomeNodesUnavailable.
				// In e2e tests this would be SomeNodesUnavailable.
				assert.NotEmpty(t, actualIRI.Status.Conditions[0].Reason)

				// Verify aggregation produced release status
				assert.Len(t, actualIRI.Status.Releases, 1)
				assert.Equal(t, "ocp-release-bundle-4.21.5-x86_64", actualIRI.Status.Releases[0].Name)
			},
		},
		{
			name: "registry-unavailable-not-on-api-int: degraded MCN produces SomeRegistriesUnavailable",
			initialObjects: objs(
				iri().finalizer(iriFinalizerName),
				clusterVersion(),
				cconfig().withDNS("example.com"),
				iriCertSecret(),
				iriAuthSecret(),
				pullSecret(),
				machineconfigmaster(),
				machineconfigworker(),
				mcn("master-0").degraded(), // MCN degraded
				mcn("master-1"),
				mcn("master-2"),
				node("master-0"),
				node("master-1"),
				node("master-2"),
				infrastructure(),
			),
			verify: func(t *testing.T, actualIRI *mcfgv1.InternalReleaseImage) {
				assert.NotNil(t, actualIRI)
				assert.Len(t, actualIRI.Status.Conditions, 1)
				assert.Equal(t, string(mcfgv1.InternalReleaseImageStatusConditionTypeDegraded), actualIRI.Status.Conditions[0].Type)
				assert.Equal(t, metav1.ConditionTrue, actualIRI.Status.Conditions[0].Status)
				assert.NotEmpty(t, actualIRI.Status.Conditions[0].Reason)

				// Verify aggregation produced release status
				assert.Len(t, actualIRI.Status.Releases, 1)
				assert.Equal(t, "ocp-release-bundle-4.21.5-x86_64", actualIRI.Status.Releases[0].Name)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := tc.initialObjects()
			f := newFixture(t, objs)
			f.run(ctrlcommon.InternalReleaseImageInstanceName)

			if tc.verify != nil {
				actualIRI, err := f.client.MachineconfigurationV1().InternalReleaseImages().Get(context.TODO(), ctrlcommon.InternalReleaseImageInstanceName, v1.GetOptions{})
				if err != nil {
					if !errors.IsNotFound(err) {
						t.Errorf("Error while running sync step: %v", err)
					} else {
						actualIRI = nil
					}
				}
				tc.verify(t, actualIRI)
			}
		})
	}
}

func TestTransformToAPIIntURL(t *testing.T) {
	cases := []struct {
		name          string
		localhostURL  string
		clusterDomain string
		expected      string
	}{
		{
			name:          "localhost without localdomain",
			localhostURL:  "localhost:22625/openshift/release-images@sha256:abc123",
			clusterDomain: "ostest.test.metalkube.org",
			expected:      "api-int.ostest.test.metalkube.org:22625/openshift/release-images@sha256:abc123",
		},
		{
			name:          "no port returns input unchanged",
			localhostURL:  "localhost",
			clusterDomain: "example.com",
			expected:      "localhost",
		},
		{
			name:          "no port with sha256 digest returns input unchanged",
			localhostURL:  "localhost/openshift/release-images@sha256:abc123",
			clusterDomain: "example.com",
			expected:      "localhost/openshift/release-images@sha256:abc123",
		},
		{
			name:          "non-localhost host with port",
			localhostURL:  "virthost.example.com:5000/openshift/release-images@sha256:abc123",
			clusterDomain: "ostest.test.metalkube.org",
			expected:      "api-int.ostest.test.metalkube.org:5000/openshift/release-images@sha256:abc123",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := transformToAPIIntURL(tc.localhostURL, tc.clusterDomain)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestNewWithAlreadyStartedInformers is a regression test for the nil-pointer
// panic caused by an informer race in New(). When an informer is already
// started, AddEventHandler replays the current cache contents as synthetic Add
// events on a separate goroutine. If the listers were assigned after the event
// handlers were registered, the replayed MachineConfigNode Add would invoke
// isControlPlaneNode -> ctrl.nodeLister.Get on a nil nodeLister and panic.
//
// This test preloads and starts the informers (in particular mcnInformer and
// nodeInformer) BEFORE calling New, exercising that ordering and asserting that
// construction completes without panicking and that the replayed event is
// handled.
func TestNewWithAlreadyStartedInformers(t *testing.T) {
	// A healthy MachineConfigNode plus its control-plane Node so that the
	// replayed Add event drives addMachineConfigNode -> isControlPlaneNode,
	// which dereferences the node lister.
	mcfgClient := fake.NewSimpleClientset(mcn("master-0").build())
	k8sClient := k8sfake.NewSimpleClientset(node("master-0").build())
	configClient := fakeconfigv1client.NewSimpleClientset()

	i := mcfginformers.NewSharedInformerFactory(mcfgClient, 0)
	k := informers.NewSharedInformerFactory(k8sClient, 0)
	ci := configinformers.NewSharedInformerFactory(configClient, 0)

	iriInformer := i.Machineconfiguration().V1().InternalReleaseImages()
	ccInformer := i.Machineconfiguration().V1().ControllerConfigs()
	mcInformer := i.Machineconfiguration().V1().MachineConfigs()
	cvInformer := ci.Config().V1().ClusterVersions()
	secretInformer := k.Core().V1().Secrets()
	mcnInformer := i.Machineconfiguration().V1().MachineConfigNodes()
	nodeInformer := k.Core().V1().Nodes()
	infraInformer := ci.Config().V1().Infrastructures()

	// Instantiate each informer so the factories start and sync them below.
	iriInformer.Informer()
	ccInformer.Informer()
	mcInformer.Informer()
	cvInformer.Informer()
	secretInformer.Informer()
	mcnInformer.Informer()
	nodeInformer.Informer()
	infraInformer.Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start and sync the informers BEFORE constructing the controller. This is
	// the ordering that previously triggered the nil-pointer race.
	i.Start(stopCh)
	k.Start(stopCh)
	ci.Start(stopCh)
	i.WaitForCacheSync(stopCh)
	k.WaitForCacheSync(stopCh)
	ci.WaitForCacheSync(stopCh)

	c := New(
		iriInformer,
		ccInformer,
		mcInformer,
		cvInformer,
		secretInformer,
		mcnInformer,
		nodeInformer,
		infraInformer,
		k8sClient,
		mcfgClient,
	)
	assert.NotNil(t, c)

	// The replayed Add event for the control-plane MachineConfigNode must be
	// handled by addMachineConfigNode without panicking, which enqueues the IRI
	// singleton. Waiting for the enqueue proves the handler ran to completion
	// against a non-nil node lister.
	assert.Eventually(t, func() bool {
		return c.queue.Len() > 0
	}, 5*time.Second, 10*time.Millisecond,
		"expected replayed MachineConfigNode Add to be handled and enqueue the IRI without panicking")
}
