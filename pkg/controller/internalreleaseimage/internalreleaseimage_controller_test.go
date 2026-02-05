package internalreleaseimage

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
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
		verify         func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig)
	}{
		{
			name:           "feature inactive",
			initialObjects: objs(),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.Nil(t, actualIRI)
				assert.Nil(t, actualMasterMC)
				assert.Nil(t, actualWorkerMC)
			},
		},
		{
			name:           "add finalizer if not present",
			initialObjects: objs(iri(), clusterVersion(), cconfig(), iriCertSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.Len(t, actualIRI.Finalizers, 2)
				assert.Contains(t, actualIRI.Finalizers, masterName())
				assert.Contains(t, actualIRI.Finalizers, workerName())
			},
		},
		{
			name: "update status if not set",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig(), iriCertSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.Len(t, actualIRI.Status.Releases, 1)
				assert.Equal(t, actualIRI.Status.Releases[0].Name, "ocp-release-bundle-4.21.5-x86_64")
				assert.Equal(t, actualIRI.Status.Releases[0].Image, "ocp-4.21-release-pullspec")
				assert.Equal(t, actualIRI.Status.Releases[0].Conditions[0].Type, string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable))
				assert.Equal(t, actualIRI.Status.Releases[0].Conditions[0].Status, metav1.ConditionTrue)
				assert.Equal(t, actualIRI.Status.Releases[0].Conditions[0].Message, "Release bundle is available")
			},
		},
		{
			name:           "generate iri machine-config if not present",
			initialObjects: objs(iri(), clusterVersion(), cconfig(), iriCertSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfig(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
		},
		{
			name: "avoid machine-config drifting",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig(), iriCertSecret(),
				machineconfigmaster().ignition("some garbage"),
				machineconfigworker().ignition("other garbage")),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfig(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
		},
		{
			name: "refresh machine-config on controllerConfig update",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().dockerRegistryImage("a-new-docker-registry-image-pullspec"), iriCertSecret(),
				machineconfigmaster(), machineconfigworker()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfig(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
		},
		{
			name: "machine-config cascade delete on iri removal - removes the first machineconfig",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()).setDeletionTimestamp(),
				clusterVersion(), cconfig(), iriCertSecret(),
				machineconfigmaster(), machineconfigworker()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.NotNil(t, iri)
				assert.Equal(t, []string{workerName()}, actualIRI.Finalizers)
				assert.Nil(t, actualMasterMC)
				assert.NotNil(t, actualWorkerMC)
			},
		},
		{
			name: "machine-config cascade delete on iri removal - then removes the remaining machineconfig",
			initialObjects: objs(
				iri().finalizer(workerName()).setDeletionTimestamp(),
				clusterVersion(), cconfig(), iriCertSecret(),
				machineconfigworker()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.NotNil(t, iri)
				assert.Empty(t, actualIRI.Finalizers)
				assert.Nil(t, actualMasterMC)
				assert.Nil(t, actualWorkerMC)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := tc.initialObjects()
			f := newFixture(t, objs)
			f.run(ctrlcommon.InternalReleaseImageInstanceName)

			if tc.verify != nil {
				actualIRI, err := f.client.MachineconfigurationV1alpha1().InternalReleaseImages().Get(context.TODO(), ctrlcommon.InternalReleaseImageInstanceName, v1.GetOptions{})
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

// The fixture used to setup and run the controller.
type fixture struct {
	t *testing.T

	client       *fake.Clientset
	k8sClient    *k8sfake.Clientset
	configClient *fakeconfigv1client.Clientset

	iriLister            []*mcfgv1alpha1.InternalReleaseImage
	ccLister             []*mcfgv1.ControllerConfig
	mcLister             []*mcfgv1.MachineConfig
	secretLister         []*corev1.Secret
	clusterVersionLister []*configv1.ClusterVersion

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
		case *corev1.Secret, *corev1.ConfigMap, *corev1.Pod:
			f.k8sObjects = append(f.k8sObjects, obj)
			switch o := obj.(type) {
			case *corev1.Secret:
				f.secretLister = append(f.secretLister, o)
			}
		case *configv1.ClusterVersion:
			f.configObjects = append(f.configObjects, obj)
		default:
			f.objects = append(f.objects, obj)
			switch o := obj.(type) {
			case *mcfgv1alpha1.InternalReleaseImage:
				f.iriLister = append(f.iriLister, o)
			case *mcfgv1.ControllerConfig:
				f.ccLister = append(f.ccLister, o)
			case *mcfgv1.MachineConfig:
				f.mcLister = append(f.mcLister, o)
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
		i.Machineconfiguration().V1alpha1().InternalReleaseImages(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().MachineConfigs(),
		ci.Config().V1().ClusterVersions(),
		k.Core().V1().Secrets(),
		f.k8sClient,
		f.client,
	)

	alwaysReady := func() bool { return true }
	c.iriListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.clusterVersionListerSynced = alwaysReady
	c.secretListerSynced = alwaysReady
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
		i.Machineconfiguration().V1alpha1().InternalReleaseImages().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.secretLister {
		k.Core().V1().Secrets().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.clusterVersionLister {
		ci.Config().V1().ClusterVersions().Informer().GetIndexer().Add(c)
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
