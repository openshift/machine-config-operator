package operator

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
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
		clusterVersion                  *configv1.ClusterVersion
		expectedManagedBootImagesStatus opv1.ManagedBootImages
		expectedSkewEnforcementStatus   opv1.BootImageSkewEnforcementStatus
		annotationExpected              bool
		enableCPMSFeatureGate           bool
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
			expectedSkewEnforcementStatus:   apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
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
			expectedSkewEnforcementStatus:   apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.19.0"),
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
			name:                            "BareMetal platform (unsupported), skew enforcement manual mode expected",
			infra:                           buildInfra(withPlatformType(configv1.BareMetalPlatformType)),
			mcop:                            buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:                  buildClusterVersion("4.18.0"),
			annotationExpected:              false,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{},
			expectedSkewEnforcementStatus:   apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
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

			enabledFeatureGates := []configv1.FeatureGateName{features.FeatureGateManagedBootImages, features.FeatureGateManagedBootImagesAWS, features.FeatureGateManagedBootImagesvSphere, features.FeatureGateManagedBootImagesAzure, features.FeatureGateBootImageSkewEnforcement}
			if tc.enableCPMSFeatureGate {
				enabledFeatureGates = append(enabledFeatureGates, features.FeatureGateManagedBootImagesCPMS)
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
