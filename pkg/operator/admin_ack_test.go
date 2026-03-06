package operator

import (
	"maps"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	opv1 "github.com/openshift/api/operator/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestSyncAdminAckConfigMap(t *testing.T) {
	cases := []struct {
		name           string
		infra          *configv1.Infrastructure
		mcop           *opv1.MachineConfiguration
		existingCMData map[string]string
		noCM           bool
		expectError    bool
		expectKeySet   bool
	}{
		{
			name:         "Azure cluster with no boot image config gets guard key added",
			infra:        buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:         buildMachineConfigurationWithNoBootImageConfiguration(),
			expectKeySet: true,
		},
		{
			name:         "Azure cluster with MachineSets explicitly configured does not get guard key",
			infra:        buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:         buildMachineConfigurationWithMachineSetsEnabled(),
			expectKeySet: false,
		},
		{
			name:         "Azure cluster with MachineSets disabled does not get guard key",
			infra:        buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:         buildMachineConfigurationWithMachineSetsDisabled(),
			expectKeySet: false,
		},
		{
			name:         "vSphere cluster with no boot image config gets guard key added",
			infra:        buildInfra(withPlatformType(configv1.VSpherePlatformType)),
			mcop:         buildMachineConfigurationWithNoBootImageConfiguration(),
			expectKeySet: true,
		},
		{
			name:         "vSphere cluster with MachineSets explicitly configured does not get guard key",
			infra:        buildInfra(withPlatformType(configv1.VSpherePlatformType)),
			mcop:         buildMachineConfigurationWithMachineSetsEnabled(),
			expectKeySet: false,
		},
		{
			name:         "AWS cluster with no boot image config does not get Azure/vSphere guard key",
			infra:        buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:         buildMachineConfigurationWithNoBootImageConfiguration(),
			expectKeySet: false,
		},
		{
			name:         "GCP cluster with no boot image config does not get Azure/vSphere guard key",
			infra:        buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:         buildMachineConfigurationWithNoBootImageConfiguration(),
			expectKeySet: false,
		},
		{
			name:         "AzureStackCloud cluster does not get guard key",
			infra:        buildInfra(withPlatformType(configv1.AzurePlatformType), withAzureStackCloud()),
			mcop:         buildMachineConfigurationWithNoBootImageConfiguration(),
			expectKeySet: false,
		},
		{
			name:           "stale guard key is removed when Azure cluster gains explicit config",
			infra:          buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:           buildMachineConfigurationWithMachineSetsEnabled(),
			existingCMData: map[string]string{BootImageAzureVSphereKey: BootImageAzureVSphereMsg},
			expectKeySet:   false,
		},
		{
			name:           "stale guard key is removed when vSphere cluster gains explicit config",
			infra:          buildInfra(withPlatformType(configv1.VSpherePlatformType)),
			mcop:           buildMachineConfigurationWithMachineSetsEnabled(),
			existingCMData: map[string]string{BootImageAzureVSphereKey: BootImageAzureVSphereMsg},
			expectKeySet:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adminGatesCM := buildAdminAckConfigMap(tc.existingCMData)

			kubeClient := fake.NewSimpleClientset(adminGatesCM)
			sharedInformer := informers.NewSharedInformerFactory(kubeClient, 0)
			cmInformer := sharedInformer.Core().V1().ConfigMaps()

			if !tc.noCM {
				cmInformer.Informer().GetIndexer().Add(adminGatesCM)
			}

			infraIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			infraIndexer.Add(tc.infra)

			mcopIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			mcopIndexer.Add(tc.mcop)

			optr := &Operator{
				kubeClient:      kubeClient,
				clusterCmLister: cmInformer.Lister(),
				infraLister:     configlistersv1.NewInfrastructureLister(infraIndexer),
				mcopLister:      mcoplistersv1.NewMachineConfigurationLister(mcopIndexer),
			}

			err := optr.syncAdminAckConfigMap()
			if tc.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Re-fetch the ConfigMap from the fake client to check what was written
			updatedCM, err := kubeClient.CoreV1().ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Get(
				t.Context(), AdminAckGatesConfigMapName, metav1.GetOptions{})
			require.NoError(t, err)

			_, keyPresent := updatedCM.Data[BootImageAzureVSphereKey]
			assert.Equal(t, tc.expectKeySet, keyPresent)
		})
	}
}

// buildAdminAckConfigMap builds the admin-gates ConfigMap with optional pre-existing data.
func buildAdminAckConfigMap(data map[string]string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      AdminAckGatesConfigMapName,
			Namespace: ctrlcommon.OpenshiftConfigManagedNamespace,
		},
		Data: map[string]string{},
	}
	maps.Copy(cm.Data, data)
	return cm
}

// withAzureStackCloud sets the Azure platform to AzureStackCloud.
func withAzureStackCloud() infraOption {
	return func(infra *configv1.Infrastructure) {
		if infra.Status.PlatformStatus == nil {
			infra.Status.PlatformStatus = &configv1.PlatformStatus{}
		}
		infra.Status.PlatformStatus.Azure = &configv1.AzurePlatformStatus{
			CloudName: configv1.AzureStackCloud,
		}
	}
}
