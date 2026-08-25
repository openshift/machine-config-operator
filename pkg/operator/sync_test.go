package operator

import (
	"context"
	"errors"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/upgrademonitor"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	opv1 "github.com/openshift/api/operator/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcplister "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"

	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	fakemcopclientset "github.com/openshift/client-go/operator/clientset/versioned/fake"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamiclister"
)

func TestSyncCloudConfig(t *testing.T) {
	cases := []struct {
		name                        string
		infra                       *configv1.Infrastructure
		kubeCloudConfig             *corev1.ConfigMap
		expectError                 bool
		expectedCloudProviderConfig string
		expectedCABundle            []byte
	}{
		{
			name:  "no kube-cloud-config on optional platform",
			infra: buildInfra(withPlatformType(configv1.AWSPlatformType)),
		},
		{
			name:        "no kube-cloud-config on required platform",
			infra:       buildInfra(withPlatformType(configv1.AzurePlatformType)),
			expectError: true,
		},
		{
			name:        "no kube-cloud-config on optional platform with CloudConfig name",
			infra:       buildInfra(withPlatformType(configv1.AWSPlatformType), withCloudConfig()),
			expectError: true,
		},
		{
			name:                        "cloud.conf on required platform",
			infra:                       buildInfra(withPlatformType(configv1.AzurePlatformType)),
			kubeCloudConfig:             buildKubeCloudConfig(withCloudConf("test-cloud-conf")),
			expectedCloudProviderConfig: "test-cloud-conf",
		},
		{
			name:            "no cloud.conf on required platform",
			infra:           buildInfra(withPlatformType(configv1.AzurePlatformType)),
			kubeCloudConfig: buildKubeCloudConfig(),
			expectError:     true,
		},
		{
			name:            "no cloud.conf on optional platform",
			infra:           buildInfra(withPlatformType(configv1.AWSPlatformType)),
			kubeCloudConfig: buildKubeCloudConfig(),
		},
		{
			name:            "no cloud.conf on optional platform with CloudConfig name",
			infra:           buildInfra(withPlatformType(configv1.AWSPlatformType), withCloudConfig()),
			kubeCloudConfig: buildKubeCloudConfig(),
		},
		{
			name:             "CA bundle with no cloud.conf on optional platform",
			infra:            buildInfra(withPlatformType(configv1.AWSPlatformType), withCloudConfig()),
			kubeCloudConfig:  buildKubeCloudConfig(withCABundle("test-ca-bundle")),
			expectedCABundle: []byte("test-ca-bundle"),
		},
		{
			name:  "no kube-cloud-config on platform None",
			infra: buildInfra(withPlatformType(configv1.NonePlatformType)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			sharedInformer := informers.NewSharedInformerFactory(client, 0)
			cmInformer := sharedInformer.Core().V1().ConfigMaps()
			if tc.kubeCloudConfig != nil {
				cmInformer.Informer().GetIndexer().Add(tc.kubeCloudConfig)
			}
			optr := &Operator{
				clusterCmLister: cmInformer.Lister(),
			}
			spec := &mcfgv1.ControllerConfigSpec{}
			err := optr.syncCloudConfig(spec, tc.infra)
			if tc.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.expectedCloudProviderConfig, spec.CloudProviderConfig)
			assert.Equal(t, tc.expectedCABundle, spec.CloudProviderCAData)
		})
	}
}

func TestSyncMachineConfigNodesRetriesTransientAPIErrors(t *testing.T) {
	const (
		nodeName = "worker-0"
		poolName = "worker"
	)

	newExistingMCN := func(desired string) *mcfgv1.MachineConfigNode {
		return &mcfgv1.MachineConfigNode{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName},
			Spec: mcfgv1.MachineConfigNodeSpec{
				Node:          mcfgv1.MCOObjectReference{Name: nodeName},
				Pool:          mcfgv1.MCOObjectReference{Name: poolName},
				ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: desired},
			},
		}
	}

	tests := []struct {
		name          string
		desiredConfig string
		existing      []runtime.Object
		verb          string
	}{
		{
			name:          "list",
			desiredConfig: "rendered-worker-current",
			existing:      []runtime.Object{newExistingMCN("rendered-worker-current")},
			verb:          "list",
		},
		{
			name: "apply create",
			verb: "create",
		},
		{
			name:          "apply spec",
			desiredConfig: "rendered-worker-new",
			existing:      []runtime.Object{newExistingMCN(upgrademonitor.NotYetSet)},
			verb:          "patch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			optr, client := newMachineConfigNodeSyncTestOperator(t, test.desiredConfig, test.existing...)
			attempts := 0
			client.PrependReactor(test.verb, "machineconfignodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
				attempts++
				if attempts == 1 {
					return true, nil, apierrors.NewServiceUnavailable("storage is (re)initializing")
				}
				return false, nil, nil
			})

			if err := optr.syncMachineConfigNodes(context.Background(), nil, nil); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if attempts != 2 {
				t.Fatalf("expected 2 %s attempts, got %d", test.verb, attempts)
			}
		})
	}
}

func TestSyncMachineConfigNodesDoesNotRetryNonTransientError(t *testing.T) {
	optr, client := newMachineConfigNodeSyncTestOperator(t, "")
	attempts := 0
	client.PrependReactor("list", "machineconfignodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
		attempts++
		return true, nil, apierrors.NewBadRequest("invalid selector")
	})

	err := optr.syncMachineConfigNodes(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected non-transient error")
	}
	if !apierrors.IsBadRequest(err) {
		t.Fatalf("expected wrapped bad request, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 list attempt, got %d", attempts)
	}
}

func TestSyncMachineConfigNodesReturnsAfterTransientRetriesExhausted(t *testing.T) {
	optr, client := newMachineConfigNodeSyncTestOperator(t, "")
	attempts := 0
	client.PrependReactor("list", "machineconfignodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
		attempts++
		return true, nil, apierrors.NewServiceUnavailable("storage is (re)initializing")
	})

	err := optr.syncMachineConfigNodes(context.Background(), nil, nil)
	if !apierrors.IsServiceUnavailable(err) {
		t.Fatalf("expected wrapped service unavailable error, got %v", err)
	}
	if attempts != retry.DefaultRetry.Steps {
		t.Fatalf("expected retry to stop after %d attempts, got %d", retry.DefaultRetry.Steps, attempts)
	}
}

func TestRetryMachineConfigNodeAPIOperationDoesNotDoubleRetryConflicts(t *testing.T) {
	attempts := 0
	conflict := apierrors.NewConflict(schema.GroupResource{Group: mcfgv1.GroupName, Resource: "machineconfignodes"}, "worker-0", errors.New("conflict"))
	err := retryMachineConfigNodeAPIOperation(context.Background(), func(context.Context) error {
		attempts++
		return conflict
	})

	if !apierrors.IsConflict(err) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected conflict not to be retried by outer retry, got %d attempts", attempts)
	}
}

func TestRetryMachineConfigNodeAPIOperationStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := retryMachineConfigNodeAPIOperation(ctx, func(context.Context) error {
		attempts++
		cancel()
		return apierrors.NewServiceUnavailable("temporarily unavailable")
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected cancellation after 1 attempt, got %d", attempts)
	}
}

func TestSyncMachineConfigNodesCanceledContextDoesNotCallAPI(t *testing.T) {
	optr, client := newMachineConfigNodeSyncTestOperator(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := optr.syncMachineConfigNodes(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if actions := client.Actions(); len(actions) != 0 {
		t.Fatalf("expected no MachineConfigNode API calls after cancellation, got %v", actions)
	}
}

func TestSyncMachineConfigNodeErrorsDoNotIncludeNodeName(t *testing.T) {
	const nodeName = "worker-0"
	newExistingMCN := func() *mcfgv1.MachineConfigNode {
		return &mcfgv1.MachineConfigNode{
			ObjectMeta: metav1.ObjectMeta{Name: nodeName},
			Spec: mcfgv1.MachineConfigNodeSpec{
				Node:          mcfgv1.MCOObjectReference{Name: nodeName},
				Pool:          mcfgv1.MCOObjectReference{Name: "worker"},
				ConfigVersion: mcfgv1.MachineConfigNodeSpecMachineConfigVersion{Desired: upgrademonitor.NotYetSet},
			},
		}
	}
	tests := []struct {
		name            string
		desiredConfig   string
		existing        []runtime.Object
		verb            string
		expectedContext string
	}{
		{
			name:            "apply",
			verb:            "create",
			expectedContext: "applying MachineConfigNode",
		},
		{
			name:            "spec",
			desiredConfig:   "rendered-worker-new",
			existing:        []runtime.Object{newExistingMCN()},
			verb:            "patch",
			expectedContext: "applying MachineConfigNode spec",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			optr, client := newMachineConfigNodeSyncTestOperator(t, test.desiredConfig, test.existing...)
			client.PrependReactor(test.verb, "machineconfignodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("permanent failure")
			})

			err := optr.syncMachineConfigNodes(context.Background(), nil, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), test.expectedContext) {
				t.Fatalf("expected operation context %q, got %v", test.expectedContext, err)
			}
			if strings.Contains(err.Error(), nodeName) {
				t.Fatalf("expected error not to include node name %q, got %v", nodeName, err)
			}
		})
	}
}

func TestSyncMachineConfigNodesDeletion(t *testing.T) {
	const nodeName = "removed-worker-0"
	newMCN := func() *mcfgv1.MachineConfigNode {
		return &mcfgv1.MachineConfigNode{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
	}

	t.Run("retries transient error", func(t *testing.T) {
		optr, client := newMachineConfigNodeDeletionTestOperator(t, newMCN())
		attempts := 0
		client.PrependReactor("delete", "machineconfignodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
			attempts++
			if attempts == 1 {
				return true, nil, apierrors.NewServiceUnavailable("temporarily unavailable")
			}
			return false, nil, nil
		})

		if err := optr.syncMachineConfigNodes(context.Background(), nil, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if attempts != 2 {
			t.Fatalf("expected 2 delete attempts, got %d", attempts)
		}
	})

	t.Run("returns terminal error without node name", func(t *testing.T) {
		optr, client := newMachineConfigNodeDeletionTestOperator(t, newMCN())
		attempts := 0
		client.PrependReactor("delete", "machineconfignodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
			attempts++
			return true, nil, apierrors.NewBadRequest("invalid delete")
		})

		err := optr.syncMachineConfigNodes(context.Background(), nil, nil)
		if !apierrors.IsBadRequest(err) {
			t.Fatalf("expected wrapped bad request, got %v", err)
		}
		if !strings.Contains(err.Error(), "deleting MachineConfigNode") {
			t.Fatalf("expected delete operation context, got %v", err)
		}
		if strings.Contains(err.Error(), nodeName) {
			t.Fatalf("expected error not to include node name %q, got %v", nodeName, err)
		}
		if attempts != 1 {
			t.Fatalf("expected 1 delete attempt, got %d", attempts)
		}
	})
}

func newMachineConfigNodeDeletionTestOperator(t *testing.T, mcn *mcfgv1.MachineConfigNode) (*Operator, *fakeclientmachineconfigv1.Clientset) {
	t.Helper()
	optr, client := newMachineConfigNodeSyncTestOperator(t, "", mcn)
	kubeClient := fake.NewSimpleClientset()
	nodeInformer := informers.NewSharedInformerFactory(kubeClient, 0).Core().V1().Nodes()
	optr.nodeLister = nodeInformer.Lister()
	return optr, client
}

func newMachineConfigNodeSyncTestOperator(t *testing.T, desiredConfig string, existing ...runtime.Object) (*Operator, *fakeclientmachineconfigv1.Clientset) {
	t.Helper()

	const (
		nodeName = "worker-0"
		poolName = "worker"
	)

	client := fakeclientmachineconfigv1.NewSimpleClientset(existing...)
	mcfgInformerFactory := mcfginformers.NewSharedInformerFactory(client, 0)
	mcpInformer := mcfgInformerFactory.Machineconfiguration().V1().MachineConfigPools()
	if err := mcpInformer.Informer().GetIndexer().Add(helpers.NewMachineConfigPool(poolName, nil, helpers.WorkerSelector, "rendered-worker-current")); err != nil {
		t.Fatalf("adding MachineConfigPool to indexer: %v", err)
	}

	kubeClient := fake.NewSimpleClientset()
	kubeInformerFactory := informers.NewSharedInformerFactory(kubeClient, 0)
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeName,
			UID:    "worker-0-uid",
			Labels: map[string]string{"node-role/worker": ""},
			Annotations: map[string]string{
				daemonconsts.DesiredMachineConfigAnnotationKey: desiredConfig,
			},
		},
	}
	if err := nodeInformer.Informer().GetIndexer().Add(node); err != nil {
		t.Fatalf("adding Node to indexer: %v", err)
	}

	return &Operator{
		client:     client,
		nodeLister: nodeInformer.Lister(),
		mcpLister:  mcpInformer.Lister(),
		fgHandler: ctrlcommon.NewFeatureGatesHardcodedHandler(
			[]configv1.FeatureGateName{}, []configv1.FeatureGateName{},
		),
	}, client
}

type infraOption func(*configv1.Infrastructure)

func buildInfra(opts ...infraOption) *configv1.Infrastructure {
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
	}
	for _, o := range opts {
		o(infra)
	}
	return infra
}

func withCloudConfig() infraOption {
	return func(infra *configv1.Infrastructure) {
		infra.Spec.CloudConfig.Name = "cloud-provider-config"
	}
}

func withPlatformType(platformType configv1.PlatformType) infraOption {
	return func(infra *configv1.Infrastructure) {
		if infra.Status.PlatformStatus == nil {
			infra.Status.PlatformStatus = &configv1.PlatformStatus{}
		}
		infra.Status.PlatformStatus.Type = platformType
	}
}

func withControlPlaneTopology(topology configv1.TopologyMode) infraOption {
	return func(infra *configv1.Infrastructure) {
		infra.Status.ControlPlaneTopology = topology
	}
}

type kubeCloudConfigOption func(*corev1.ConfigMap)

func buildKubeCloudConfig(opts ...kubeCloudConfigOption) *corev1.ConfigMap {
	kubeCloudConfig := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "openshift-config-managed",
			Name:      "kube-cloud-config",
		},
	}
	for _, o := range opts {
		o(kubeCloudConfig)
	}
	return kubeCloudConfig
}

func withCloudConf(cloudConf string) kubeCloudConfigOption {
	return func(kubeCloudConfig *corev1.ConfigMap) {
		if kubeCloudConfig.Data == nil {
			kubeCloudConfig.Data = map[string]string{}
		}
		kubeCloudConfig.Data["cloud.conf"] = cloudConf
	}
}

func withCABundle(caBundle string) kubeCloudConfigOption {
	return func(kubeCloudConfig *corev1.ConfigMap) {
		if kubeCloudConfig.Data == nil {
			kubeCloudConfig.Data = map[string]string{}
		}
		kubeCloudConfig.Data["ca-bundle.pem"] = caBundle
	}
}

func TestMachineOSBuilderSecretReconciliation(t *testing.T) {
	masterPool := helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0")
	workerPool := helpers.NewMachineConfigPool("worker", nil, helpers.MasterSelector, "v0")
	infraPool := helpers.NewMachineConfigPool("infra", nil, helpers.MasterSelector, "v0")
	entitlementSecret := helpers.NewOpaqueSecret(ctrlcommon.SimpleContentAccessSecretName, ctrlcommon.OpenshiftConfigManagedNamespace, "abc")
	workerEntitlementSecret := helpers.NewOpaqueSecretWithOwnerPool(ctrlcommon.SimpleContentAccessSecretName+"-"+workerPool.Name, ctrlcommon.MCONamespace, "abc", *workerPool)
	infraEntitlementSecret := helpers.NewOpaqueSecretWithOwnerPool(ctrlcommon.SimpleContentAccessSecretName+"-"+infraPool.Name, ctrlcommon.MCONamespace, "abc", *infraPool)
	outOfDateInfraEntitlementSecret := helpers.NewOpaqueSecretWithOwnerPool(ctrlcommon.SimpleContentAccessSecretName+"-"+infraPool.Name, ctrlcommon.MCONamespace, "123", *infraPool)
	globalPullSecret := helpers.NewDockerCfgJSONSecret(ctrlcommon.GlobalPullSecretName, ctrlcommon.OpenshiftConfigNamespace, "abc")
	outOfDateGlobalPullSecretCopy := helpers.NewDockerCfgJSONSecret(ctrlcommon.GlobalPullSecretCopyName, ctrlcommon.MCONamespace, "123")
	globalPullSecretCopy := helpers.NewDockerCfgJSONSecret(ctrlcommon.GlobalPullSecretCopyName, ctrlcommon.MCONamespace, "abc")

	cases := []struct {
		name               string
		mcoSecrets         []*corev1.Secret
		ocSecrets          []*corev1.Secret
		ocManagedSecrets   []*corev1.Secret
		expectedMCOSecrets []corev1.Secret
		layeredMCPs        []*mcfgv1.MachineConfigPool
	}{
		{
			name:               "no entitlement secret on cluster, with opted-in pool",
			ocSecrets:          []*corev1.Secret{globalPullSecret.DeepCopy()},
			ocManagedSecrets:   []*corev1.Secret{},
			mcoSecrets:         []*corev1.Secret{},
			layeredMCPs:        []*mcfgv1.MachineConfigPool{infraPool.DeepCopy()},
			expectedMCOSecrets: []corev1.Secret{*globalPullSecretCopy.DeepCopy()},
		},
		{
			name:               "entitlement secret on cluster, with opted-in pool",
			ocSecrets:          []*corev1.Secret{globalPullSecret.DeepCopy()},
			ocManagedSecrets:   []*corev1.Secret{entitlementSecret.DeepCopy()},
			mcoSecrets:         []*corev1.Secret{},
			layeredMCPs:        []*mcfgv1.MachineConfigPool{infraPool.DeepCopy()},
			expectedMCOSecrets: []corev1.Secret{*infraEntitlementSecret.DeepCopy(), *globalPullSecretCopy.DeepCopy()},
		},
		{
			name:               "entitlement secret on cluster, with multiple opted-in pools",
			ocSecrets:          []*corev1.Secret{globalPullSecret.DeepCopy()},
			ocManagedSecrets:   []*corev1.Secret{entitlementSecret.DeepCopy()},
			mcoSecrets:         []*corev1.Secret{},
			layeredMCPs:        []*mcfgv1.MachineConfigPool{workerPool.DeepCopy(), infraPool.DeepCopy()},
			expectedMCOSecrets: []corev1.Secret{*workerEntitlementSecret.DeepCopy(), *infraEntitlementSecret.DeepCopy(), *globalPullSecretCopy.DeepCopy()},
		},
		{
			name:               "entitlement, cloned secret and global pull secret copy on cluster, with no opted-in pools",
			ocSecrets:          []*corev1.Secret{globalPullSecret.DeepCopy()},
			ocManagedSecrets:   []*corev1.Secret{entitlementSecret.DeepCopy()},
			mcoSecrets:         []*corev1.Secret{infraEntitlementSecret.DeepCopy(), globalPullSecretCopy.DeepCopy()},
			layeredMCPs:        []*mcfgv1.MachineConfigPool{},
			expectedMCOSecrets: []corev1.Secret{},
		},
		{
			name:               "entitlement and cloned secret on cluster, with an outdated cloned secret",
			ocSecrets:          []*corev1.Secret{globalPullSecret.DeepCopy()},
			ocManagedSecrets:   []*corev1.Secret{entitlementSecret.DeepCopy()},
			mcoSecrets:         []*corev1.Secret{outOfDateInfraEntitlementSecret.DeepCopy()},
			layeredMCPs:        []*mcfgv1.MachineConfigPool{infraPool.DeepCopy()},
			expectedMCOSecrets: []corev1.Secret{*infraEntitlementSecret.DeepCopy(), *globalPullSecretCopy.DeepCopy()},
		},
		{
			name:               "outdated global pull secret copy on cluster",
			ocSecrets:          []*corev1.Secret{globalPullSecret.DeepCopy()},
			ocManagedSecrets:   []*corev1.Secret{},
			mcoSecrets:         []*corev1.Secret{outOfDateGlobalPullSecretCopy.DeepCopy()},
			layeredMCPs:        []*mcfgv1.MachineConfigPool{infraPool.DeepCopy()},
			expectedMCOSecrets: []corev1.Secret{*globalPullSecretCopy.DeepCopy()},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create fake kube client & informers
			kubeClient := fake.NewSimpleClientset()
			sharedInformerFactory := informers.NewSharedInformerFactory(kubeClient, 0)
			mcoSecretInformer := sharedInformerFactory.Core().V1().Secrets()
			ocManagedSecretInformer := sharedInformerFactory.Core().V1().Secrets()
			ocSecretInformer := sharedInformerFactory.Core().V1().Secrets()

			// Add secrets to informer and client
			for _, secret := range tc.mcoSecrets {
				mcoSecretInformer.Informer().GetIndexer().Add(secret)
				_, err := kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, secret := range tc.ocManagedSecrets {
				ocManagedSecretInformer.Informer().GetIndexer().Add(secret)
				_, err := kubeClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigManagedNamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
				assert.NoError(t, err)
			}
			for _, secret := range tc.ocSecrets {
				ocSecretInformer.Informer().GetIndexer().Add(secret)
				_, err := kubeClient.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			// Create MCO specific clients
			mcfgClient := fakeclientmachineconfigv1.NewSimpleClientset()
			mcfgInformerFactory := mcfginformers.NewSharedInformerFactoryWithOptions(mcfgClient, 0, mcfginformers.WithNamespace(ctrlcommon.MCONamespace))
			mcpInformer := mcfgInformerFactory.Machineconfiguration().V1().MachineConfigPools()

			// Add all pools to mcpInformer
			mcpInformer.Informer().GetIndexer().Add(masterPool)
			mcpInformer.Informer().GetIndexer().Add(workerPool)
			mcpInformer.Informer().GetIndexer().Add(infraPool)

			optr := &Operator{
				client:                mcfgClient,
				kubeClient:            kubeClient,
				mcpLister:             mcpInformer.Lister(),
				mcoSecretLister:       mcoSecretInformer.Lister(),
				ocSecretLister:        ocSecretInformer.Lister(),
				ocManagedSecretLister: ocManagedSecretInformer.Lister(),
			}
			err := optr.reconcileSimpleContentAccessSecrets(tc.layeredMCPs)
			assert.NoError(t, err)

			err = optr.reconcileGlobalPullSecretCopy(tc.layeredMCPs)
			assert.NoError(t, err)

			// Verify secrets in MCO namespace are as expected
			secrets, err := kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
			assert.NoError(t, err)
			assert.ElementsMatch(t, secrets.Items, tc.expectedMCOSecrets)
		})
	}
}

func TestSyncMachineConfiguration(t *testing.T) {
	cases := []struct {
		name                            string
		mcop                            *opv1.MachineConfiguration
		infra                           *configv1.Infrastructure
		clusterVersion                  *configv1.ClusterVersion
		expectedManagedBootImagesStatus opv1.ManagedBootImages
		expectedSkewEnforcementStatus   opv1.BootImageSkewEnforcementStatus
		annotationExpected              bool
		enableCPMSFeatureGate           bool
		provisioningCRPresent           bool
		provisioningOSDownloadURL       string
	}{
		{
			name:               "AWS platform, no existing config, opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:               "AWS platform, existing enabled config, no opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithMachineSetsEnabled(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:               "AWS platform, existing disabled config, no opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithMachineSetsDisabled(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		{
			name:               "GCP platform, no existing config, opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:               buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:               "GCP platform, existing enabled config, no opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:               buildMachineConfigurationWithMachineSetsEnabled(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},

		{
			name:               "GCP platform, existing parial config, no opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:               buildMachineConfigurationWithMachineSetsPartiallyEnabled(map[string]string{"test": "boot"}),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.Partial, Partial: &opv1.PartialSelector{
						MachineResourceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"test": "boot"},
						},
					}}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		{
			name:               "GCP platform, existing disabled config, no opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:               buildMachineConfigurationWithMachineSetsDisabled(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		{
			name:               "Azure platform, no existing config, opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:               buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:               "vsphere platform, no existing config, opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.VSpherePlatformType)),
			mcop:               buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                            "bare metal platform, unsupported platform, no configuration expected",
			infra:                           buildInfra(withPlatformType(configv1.BareMetalPlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:                  buildClusterVersion("4.18.0"),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{},
			expectedSkewEnforcementStatus:   apihelpers.GetSkewEnforcementStatusNone(),
		},
		{
			name:               "vsphere platform, empty list config, no opt-in expected",
			infra:              buildInfra(withPlatformType(configv1.VSpherePlatformType)),
			mcop:               buildMachineConfigurationWithEmptyListBootImageConfiguration(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		// CPMS test cases - feature gate enabled
		{
			name:                  "AWS platform, no existing config, default CPMS disabled",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "GCP platform, no existing config, default CPMS disabled",
			infra:                 buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "Azure platform, no existing config, default CPMS disabled",
			infra:                 buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "AWS platform, CPMS enabled in spec, MachineSets should still follow platform default (All)",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithCPMSEnabled(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true, // MachineSets get auto opted-in since no opinion exists
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "Azure platform, CPMS enabled in spec, MachineSets should still follow platform default (All)",
			infra:                 buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:                  buildMachineConfigurationWithCPMSEnabled(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "AWS platform, MachineSets enabled in spec, CPMS should remain disabled (no opinion)",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithMachineSetsEnabled(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    false,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "AWS platform, both MachineSets and CPMS enabled in spec",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithMachineSetsEnabledCPMSEnabled(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    false,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "AWS platform, MachineSets disabled but CPMS enabled in spec, CPMS opinion reflected",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithMachineSetsDisabledCPMSEnabled(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    false,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		{
			name:                  "AWS platform, MachineSets partially enabled and CPMS enabled in spec, both opinions reflected",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithMachineSetsPartiallyEnabledCPMSEnabled(map[string]string{"test": "boot"}),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    false,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.Partial, Partial: &opv1.PartialSelector{
						MachineResourceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"test": "boot"},
						},
					}}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		{
			name:                  "AWS platform, empty list config, no opt-in expected",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithEmptyListBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    false,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		{
			name:                            "bare metal platform, unsupported platform, no MachineSet/CPMS configuration expected",
			infra:                           buildInfra(withPlatformType(configv1.BareMetalPlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:                  buildClusterVersion("4.19.0"),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{},
			expectedSkewEnforcementStatus:   apihelpers.GetSkewEnforcementStatusNone(),
		},
		{
			name:                  "vsphere platform, CPMS updates unsupported, MachineSet configuration expected, no CPMS configuration expected",
			infra:                 buildInfra(withPlatformType(configv1.VSpherePlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.19.0"),
			annotationExpected:    true,
			enableCPMSFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.19.0"),
		},
		// Skew enforcement test cases
		{
			name:               "AWS platform, boot images enabled, skew enforcement automatic mode expected",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithBootImageEnabledAndNoSkewEnforcement(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:               "AWS platform, boot images disabled, skew enforcement manual mode expected",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithBootImageDisabledAndNoSkewEnforcement(),
			clusterVersion:     buildClusterVersion("4.17.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.17.0"),
		},
		{
			name:               "AWS platform, spec defines manual mode, status should reflect spec",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithSkewEnforcementManual("4.16.0"),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.16.0"),
		},
		{
			name:               "AWS platform, spec defines none mode, status should reflect none",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithSkewEnforcementNone(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusNone(),
		},
		{
			name:               "GCP platform, boot images enabled, skew enforcement automatic mode expected",
			infra:              buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:               buildMachineConfigurationWithBootImageEnabledAndNoSkewEnforcement(),
			clusterVersion:     buildClusterVersion("4.19.1"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.19.1"),
		},
		{
			name:                            "BareMetal platform, Provisioning CR absent, skew enforcement None expected",
			infra:                           buildInfra(withPlatformType(configv1.BareMetalPlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:                  buildClusterVersion("4.18.0"),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{},
			expectedSkewEnforcementStatus:   apihelpers.GetSkewEnforcementStatusNone(),
		},
		{
			name:                            "BareMetal platform, Provisioning CR present with empty URL, skew enforcement None expected",
			infra:                           buildInfra(withPlatformType(configv1.BareMetalPlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:                  buildClusterVersion("4.18.0"),
			annotationExpected:              false,
			provisioningCRPresent:           true,
			provisioningOSDownloadURL:       "",
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{},
			expectedSkewEnforcementStatus:   apihelpers.GetSkewEnforcementStatusNone(),
		},
		{
			name:                            "BareMetal platform, legacy qcow2 path (provisioningOSDownloadURL set), skew enforcement Manual expected",
			infra:                           buildInfra(withPlatformType(configv1.BareMetalPlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:                  buildClusterVersion("4.9.0"),
			annotationExpected:              false,
			provisioningCRPresent:           true,
			provisioningOSDownloadURL:       "https://example.com/rhcos.qcow2",
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{},
			expectedSkewEnforcementStatus:   apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.9.0"),
		},
		{
			name:               "AWS platform, cluster version with multiple history entries",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithBootImageEnabledAndNoSkewEnforcement(),
			clusterVersion:     buildClusterVersionWithMultipleHistory("4.19.0", "4.18.0", "4.17.0"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.17.0"),
		},
		{
			name:               "AWS platform, CI version format should be parsed correctly",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:               buildMachineConfigurationWithBootImageEnabledAndNoSkewEnforcement(),
			clusterVersion:     buildClusterVersion("4.18.0-0.ci-2024-01-01-000000"),
			annotationExpected: false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:               "SNO cluster, no skew enforcement spec, skew enforcement defaults to None",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType), withControlPlaneTopology(configv1.SingleReplicaTopologyMode)),
			mcop:               buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusNone(),
		},
		{
			name:               "SNO cluster, spec defines manual mode, status should reflect spec",
			infra:              buildInfra(withPlatformType(configv1.AWSPlatformType), withControlPlaneTopology(configv1.SingleReplicaTopologyMode)),
			mcop:               buildMachineConfigurationWithSkewEnforcementManual("4.17.0"),
			clusterVersion:     buildClusterVersion("4.18.0"),
			annotationExpected: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.17.0"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			infraIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			infraIndexer.Add(tc.infra)
			mcopIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			mcopIndexer.Add(tc.mcop)
			mcpIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			clusterVersionIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			if tc.clusterVersion != nil {
				clusterVersionIndexer.Add(tc.clusterVersion)
			}

			enabledFeatureGates := []configv1.FeatureGateName{features.FeatureGateBootImageSkewEnforcement}
			if tc.enableCPMSFeatureGate {
				enabledFeatureGates = append(enabledFeatureGates, features.FeatureGateManagedBootImagesCPMS)
			}

			provisioningGVR := schema.GroupVersionResource{Group: "metal3.io", Version: "v1alpha1", Resource: "provisionings"}
			provisioningIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if tc.provisioningCRPresent {
				provisioningIndexer.Add(&unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "metal3.io/v1alpha1",
					"kind":       "Provisioning",
					"metadata":   map[string]interface{}{"name": "provisioning-configuration"},
					"spec":       map[string]interface{}{"provisioningOSDownloadURL": tc.provisioningOSDownloadURL},
				}})
			}
			optr := &Operator{
				eventRecorder: &record.FakeRecorder{},
				fgHandler: ctrlcommon.NewFeatureGatesHardcodedHandler(
					enabledFeatureGates, []configv1.FeatureGateName{},
				),
				infraLister:          configlistersv1.NewInfrastructureLister(infraIndexer),
				mcopLister:           mcoplistersv1.NewMachineConfigurationLister(mcopIndexer),
				mcopClient:           fakemcopclientset.NewSimpleClientset(tc.mcop),
				mcpLister:            mcplister.NewMachineConfigPoolLister(mcpIndexer),
				clusterVersionLister: configlistersv1.NewClusterVersionLister(clusterVersionIndexer),
				provisioningLister:   dynamiclister.New(provisioningIndexer, provisioningGVR),
			}
			err := optr.syncMachineConfiguration(nil, nil)
			assert.NoError(t, err)
			mcop, err := optr.mcopClient.OperatorV1().MachineConfigurations().Get(context.TODO(), "cluster", metav1.GetOptions{})
			assert.NoError(t, err)
			// Ensure ManagedBootImagesStatus and annotations are as expected
			assert.Equal(t, tc.expectedManagedBootImagesStatus, mcop.Status.ManagedBootImagesStatus)
			assert.Equal(t, tc.annotationExpected, metav1.HasAnnotation(mcop.ObjectMeta, ctrlcommon.BootImageOptedInAnnotation))
			// Ensure BootImageSkewEnforcementStatus is as expected
			assert.Equal(t, tc.expectedSkewEnforcementStatus, mcop.Status.BootImageSkewEnforcementStatus)
		})
	}
}

func buildMachineConfigurationWithMachineSetsDisabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithMachineSetsPartiallyEnabled(matchLabels map[string]string) *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.Partial, Partial: &opv1.PartialSelector{
						MachineResourceSelector: &metav1.LabelSelector{
							MatchLabels: matchLabels,
						},
					}}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithMachineSetsEnabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithNoBootImageConfiguration() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{},
		},
	}
}

func buildMachineConfigurationWithEmptyListBootImageConfiguration() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{MachineManagers: []opv1.MachineManager{}},
		},
	}
}

func buildMachineConfigurationWithCPMSEnabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithMachineSetsEnabledCPMSEnabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithMachineSetsDisabledCPMSEnabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithMachineSetsPartiallyEnabledCPMSEnabled(matchLabels map[string]string) *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.Partial, Partial: &opv1.PartialSelector{
						MachineResourceSelector: &metav1.LabelSelector{
							MatchLabels: matchLabels,
						},
					}}},
					{Resource: opv1.ControlPlaneMachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
		},
	}
}

// Helper functions for building ClusterVersion objects
func buildClusterVersion(version string) *configv1.ClusterVersion {
	return &configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "version",
		},
		Status: configv1.ClusterVersionStatus{
			History: []configv1.UpdateHistory{
				{
					State:   configv1.CompletedUpdate,
					Version: version,
				},
			},
		},
	}
}

func buildClusterVersionWithMultipleHistory(versions ...string) *configv1.ClusterVersion {
	history := make([]configv1.UpdateHistory, len(versions))
	for i, v := range versions {
		state := configv1.CompletedUpdate
		if i == 0 {
			state = configv1.PartialUpdate // Most recent is partial (in progress)
		}
		history[i] = configv1.UpdateHistory{
			State:   state,
			Version: v,
		}
	}
	return &configv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name: "version",
		},
		Status: configv1.ClusterVersionStatus{
			History: history,
		},
	}
}

// Helper functions for building MachineConfiguration with skew enforcement
func buildMachineConfigurationWithSkewEnforcementManual(ocpVersion string) *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			BootImageSkewEnforcement: opv1.BootImageSkewEnforcementConfig{
				Mode: opv1.BootImageSkewEnforcementConfigModeManual,
				Manual: opv1.ClusterBootImageManual{
					Mode:       opv1.ClusterBootImageSpecModeOCPVersion,
					OCPVersion: ocpVersion,
				},
			},
		},
	}
}

func buildMachineConfigurationWithSkewEnforcementNone() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			BootImageSkewEnforcement: opv1.BootImageSkewEnforcementConfig{
				Mode: opv1.BootImageSkewEnforcementConfigModeNone,
			},
		},
	}
}

func buildMachineConfigurationWithBootImageEnabledAndNoSkewEnforcement() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithBootImageDisabledAndNoSkewEnforcement() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
		},
	}
}
