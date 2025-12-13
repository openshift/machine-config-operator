package internalreleaseimage

import (
	"context"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
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
			initialObjects: objs(iri(), cconfig(), iriCertSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				assert.Equal(t, iri().finalizer(masterName(), workerName()).build(), actualIRI)
			},
		},
		{
			name:           "generate iri machine-config if not present",
			initialObjects: objs(iri(), cconfig(), iriCertSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfig(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
		},
		{
			name: "avoid machine-config drifting",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				cconfig(), iriCertSecret(),
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
				cconfig().dockerRegistryImage("a-new-docker-registry-image-pullspec"), iriCertSecret(),
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
				cconfig(), iriCertSecret(),
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
				cconfig(), iriCertSecret(),
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
			for _, obj := range objs {
				switch o := obj.(type) {
				case *mcfgv1alpha1.InternalReleaseImage:
					f.iriLister = append(f.iriLister, o)
				case *mcfgv1.ControllerConfig:
					f.ccLister = append(f.ccLister, o)
				case *mcfgv1.MachineConfig:
					f.mcLister = append(f.mcLister, o)
				}
			}

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

	client    *fake.Clientset
	k8sClient *k8sfake.Clientset
	iriLister []*mcfgv1alpha1.InternalReleaseImage
	ccLister  []*mcfgv1.ControllerConfig
	mcLister  []*mcfgv1.MachineConfig

	controller *Controller
	objects    []runtime.Object
	k8sObjects []runtime.Object
}

func newFixture(t *testing.T, objects []runtime.Object) *fixture {
	f := &fixture{t: t}
	f.setupObjects(objects)
	f.controller = f.newController()
	return f
}

func (f *fixture) setupObjects(objs []runtime.Object) {
	for _, o := range objs {
		switch o.(type) {
		case *corev1.Secret, *corev1.ConfigMap, *corev1.Pod:
			f.k8sObjects = append(f.k8sObjects, o)
		default:
			f.objects = append(f.objects, o)
		}
	}
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.k8sClient = k8sfake.NewSimpleClientset(f.k8sObjects...)
	i := informers.NewSharedInformerFactory(f.client, func() time.Duration { return 0 }())

	c := New(
		i.Machineconfiguration().V1alpha1().InternalReleaseImages(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().MachineConfigs(),
		f.k8sClient,
		f.client,
	)

	alwaysReady := func() bool { return true }
	c.iriListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	stopCh := make(chan struct{})
	defer close(stopCh)
	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)

	for _, c := range f.iriLister {
		i.Machineconfiguration().V1alpha1().InternalReleaseImages().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.ccLister {
		i.Machineconfiguration().V1().ControllerConfigs().Informer().GetIndexer().Add(c)
	}
	for _, c := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(c)
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
