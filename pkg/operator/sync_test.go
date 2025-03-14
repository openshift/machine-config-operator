package operator

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	opv1 "github.com/openshift/api/operator/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcplister "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"

	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	fakemcopclientset "github.com/openshift/client-go/operator/clientset/versioned/fake"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"
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
			mcfgInformerFactory := mcfginformers.NewFilteredSharedInformerFactory(mcfgClient, 0, ctrlcommon.MCONamespace, nil)
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
		expectedManagedBootImagesStatus opv1.ManagedBootImages
		annotationExpected              bool
	}{
		{
			name:                            "AWS platform, no existing config, opt-in expected",
			infra:                           buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			annotationExpected:              true,
			expectedManagedBootImagesStatus: apihelpers.GetManagedBootImagesWithUpdateEnabled(),
		},
		{
			name:                            "AWS platform, existing enabled config, no opt-in expected",
			infra:                           buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                            buildMachineConfigurationWithBootImageUpdateEnabled(),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: apihelpers.GetManagedBootImagesWithUpdateEnabled(),
		},
		{
			name:                            "AWS platform, existing disabled config, no opt-in expected",
			infra:                           buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                            buildMachineConfigurationWithBootImageUpdateDisabled(),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: apihelpers.GetManagedBootImagesWithUpdateDisabled(),
		},
		{
			name:                            "GCP platform, no existing config, opt-in expected",
			infra:                           buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			annotationExpected:              true,
			expectedManagedBootImagesStatus: apihelpers.GetManagedBootImagesWithUpdateEnabled(),
		},
		{
			name:                            "GCP platform, existing enabled config, no opt-in expected",
			infra:                           buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:                            buildMachineConfigurationWithBootImageUpdateEnabled(),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: apihelpers.GetManagedBootImagesWithUpdateEnabled(),
		},
		{
			name:                            "GCP platform, existing disabled config, no opt-in expected",
			infra:                           buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:                            buildMachineConfigurationWithBootImageUpdateDisabled(),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: apihelpers.GetManagedBootImagesWithUpdateDisabled(),
		},
		{
			name:                            "Azure platform, no existing config, no opt-in expected",
			infra:                           buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: apihelpers.GetManagedBootImagesWithNoConfiguration(),
		},
		{
			name:                            "vsphere platform, no existing config, no opt-in expected",
			infra:                           buildInfra(withPlatformType(configv1.VSpherePlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: apihelpers.GetManagedBootImagesWithNoConfiguration(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			infraIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			infraIndexer.Add(tc.infra)
			mcopIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			mcopIndexer.Add(tc.mcop)
			mcpIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			optr := &Operator{
				eventRecorder: &record.FakeRecorder{},
				fgAccessor: featuregates.NewHardcodedFeatureGateAccess(
					[]configv1.FeatureGateName{features.FeatureGateManagedBootImages, features.FeatureGateManagedBootImagesAWS, features.FeatureGateNodeDisruptionPolicy}, []configv1.FeatureGateName{},
				),
				infraLister: configlistersv1.NewInfrastructureLister(infraIndexer),
				mcopLister:  mcoplistersv1.NewMachineConfigurationLister(mcopIndexer),
				mcopClient:  fakemcopclientset.NewSimpleClientset(tc.mcop),
				mcpLister:   mcplister.NewMachineConfigPoolLister(mcpIndexer),
			}
			err := optr.syncMachineConfiguration(nil, nil)
			assert.NoError(t, err)
			mcop, err := optr.mcopClient.OperatorV1().MachineConfigurations().Get(context.TODO(), "cluster", metav1.GetOptions{})
			assert.NoError(t, err)
			// Ensure ManagedBootImagesStatus and annotations are as expected
			assert.Equal(t, tc.expectedManagedBootImagesStatus, mcop.Status.ManagedBootImagesStatus)
			assert.Equal(t, tc.annotationExpected, metav1.HasAnnotation(mcop.ObjectMeta, ctrlcommon.BootImageOptedInAnnotation))
		})
	}
}

func buildMachineConfigurationWithBootImageUpdateDisabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{Spec: opv1.MachineConfigurationSpec{ManagedBootImages: apihelpers.GetManagedBootImagesWithUpdateDisabled()}, ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
}

func buildMachineConfigurationWithBootImageUpdateEnabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{Spec: opv1.MachineConfigurationSpec{ManagedBootImages: apihelpers.GetManagedBootImagesWithUpdateEnabled()}, ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
}

func buildMachineConfigurationWithNoBootImageConfiguration() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{Spec: opv1.MachineConfigurationSpec{ManagedBootImages: apihelpers.GetManagedBootImagesWithNoConfiguration()}, ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
}
