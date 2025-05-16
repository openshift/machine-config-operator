package operator

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	opv1 "github.com/openshift/api/operator/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

func TestAdminAck(t *testing.T) {
	cases := []struct {
		name                string
		infra               *configv1.Infrastructure
		mcop                *opv1.MachineConfiguration
		adminCM             *corev1.ConfigMap
		node                *corev1.Node
		expectBootImageGate bool
		expectAArch64Gate   bool
	}{
		{
			name:    "non AWS/GCP platform, with no boot image configuration",
			infra:   buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:    buildMachineConfigurationWithNoBootImageConfiguration(),
			adminCM: buildAdminAckConfigMapWithData(nil),
		},
		{
			name:                "AWS platform, with no boot image configuration",
			infra:               buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                buildMachineConfigurationWithNoBootImageConfiguration(),
			adminCM:             buildAdminAckConfigMapWithData(nil),
			expectBootImageGate: true,
		},
		{
			name:                "GCP platform, with no boot image configuration",
			infra:               buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:                buildMachineConfigurationWithNoBootImageConfiguration(),
			adminCM:             buildAdminAckConfigMapWithData(nil),
			expectBootImageGate: true,
		},
		{
			name:    "non AWS/GCP platform, with a boot image configuration",
			infra:   buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:    buildMachineConfigurationWithBootImageUpdateDisabled(),
			adminCM: buildAdminAckConfigMapWithData(nil),
		},
		{
			name:    "AWS platform, with a boot image configuration",
			infra:   buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:    buildMachineConfigurationWithBootImageUpdateDisabled(),
			adminCM: buildAdminAckConfigMapWithData(map[string]string{BootImageAWSGCPKey: BootImageAWSGCPMsg}),
		},
		{
			name:    "GCP platform, with a boot image configuration",
			infra:   buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:    buildMachineConfigurationWithBootImageUpdateDisabled(),
			adminCM: buildAdminAckConfigMapWithData(map[string]string{BootImageAWSGCPKey: BootImageAWSGCPMsg}),
		},
		{
			name:              "aarch64 Nodes present",
			infra:             buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:              buildMachineConfigurationWithBootImageUpdateDisabled(),
			adminCM:           buildAdminAckConfigMapWithData(nil),
			node:              buildNodeWithArch("arm64"),
			expectAArch64Gate: true,
		},
		{
			name:    "aarch64 Nodes not present",
			infra:   buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:    buildMachineConfigurationWithBootImageUpdateDisabled(),
			adminCM: buildAdminAckConfigMapWithData(nil),
			node:    buildNodeWithArch("amd64"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create clients and listers
			kubeClient := fake.NewSimpleClientset()
			infraIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			infraIndexer.Add(tc.infra)
			mcopIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			mcopIndexer.Add(tc.mcop)

			configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			configMapIndexer.Add(tc.adminCM)
			_, err := kubeClient.CoreV1().ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Create(context.TODO(), tc.adminCM, metav1.CreateOptions{})
			assert.NoError(t, err)

			nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			if tc.node != nil {
				nodeIndexer.Add(tc.node)
				_, err = kubeClient.CoreV1().Nodes().Create(context.TODO(), tc.node, metav1.CreateOptions{})
				assert.NoError(t, err)
			}

			optr := &Operator{
				kubeClient:               kubeClient,
				infraLister:              configlistersv1.NewInfrastructureLister(infraIndexer),
				mcopLister:               mcoplistersv1.NewMachineConfigurationLister(mcopIndexer),
				ocManagedConfigMapLister: corelisterv1.NewConfigMapLister(configMapIndexer),
				nodeLister:               corelisterv1.NewNodeLister(nodeIndexer),
			}
			err = optr.syncAdminAckConfigMap()
			assert.NoError(t, err)

			adminCM, err := kubeClient.CoreV1().ConfigMaps(ctrlcommon.OpenshiftConfigManagedNamespace).Get(context.TODO(), AdminAckGatesConfigMapName, metav1.GetOptions{})
			assert.NoError(t, err)

			_, gateFound := adminCM.Data[BootImageAWSGCPKey]
			assert.Equal(t, tc.expectBootImageGate, gateFound)

			_, gateFound = adminCM.Data[AArch64BootImageKey]
			assert.Equal(t, tc.expectAArch64Gate, gateFound)
		})
	}
}

func buildMachineConfigurationWithBootImageUpdateDisabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{Spec: opv1.MachineConfigurationSpec{ManagedBootImages: apihelpers.GetManagedBootImagesWithUpdateDisabled()}, ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
}

func buildMachineConfigurationWithNoBootImageConfiguration() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{Spec: opv1.MachineConfigurationSpec{ManagedBootImages: apihelpers.GetManagedBootImagesWithNoConfiguration()}, ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
}

func buildAdminAckConfigMapWithData(data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: AdminAckGatesConfigMapName, Namespace: ctrlcommon.OpenshiftConfigManagedNamespace},
		Data:       data,
	}
}

func buildNodeWithArch(arch string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				Architecture: arch,
			},
		},
	}
}
