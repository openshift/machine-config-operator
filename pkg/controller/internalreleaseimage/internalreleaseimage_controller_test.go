package internalreleaseimage

import (
	"context"
	"encoding/json"
	"strings"
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
		name             string
		initialObjects   func() []runtime.Object
		verify           func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig)
		verifyPullSecret func(t *testing.T, f *fixture)
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
			name:           "generate iri machine-config with auth",
			initialObjects: objs(iri(), clusterVersion(), cconfig().withDNS("example.com"), iriCertSecret(), iriAuthSecret(), pullSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfigWithAuth(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
		},
		{
			name:           "merge iri auth into pull secret",
			initialObjects: objs(iri(), clusterVersion(), cconfig().withDNS("example.com"), iriCertSecret(), iriAuthSecret(), pullSecret()),
			verify: func(t *testing.T, actualIRI *mcfgv1alpha1.InternalReleaseImage, actualMasterMC *mcfgv1.MachineConfig, actualWorkerMC *mcfgv1.MachineConfig) {
				verifyInternalReleaseMasterMachineConfigWithAuth(t, actualMasterMC)
				verifyInternalReleaseWorkerMachineConfig(t, actualWorkerMC)
			},
			verifyPullSecret: func(t *testing.T, f *fixture) {
				ps, err := f.k8sClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(
					context.TODO(), ctrlcommon.GlobalPullSecretName, metav1.GetOptions{})
				assert.NoError(t, err)
				var dockerConfig map[string]interface{}
				err = json.Unmarshal(ps.Data[corev1.DockerConfigJsonKey], &dockerConfig)
				assert.NoError(t, err)
				auths := dockerConfig["auths"].(map[string]interface{})
				iriEntry, ok := auths["api-int.example.com:22625"]
				assert.True(t, ok, "IRI auth entry should be present in pull secret")
				assert.NotNil(t, iriEntry)
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
			if tc.verifyPullSecret != nil {
				tc.verifyPullSecret(t, f)
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
	mcpLister            []*mcfgv1.MachineConfigPool
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
			case *mcfgv1.MachineConfigPool:
				f.mcpLister = append(f.mcpLister, o)
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
		i.Machineconfiguration().V1().MachineConfigPools(),
		ci.Config().V1().ClusterVersions(),
		k.Core().V1().Secrets(),
		f.k8sClient,
		f.client,
	)

	alwaysReady := func() bool { return true }
	c.iriListerSynced = alwaysReady
	c.ccListerSynced = alwaysReady
	c.mcListerSynced = alwaysReady
	c.mcpListerSynced = alwaysReady
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
	for _, c := range f.mcpLister {
		i.Machineconfiguration().V1().MachineConfigPools().Informer().GetIndexer().Add(c)
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

// getAuthSecret fetches the auth secret from the fake k8s client.
func (f *fixture) getAuthSecret() *corev1.Secret {
	s, err := f.k8sClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(
		context.TODO(), ctrlcommon.InternalReleaseImageAuthSecretName, metav1.GetOptions{})
	if err != nil {
		f.t.Fatalf("failed to get auth secret: %v", err)
	}
	return s
}

// getPullSecret fetches the global pull secret from the fake k8s client.
func (f *fixture) getPullSecret() *corev1.Secret {
	s, err := f.k8sClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(
		context.TODO(), ctrlcommon.GlobalPullSecretName, metav1.GetOptions{})
	if err != nil {
		f.t.Fatalf("failed to get pull secret: %v", err)
	}
	return s
}

// TestCredentialRotation tests the reconcileAuthCredentials state machine.
// Each test case sets up state for a specific phase of the rotation and runs
// a single sync to verify the controller produces the correct output.
// See docs/IRIAuthCredentialRotation.md for the full rotation design.
func TestCredentialRotation(t *testing.T) {
	const baseDomain = "example.com"
	const oldPassword = "oldpassword"
	const newPassword = "newpassword"
	const changedPassword = "changedpassword"

	// Generate valid bcrypt htpasswd entries for test fixtures.
	oldHtpasswd, err := GenerateHtpasswdEntry(IRIBaseUsername, oldPassword)
	assert.NoError(t, err)

	dualHtpasswd, err := GenerateDualHtpasswd(IRIBaseUsername, oldPassword, "openshift1", newPassword)
	assert.NoError(t, err)

	cases := []struct {
		name           string
		initialObjects func() []runtime.Object
		verify         func(t *testing.T, f *fixture)
	}{
		// ===== Phase 1: Dual htpasswd written, MCP rolling out =====
		{
			name: "phase 1: rotation triggers dual htpasswd when deployed differs from desired",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(newPassword, oldHtpasswd),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				authSecret := f.getAuthSecret()
				htpasswd := string(authSecret.Data["htpasswd"])
				assert.True(t, HtpasswdHasValidEntry(htpasswd, IRIBaseUsername, oldPassword),
					"dual htpasswd should contain old credentials")
				assert.True(t, HtpasswdHasValidEntry(htpasswd, "openshift1", newPassword),
					"dual htpasswd should contain new credentials")
			},
		},
		{
			name: "phase 1: mid-rotation password change regenerates dual htpasswd",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(changedPassword, dualHtpasswd),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				authSecret := f.getAuthSecret()
				htpasswd := string(authSecret.Data["htpasswd"])
				assert.True(t, HtpasswdHasValidEntry(htpasswd, IRIBaseUsername, oldPassword),
					"dual htpasswd should preserve old credentials")
				assert.True(t, HtpasswdHasValidEntry(htpasswd, "openshift1", changedPassword),
					"dual htpasswd should be regenerated with changed password")
				assert.False(t, HtpasswdHasValidEntry(htpasswd, "openshift1", newPassword),
					"dual htpasswd should not contain stale new password")
			},
		},
		{
			name: "phase 1: user edits htpasswd directly triggers regeneration",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(newPassword, "openshift:invalidhash\n"),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				authSecret := f.getAuthSecret()
				htpasswd := string(authSecret.Data["htpasswd"])
				assert.True(t, HtpasswdHasValidEntry(htpasswd, IRIBaseUsername, oldPassword),
					"regenerated dual htpasswd should contain old credentials")
				assert.True(t, HtpasswdHasValidEntry(htpasswd, "openshift1", newPassword),
					"regenerated dual htpasswd should contain new credentials")
			},
		},
		// ===== Phase 2: All MCPs updated, pull secret about to be updated =====
		{
			name: "phase 2: all pools updated advances to pull secret update",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(newPassword, dualHtpasswd),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				ps := f.getPullSecret()
				u, p := ExtractIRICredentialsFromPullSecret(ps.Data[corev1.DockerConfigJsonKey], baseDomain)
				assert.Equal(t, "openshift1", u, "pull secret should have new username")
				assert.Equal(t, newPassword, p, "pull secret should have new password")
			},
		},
		{
			name: "phase 2: pools not updated blocks pull secret update",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(newPassword, dualHtpasswd),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker").notUpdated(),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				ps := f.getPullSecret()
				_, p := ExtractIRICredentialsFromPullSecret(ps.Data[corev1.DockerConfigJsonKey], baseDomain)
				assert.Empty(t, p, "pull secret should NOT be updated while pools are rolling out")
			},
		},
		{
			name: "phase 2: password change before pull secret update prevents stale update",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(changedPassword, dualHtpasswd),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				ps := f.getPullSecret()
				_, p := ExtractIRICredentialsFromPullSecret(ps.Data[corev1.DockerConfigJsonKey], baseDomain)
				assert.Empty(t, p, "pull secret should not be updated with stale password")
				authSecret := f.getAuthSecret()
				htpasswd := string(authSecret.Data["htpasswd"])
				assert.True(t, HtpasswdHasValidEntry(htpasswd, "openshift1", changedPassword),
					"htpasswd should be regenerated with changed password")
			},
		},
		// ===== Phase 2b: Pull secret updated, re-render in progress =====
		{
			name: "phase 2b: inconsistent credentials across pools causes wait",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(newPassword, dualHtpasswd),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, "openshift1", newPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, IRIBaseUsername, oldPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				authSecret := f.getAuthSecret()
				assert.Equal(t, dualHtpasswd, string(authSecret.Data["htpasswd"]),
					"htpasswd should not be modified during cross-pool inconsistency")
			},
		},
		// ===== Phase 3: Deployed matches desired, cleaning up =====
		{
			name: "phase 3: deployed matches desired cleans up dual htpasswd",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(newPassword, dualHtpasswd),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, "openshift1", newPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, "openshift1", newPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				authSecret := f.getAuthSecret()
				htpasswd := string(authSecret.Data["htpasswd"])
				assert.True(t, HtpasswdHasValidEntry(htpasswd, "openshift1", newPassword),
					"cleaned up htpasswd should have valid entry for deployed credentials")
				lines := strings.Split(strings.TrimSpace(htpasswd), "\n")
				assert.Equal(t, 1, len(lines), "cleaned up htpasswd should have single entry")
			},
		},
		{
			name: "phase 3: user edits htpasswd in steady state triggers repair",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(newPassword, "openshift1:invalidhash\n"),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, "openshift1", newPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, "openshift1", newPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				authSecret := f.getAuthSecret()
				htpasswd := string(authSecret.Data["htpasswd"])
				assert.True(t, HtpasswdHasValidEntry(htpasswd, "openshift1", newPassword),
					"repaired htpasswd should have valid hash")
			},
		},
		{
			name: "phase 3: password change during cleanup starts new rotation",
			initialObjects: objs(
				iri().finalizer(masterName(), workerName()),
				clusterVersion(), cconfig().withDNS(baseDomain), iriCertSecret(),
				iriAuthSecretCustom(changedPassword, dualHtpasswd),
				pullSecret(),
				renderedMC("rendered-master").withIRICredentials(baseDomain, "openshift1", newPassword),
				renderedMC("rendered-worker").withIRICredentials(baseDomain, "openshift1", newPassword),
				mcp("master", "rendered-master"),
				mcp("worker", "rendered-worker"),
				machineconfigmaster(), machineconfigworker(),
			),
			verify: func(t *testing.T, f *fixture) {
				authSecret := f.getAuthSecret()
				htpasswd := string(authSecret.Data["htpasswd"])
				assert.True(t, HtpasswdHasValidEntry(htpasswd, "openshift1", newPassword),
					"new rotation should preserve current deployed credentials")
				assert.True(t, HtpasswdHasValidEntry(htpasswd, "openshift2", changedPassword),
					"new rotation should use next generation username")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := tc.initialObjects()
			f := newFixture(t, objs)
			f.run(ctrlcommon.InternalReleaseImageInstanceName)
			tc.verify(t, f)
		})
	}
}
