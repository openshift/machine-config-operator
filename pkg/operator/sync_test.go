package operator

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

func TestRemoveEtcdNoProxyConfig(t *testing.T) {
	cases := []struct {
		name            string
		proxy           string
		expectedNoProxy string
	}{
		{
			name:            "empty NoProxy",
			proxy:           "",
			expectedNoProxy: "",
		},
		{
			name:            "NoProxy etcd with domain",
			proxy:           ".cluster.local,.svc,1001:db8::/120,127.0.0.1,2002:db8::/53,2003:db8::/112,api-int.a.com,etcd-0.a.com,etcd-1.a.com,etcd-2.a.com,localhost",
			expectedNoProxy: ".cluster.local,.svc,1001:db8::/120,127.0.0.1,2002:db8::/53,2003:db8::/112,api-int.a.com,localhost",
		},
		{
			name:            "NoProxy etcd last with domain",
			proxy:           ".cluster.local,.svc,1001:db8::/120,127.0.0.1,2002:db8::/53,2003:db8::/112,api-int.a.com,etcd-0.a.com,etcd-1.a.com,etcd-2.a.com",
			expectedNoProxy: ".cluster.local,.svc,1001:db8::/120,127.0.0.1,2002:db8::/53,2003:db8::/112,api-int.a.com",
		},
		{
			name:            "NoProxy etcd without domain",
			proxy:           ".cluster.local,.svc,1001:db8::/120,127.0.0.1,2002:db8::/53,2003:db8::/112,api-int.a.com,etcd-0.,etcd-1.,etcd-2.,localhost",
			expectedNoProxy: ".cluster.local,.svc,1001:db8::/120,127.0.0.1,2002:db8::/53,2003:db8::/112,api-int.a.com,localhost",
		},
		{
			name:            "NoProxy etcd last without domain",
			proxy:           ".cluster.local,.svc,1001:db8::/120,127.0.0.1,2002:db8::/53,2003:db8::/112,api-int.a.com,etcd-0.,etcd-1.,etcd-2.",
			expectedNoProxy: ".cluster.local,.svc,1001:db8::/120,127.0.0.1,2002:db8::/53,2003:db8::/112,api-int.a.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noProxy := removeEtcdNoProxyConfig(tc.proxy)
			assert.Equal(t, tc.expectedNoProxy, noProxy)
		})
	}
}

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
