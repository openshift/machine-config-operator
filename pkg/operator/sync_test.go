package operator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"text/template"

	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	"github.com/openshift/machine-config-operator/pkg/apihelpers"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	opv1 "github.com/openshift/api/operator/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcplister "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"

	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	fakemcopclientset "github.com/openshift/client-go/operator/clientset/versioned/fake"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/dynamiclister"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
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

func withBareMetalVIPManagement(vipManagement configv1.VIPManagementType) infraOption {
	return func(infra *configv1.Infrastructure) {
		if infra.Status.PlatformStatus == nil {
			infra.Status.PlatformStatus = &configv1.PlatformStatus{}
		}
		infra.Status.PlatformStatus.Type = configv1.BareMetalPlatformType
		infra.Status.PlatformStatus.BareMetal = &configv1.BareMetalPlatformStatus{
			VIPManagement:        vipManagement,
			APIServerInternalIPs: []string{"192.168.111.5"},
			IngressIPs:           []string{"192.168.111.4"},
		}
	}
}

func withDualStackVIPs() infraOption {
	return func(infra *configv1.Infrastructure) {
		infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs = []string{"192.168.111.5", "fd2e:6f44:5dd8:c956::5"}
		infra.Status.PlatformStatus.BareMetal.IngressIPs = []string{"192.168.111.4", "fd2e:6f44:5dd8:c956::4"}
	}
}

func buildBGPVIPConfigCR(opts ...func(*mcfgv1alpha1.BGPVIPConfig)) *mcfgv1alpha1.BGPVIPConfig {
	cr := &mcfgv1alpha1.BGPVIPConfig{
		TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1alpha1.GroupVersion.String(), Kind: "BGPVIPConfig"},
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Generation: 3},
		Spec: mcfgv1alpha1.BGPVIPConfigSpec{
			LocalASN: 64512,
			DefaultPeers: []mcfgv1alpha1.BGPVIPPeer{
				{PeerAddress: "192.168.111.1", PeerASN: 64513},
				{PeerAddress: "fd2e:6f44:5dd8:c956::1", PeerASN: 64513},
			},
		},
	}
	for _, o := range opts {
		o(cr)
	}
	return cr
}

// TestBGPVIPPeersJSONFromCR locks the serialized transport to the Dev
// Preview config.json schema so templates and runtimecfg stay untouched.
// The dual-stack golden payload was captured from a live cluster
// (openshift-network-operator/bgp-vip-config, data["config.json"]).
func TestBGPVIPPeersJSONFromCR(t *testing.T) {
	goldenDualStack := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513},{"peerAddress":"fd2e:6f44:5dd8:c956::1","peerASN":64513}],"apiVIPs":["192.168.111.5","fd2e:6f44:5dd8:c956::5"],"ingressVIPs":["192.168.111.4","fd2e:6f44:5dd8:c956::4"]}`

	cases := []struct {
		name     string
		cr       *mcfgv1alpha1.BGPVIPConfig
		infra    *configv1.Infrastructure
		resolver bgpVIPPasswordResolver
		expected string
	}{
		{
			name:     "dual-stack, no overrides (live-cluster golden)",
			cr:       buildBGPVIPConfigCR(),
			infra:    buildInfra(withBareMetalVIPManagement("BGP"), withDualStackVIPs()),
			expected: goldenDualStack,
		},
		{
			name: "enums, timers, port, password, communities and host overrides",
			cr: buildBGPVIPConfigCR(func(cr *mcfgv1alpha1.BGPVIPConfig) {
				cr.Spec.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{
					PeerAddress:          "192.168.111.1",
					PeerASN:              64513,
					PasswordSecret:       mcfgv1alpha1.BGPVIPPasswordSecret{Name: "bgp-peer-pw"},
					Port:                 1790,
					FailureDetection:     mcfgv1alpha1.FailureDetectionBFD,
					PeerReachability:     mcfgv1alpha1.PeerReachabilityMultiHop,
					HoldTimeSeconds:      90,
					KeepaliveTimeSeconds: 30,
				}}
				cr.Spec.Communities = []mcfgv1alpha1.BGPCommunity{"64512:100", "64512:4200000000:300"}
				cr.Spec.HostOverrides = []mcfgv1alpha1.BGPVIPHostPeers{{
					Hostname: "master-0",
					Peers: []mcfgv1alpha1.BGPVIPPeer{{
						PeerAddress:      "192.168.111.2",
						PeerASN:          64514,
						FailureDetection: mcfgv1alpha1.FailureDetectionHoldTimer,
						PeerReachability: mcfgv1alpha1.PeerReachabilityDirectlyConnected,
						HoldTimeSeconds:  180,
					}},
				}}
			}),
			infra: buildInfra(withBareMetalVIPManagement("BGP")),
			resolver: func(name string) (string, error) {
				if name != "bgp-peer-pw" {
					return "", fmt.Errorf("unexpected secret name %q", name)
				}
				return "s3cret", nil
			},
			// HoldTimer / DirectlyConnected are the renderer defaults and stay
			// absent, exactly as the installer's ConfigMap producer writes them;
			// timers are bare seconds.
			expected: `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"s3cret","port":1790,"bfdEnabled":"true","ebgpMultiHop":"true","holdTime":"90","keepaliveTime":"30"}],"communities":["64512:100","64512:4200000000:300"],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"holdTime":"180"}]}}`,
		},
		{
			name: "omitted enums and zero timers are absent",
			cr: buildBGPVIPConfigCR(func(cr *mcfgv1alpha1.BGPVIPConfig) {
				cr.Spec.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{
					PeerAddress: "192.168.111.1",
					PeerASN:     64513,
				}}
			}),
			infra:    buildInfra(withBareMetalVIPManagement("BGP")),
			expected: `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513}],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := tc.resolver
			if resolver == nil {
				// No-password peers must never trigger a resolution.
				resolver = func(name string) (string, error) {
					t.Fatalf("unexpected password resolution for %q", name)
					return "", nil
				}
			}
			got, err := bgpVIPPeersJSONFromCR(tc.cr, tc.infra, resolver)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
			// Sanity: the payload must be valid, compact JSON.
			var buf bytes.Buffer
			require.NoError(t, json.Compact(&buf, []byte(got)))
			assert.Equal(t, got, buf.String())
		})
	}
}

// TestBGPVIPPeersJSONFromCRResolverError locks the failure contract: a
// resolver error fails the whole render, for default peers and overrides.
func TestBGPVIPPeersJSONFromCRResolverError(t *testing.T) {
	cr := buildBGPVIPConfigCR(func(cr *mcfgv1alpha1.BGPVIPConfig) {
		cr.Spec.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{
			PeerAddress:    "192.168.111.1",
			PeerASN:        64513,
			PasswordSecret: mcfgv1alpha1.BGPVIPPasswordSecret{Name: "missing"},
		}}
	})
	infra := buildInfra(withBareMetalVIPManagement("BGP"))
	failing := func(name string) (string, error) { return "", fmt.Errorf("secret %q not found", name) }
	_, err := bgpVIPPeersJSONFromCR(cr, infra, failing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `secret "missing" not found`)
	assert.Contains(t, err.Error(), "192.168.111.1")
}

// TestBGPVIPPasswordFromSecret locks the Secret validation shared by the
// day-2 and bootstrap resolvers.
func TestBGPVIPPasswordFromSecret(t *testing.T) {
	build := func(secretType corev1.SecretType, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer-pw", Namespace: "openshift-config"},
			Type:       secretType,
			Data:       data,
		}
	}
	t.Run("valid", func(t *testing.T) {
		got, err := bgpVIPPasswordFromSecret(build(corev1.SecretTypeBasicAuth, map[string][]byte{"password": []byte("s3cret")}))
		require.NoError(t, err)
		assert.Equal(t, "s3cret", got)
	})
	t.Run("wrong type", func(t *testing.T) {
		_, err := bgpVIPPasswordFromSecret(build(corev1.SecretTypeOpaque, map[string][]byte{"password": []byte("s3cret")}))
		assert.ErrorContains(t, err, "kubernetes.io/basic-auth")
	})
	t.Run("missing key", func(t *testing.T) {
		_, err := bgpVIPPasswordFromSecret(build(corev1.SecretTypeBasicAuth, nil))
		assert.ErrorContains(t, err, `non-empty "password" key`)
	})
	t.Run("over 80 bytes", func(t *testing.T) {
		_, err := bgpVIPPasswordFromSecret(build(corev1.SecretTypeBasicAuth, map[string][]byte{"password": bytes.Repeat([]byte("x"), 81)}))
		assert.ErrorContains(t, err, "80-byte TCP MD5 limit")
	})
}

func newBGPVIPConfigLister(t *testing.T, crs ...*mcfgv1alpha1.BGPVIPConfig) dynamiclister.Lister {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, cr := range crs {
		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
		require.NoError(t, err)
		require.NoError(t, indexer.Add(&unstructured.Unstructured{Object: content}))
	}
	return dynamiclister.New(indexer, bgpVIPConfigGVR)
}

func TestSyncBGPVIPPeersJSON(t *testing.T) {
	goldenJSON := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513},{"peerAddress":"fd2e:6f44:5dd8:c956::1","peerASN":64513}],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"]}`
	withPasswordSecret := func(cr *mcfgv1alpha1.BGPVIPConfig) {
		cr.Spec.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{
			PeerAddress:    "192.168.111.1",
			PeerASN:        64513,
			PasswordSecret: mcfgv1alpha1.BGPVIPPasswordSecret{Name: "bgp-peer-pw"},
		}}
	}
	peerPasswordSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer-pw", Namespace: "openshift-config"},
		Type:       corev1.SecretTypeBasicAuth,
		Data:       map[string][]byte{"password": []byte("s3cret")},
	}
	cases := []struct {
		name                    string
		infra                   *configv1.Infrastructure
		cr                      *mcfgv1alpha1.BGPVIPConfig
		secrets                 []runtime.Object
		expectError             bool
		expectRenderFailed      bool
		ccApplyError            error
		expectedBGPVIPPeersJSON string
		expectedRendered        *metav1.ConditionStatus
	}{
		{
			name:  "non-BareMetal platform is a no-op",
			infra: buildInfra(withPlatformType(configv1.AWSPlatformType)),
		},
		{
			name:  "BareMetal without BGP VIP management is a no-op",
			infra: buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("")),
		},
		{
			// User-managed load balancer: BGP mode without VIPs renders
			// keepalived-neither-BGP, and the peers ingestion must not
			// degrade the operator over the (rightfully) absent CR.
			name: "BGP without VIPs is a no-op",
			infra: buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP"),
				func(infra *configv1.Infrastructure) {
					infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs = nil
				}),
		},
		{
			name:                    "BGP enabled with BGPVIPConfig present",
			infra:                   buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP")),
			cr:                      buildBGPVIPConfigCR(),
			expectedBGPVIPPeersJSON: goldenJSON,
			expectedRendered:        ptr.To(metav1.ConditionTrue),
		},
		{
			name:                    "ControllerConfig apply failure reports Rendered=False",
			infra:                   buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP")),
			cr:                      buildBGPVIPConfigCR(),
			ccApplyError:            errors.New("controllerconfigs is forbidden"),
			expectedBGPVIPPeersJSON: goldenJSON,
			expectedRendered:        ptr.To(metav1.ConditionFalse),
		},
		{
			name:             "BGP enabled with missing BGPVIPConfig degrades",
			infra:            buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP")),
			expectError:      true,
			expectedRendered: nil, // no CR to write status to
		},
		{
			name:                    "peer passwordSecret is resolved into the JSON password",
			infra:                   buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP")),
			cr:                      buildBGPVIPConfigCR(withPasswordSecret),
			secrets:                 []runtime.Object{peerPasswordSecret},
			expectedBGPVIPPeersJSON: `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"s3cret"}],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"]}`,
			expectedRendered:        ptr.To(metav1.ConditionTrue),
		},
		{
			// Missing/invalid password Secret => sync error (Degraded) and
			// Rendered=False reported at render time, same semantics as a
			// missing CR.
			name:               "missing password Secret degrades and reports Rendered=False",
			infra:              buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP")),
			cr:                 buildBGPVIPConfigCR(withPasswordSecret),
			expectError:        true,
			expectRenderFailed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, mcfgv1alpha1.AddToScheme(scheme))
			var objs []runtime.Object
			var crs []*mcfgv1alpha1.BGPVIPConfig
			if tc.cr != nil {
				objs = append(objs, tc.cr)
				crs = append(crs, tc.cr)
			}
			dynamicClient := fakedynamic.NewSimpleDynamicClient(scheme, objs...)
			var patched []byte
			dynamicClient.PrependReactor("patch", "bgpvipconfigs", func(action clienttesting.Action) (bool, runtime.Object, error) {
				patchAction := action.(clienttesting.PatchAction)
				assert.Equal(t, "status", action.GetSubresource(), "status subresource expected")
				assert.Equal(t, types.ApplyPatchType, patchAction.GetPatchType(), "server-side apply expected")
				patched = patchAction.GetPatch()
				content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tc.cr)
				require.NoError(t, err)
				return true, &unstructured.Unstructured{Object: content}, nil
			})
			optr := &Operator{
				bgpVIPConfigLister: newBGPVIPConfigLister(t, crs...),
				bgpVIPConfigClient: dynamicClient.Resource(bgpVIPConfigGVR),
				kubeClient:         fake.NewSimpleClientset(tc.secrets...),
			}
			spec := &mcfgv1.ControllerConfigSpec{}
			// Pre-seed a stale pending report: every sync must clear it so a
			// deleted CR or a disabled feature cannot be re-reported.
			optr.bgpVIPConfigToReport = &mcfgv1alpha1.BGPVIPConfig{}
			err := optr.syncBGPVIPPeersJSON(spec, tc.infra)
			if tc.expectError {
				assert.Error(t, err)
				assert.Empty(t, spec.BGPVIPPeersJSON)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedBGPVIPPeersJSON, spec.BGPVIPPeersJSON)
			}
			if tc.expectRenderFailed {
				// A render failure with a readable CR reports
				// Rendered=False/RenderFailed immediately, and leaves no
				// pending report behind.
				require.NotNil(t, patched, "a Rendered=False status apply is expected at render time")
				var applied map[string]interface{}
				require.NoError(t, json.Unmarshal(patched, &applied))
				conds := applied["status"].(map[string]interface{})["conditions"].([]interface{})
				require.Len(t, conds, 1)
				cond := conds[0].(map[string]interface{})
				assert.Equal(t, mcfgv1alpha1.BGPVIPConfigRendered, cond["type"])
				assert.Equal(t, string(metav1.ConditionFalse), cond["status"])
				assert.Equal(t, "RenderFailed", cond["reason"])
				assert.Contains(t, cond["message"], "bgp-peer-pw")
				assert.Nil(t, optr.bgpVIPConfigToReport, "no pending report after a render failure")
				return
			}
			// Rendered must not be reported by the render step itself: the
			// status write only happens after the ControllerConfig apply
			// outcome is known (live validation caught Rendered=True while
			// the fleet still ran the previous generation's peers).
			assert.Nil(t, patched, "no status write expected before the ControllerConfig apply")
			optr.reportBGPVIPConfigRendered(tc.ccApplyError)
			if tc.expectedRendered == nil {
				assert.Nil(t, patched, "no status write expected")
				assert.Nil(t, optr.bgpVIPConfigToReport, "the stale pending report must have been cleared by the sync")
				return
			}
			require.NotNil(t, patched, "a status apply is expected")
			var applied map[string]interface{}
			require.NoError(t, json.Unmarshal(patched, &applied))
			status := applied["status"].(map[string]interface{})
			// The API has no status-level observedGeneration; the condition
			// carries it (asserted below) and nothing else may be written.
			assert.NotContains(t, status, "observedGeneration")
			conds := status["conditions"].([]interface{})
			require.Len(t, conds, 1)
			cond := conds[0].(map[string]interface{})
			assert.Equal(t, mcfgv1alpha1.BGPVIPConfigRendered, cond["type"])
			assert.Equal(t, string(*tc.expectedRendered), cond["status"])
			assert.Equal(t, float64(tc.cr.Generation), cond["observedGeneration"])
			if *tc.expectedRendered == metav1.ConditionFalse {
				assert.Equal(t, "RenderFailed", cond["reason"])
				assert.Contains(t, cond["message"], "controllerconfigs is forbidden")
			}
			// The pending report is consumed exactly once: the pointer is
			// cleared, and a second ControllerConfig apply (without a fresh
			// render) must not re-apply status from a stale CR.
			assert.Nil(t, optr.bgpVIPConfigToReport, "pending report must be cleared after being consumed")
			patched = nil
			optr.reportBGPVIPConfigRendered(tc.ccApplyError)
			assert.Nil(t, patched, "a second ControllerConfig apply must not re-report status")
		})
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

// newNamespacedIndexer returns an indexer configured with the namespace index,
// as required by the namespaced core listers (Secrets, ServiceAccounts).
func newNamespacedIndexer(t *testing.T, objs ...interface{}) cache.Indexer {
	t.Helper()
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, o := range objs {
		require.NoError(t, idx.Add(o), "could not add %T to the test indexer", o)
	}
	return idx
}

// TestGetImageRegistryPullSecretsIRIMerge verifies that getImageRegistryPullSecrets
// merges InternalReleaseImage (IRI) registry credentials into the assembled
// image-registry pull secret when IRI is in use, and leaves the secret untouched
// when IRI is absent. This blob feeds ControllerConfig.Spec.InternalRegistryPullSecret,
// which the daemon writes to /etc/mco/internal-registry-pull-secret.json for the
// OS-update image-pull path.
func TestGetImageRegistryPullSecretsIRIMerge(t *testing.T) {
	const (
		baseDomain  = "example.com"
		iriPassword = "s3cr3t"
	)
	expectedIRIAuth := base64.StdEncoding.EncodeToString([]byte(ctrlcommon.IRIRegistryUsername + ":" + iriPassword))
	iriAPIIntHost := "api-int." + baseDomain + ":22625"
	iriLocalHost := "localhost:22625"

	// Common fixtures independent of whether IRI is enabled.
	imageRegistryCO := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "image-registry"}}
	clusterDNS := &configv1.DNS{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       configv1.DNSSpec{BaseDomain: baseDomain},
	}
	// machine-os-puller SA with no image pull secrets; the cluster pull secret
	// alone keeps the assembled "auths" map non-empty so the merge path runs.
	machineOSPullerSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-os-puller", Namespace: ctrlcommon.MCONamespace},
	}
	populatedPullSecretContent := `{"auths":{"registry.example.com":{"auth":"` +
		base64.StdEncoding.EncodeToString([]byte("user:pass")) + `"}}}`

	// IRI-specific fixtures.
	iriInstance := &mcfgv1.InternalReleaseImage{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.InternalReleaseImageInstanceName},
	}
	iriAuthSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.InternalReleaseImageAuthSecretName, Namespace: ctrlcommon.MCONamespace},
		Data:       map[string][]byte{"password": []byte(iriPassword)},
	}
	controllerConfig := &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.ControllerConfigName},
		Spec: mcfgv1.ControllerConfigSpec{
			DNS: &configv1.DNS{Spec: configv1.DNSSpec{BaseDomain: baseDomain}},
		},
	}

	cases := []struct {
		name string
		// pullSecretContent is the raw ".dockerconfigjson" of the cluster pull secret.
		pullSecretContent string
		iriEnabled        bool
		// ccAbsent drops ControllerConfig from the lister, as on a cluster that
		// has not rendered its first one yet.
		ccAbsent bool
		// expectNil asserts getImageRegistryPullSecrets returns a nil secret,
		// used for the empty-"auths" ("don't roll config") path.
		expectNil bool
	}{
		{name: "IRI absent - pull secret unchanged", pullSecretContent: populatedPullSecretContent, iriEnabled: false},
		{name: "IRI present - credentials merged", pullSecretContent: populatedPullSecretContent, iriEnabled: true},
		// The base domain comes from the cluster DNS object, so a missing
		// ControllerConfig must not fail the whole pull-secret assembly.
		{name: "ControllerConfig absent - credentials still merged", pullSecretContent: populatedPullSecretContent, iriEnabled: true, ccAbsent: true},
		// With no image-pull secrets on the SA and an empty cluster pull secret,
		// the assembled "auths" map is empty. Even with IRI enabled the function
		// must return nil rather than emitting a secret carrying only IRI creds.
		{name: "empty auths with IRI enabled - returns nil", pullSecretContent: "{}", iriEnabled: true, expectNil: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mcoSecretObjs := []interface{}{}
			iriObjs := []interface{}{}
			if tc.iriEnabled {
				mcoSecretObjs = append(mcoSecretObjs, iriAuthSecret)
				iriObjs = append(iriObjs, iriInstance)
			}
			ccObjs := []interface{}{controllerConfig}
			if tc.ccAbsent {
				ccObjs = nil
			}

			clusterPullSecret := helpers.NewDockerCfgJSONSecret(ctrlcommon.GlobalPullSecretName, ctrlcommon.OpenshiftConfigNamespace, tc.pullSecretContent)

			optr := &Operator{
				namespace:             ctrlcommon.MCONamespace,
				clusterOperatorLister: configlistersv1.NewClusterOperatorLister(newNamespacedIndexer(t, imageRegistryCO)),
				dnsLister:             configlistersv1.NewDNSLister(newNamespacedIndexer(t, clusterDNS)),
				mcoSALister:           corev1listers.NewServiceAccountLister(newNamespacedIndexer(t, machineOSPullerSA)),
				mcoSecretLister:       corev1listers.NewSecretLister(newNamespacedIndexer(t, mcoSecretObjs...)),
				ocSecretLister:        corev1listers.NewSecretLister(newNamespacedIndexer(t, clusterPullSecret)),
				ccLister:              mcplister.NewControllerConfigLister(newNamespacedIndexer(t, ccObjs...)),
				iriLister:             mcplister.NewInternalReleaseImageLister(newNamespacedIndexer(t, iriObjs...)),
			}

			raw, err := optr.getImageRegistryPullSecrets()
			assert.NoError(t, err)

			if tc.expectNil {
				// Empty "auths": nothing to roll, so no secret is emitted (and
				// IRI creds are not emitted on their own).
				assert.Nil(t, raw)
				return
			}
			assert.NotEmpty(t, raw)

			var parsed struct {
				Auths map[string]struct {
					Auth string `json:"auth"`
				} `json:"auths"`
			}
			assert.NoError(t, json.Unmarshal(raw, &parsed))

			// The original registry entry is always present.
			assert.Contains(t, parsed.Auths, "registry.example.com")

			if tc.iriEnabled {
				assert.Contains(t, parsed.Auths, iriAPIIntHost, "expected api-int IRI auth entry to be merged")
				assert.Contains(t, parsed.Auths, iriLocalHost, "expected localhost IRI auth entry to be merged")
				assert.Equal(t, expectedIRIAuth, parsed.Auths[iriAPIIntHost].Auth)
				assert.Equal(t, expectedIRIAuth, parsed.Auths[iriLocalHost].Auth)
			} else {
				assert.NotContains(t, parsed.Auths, iriAPIIntHost, "IRI auth entry must not be present when IRI is absent")
				assert.NotContains(t, parsed.Auths, iriLocalHost, "IRI auth entry must not be present when IRI is absent")
			}
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
		enableCAPIFeatureGate           bool
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
		// CAPI test cases - feature gate enabled
		{
			name:                  "AWS platform, CAPI gate enabled, no admin opinion, CAPI MS and MD auto opt-in to All",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCAPIFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "GCP platform, CAPI gate enabled, no admin opinion, CAPI MS and MD auto opt-in to All",
			infra:                 buildInfra(withPlatformType(configv1.GCPPlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCAPIFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "Azure platform, CAPI gate enabled, no admin opinion, CAPI MS and MD auto opt-in to All",
			infra:                 buildInfra(withPlatformType(configv1.AzurePlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCAPIFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			name:                  "vSphere platform, CAPI gate enabled, no admin opinion, CAPI MS and MD auto opt-in to All",
			infra:                 buildInfra(withPlatformType(configv1.VSpherePlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCAPIFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			// MAPI MachineSets have no spec entry so they auto opt-in; CAPI entries come from spec.
			name:                  "AWS platform, CAPI gate enabled, admin opinion for both CAPI MS and MD, MAPI still auto opts in",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithCAPIMachineSetsAndDeploymentsEnabled(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCAPIFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusAutomaticWithOCPVersion("4.18.0"),
		},
		{
			// Admin disabled CAPI MS; MD has no spec opinion so it auto opts in; MAPI MS also auto opts in.
			name:                  "AWS platform, CAPI gate enabled, CAPI MS disabled in spec, CAPI MD and MAPI MS auto opt-in",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithCAPIMachineSetsDisabled(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCAPIFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		{
			name:                  "AWS platform, CAPI gate enabled, CAPI MD disabled in spec, CAPI MS and MAPI MS auto opt-in",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithCAPIMachineDeploymentsDisabled(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCAPIFeatureGate: true,
			expectedManagedBootImagesStatus: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.MachineAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
			expectedSkewEnforcementStatus: apihelpers.GetSkewEnforcementStatusManualWithOCPVersion("4.18.0"),
		},
		{
			name:                  "AWS platform, CAPI gate disabled, no CAPI managers in status",
			infra:                 buildInfra(withPlatformType(configv1.AWSPlatformType)),
			mcop:                  buildMachineConfigurationWithNoBootImageConfiguration(),
			clusterVersion:        buildClusterVersion("4.18.0"),
			annotationExpected:    true,
			enableCAPIFeatureGate: false,
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

			enabledFeatureGates := []configv1.FeatureGateName{}
			if tc.enableCPMSFeatureGate {
				enabledFeatureGates = append(enabledFeatureGates, features.FeatureGateManagedBootImagesCPMS)
			}
			if tc.enableCAPIFeatureGate {
				enabledFeatureGates = append(enabledFeatureGates, features.FeatureGateManagedBootImagesAWSCAPI)
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

func buildMachineConfigurationWithCAPIMachineSetsAndDeploymentsEnabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.All}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithCAPIMachineDeploymentsDisabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineDeployments, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
		},
	}
}

func buildMachineConfigurationWithCAPIMachineSetsDisabled() *opv1.MachineConfiguration {
	return &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: opv1.MachineConfigurationSpec{
			ManagedBootImages: opv1.ManagedBootImages{
				MachineManagers: []opv1.MachineManager{
					{Resource: opv1.MachineSets, APIGroup: opv1.ClusterAPI, Selection: opv1.MachineManagerSelector{Mode: opv1.None}},
				},
			},
		},
	}
}

// TestFRRConfTemplateTimersAndPort guards the review findings from
// openshift/installer#10718 (review 4919561631): FRR's
// "timers <keepalive> <hold>" takes bare seconds (the peer data carries
// whole-second decimal strings, converted from install-config durations by
// the producers), the line must only render when BOTH timers are set, and
// the peer port must be rendered (it was dead code before).
func TestFRRConfTemplateTimersAndPort(t *testing.T) {
	// Outer pass: the MCO asset render only unwraps the {{` ... `}} escapes.
	outer, err := renderAsset(&renderConfig{}, "manifests/on-prem/frr.conf.tmpl")
	require.NoError(t, err)

	// Inner pass: emulate runtimecfg rendering the on-disk tmpl.
	type peer struct {
		PeerAddress, PeerASN, Password, BFDEnabled, EBGPMultiHop, HoldTime, KeepaliveTime string
		Port                                                                              int32
	}
	render := func(p peer) string {
		data := struct {
			Hostname, LocalASN, RouterID string
			Peers                        []peer
			Communities                  []string
		}{
			Hostname: "master-0", LocalASN: "64512", RouterID: "192.168.111.20",
			Peers: []peer{p},
		}
		tmpl, err := template.New("frr").Funcs(template.FuncMap{
			"isIPv4": func(s string) bool { ip := net.ParseIP(s); return ip != nil && ip.To4() != nil },
			"isIPv6": func(s string) bool { ip := net.ParseIP(s); return ip != nil && ip.To4() == nil },
		}).Parse(string(outer))
		require.NoError(t, err)
		var rendered bytes.Buffer
		require.NoError(t, tmpl.Execute(&rendered, data))
		return rendered.String()
	}

	// Both timers set (bare seconds): rendered in FRR's keepalive-first order.
	out := render(peer{PeerAddress: "192.168.111.1", PeerASN: "64513", HoldTime: "90", KeepaliveTime: "30"})
	assert.Contains(t, out, " neighbor 192.168.111.1 timers 30 90\n")

	// Only one timer set: no timers line at all (FRR requires both).
	out = render(peer{PeerAddress: "192.168.111.1", PeerASN: "64513", HoldTime: "90"})
	assert.NotContains(t, out, " timers ")

	// Port set: rendered; omitted: absent.
	out = render(peer{PeerAddress: "192.168.111.1", PeerASN: "64513", Port: 1790})
	assert.Contains(t, out, " neighbor 192.168.111.1 port 1790\n")
	out = render(peer{PeerAddress: "192.168.111.1", PeerASN: "64513"})
	assert.NotContains(t, out, " port ")

	// Every neighbor carries disable-connected-check (frr-k8s parity): with
	// the RA-assigned /128 on br-ex, FRR's eBGP connected check fails after
	// a reboot and the IPv6 session never leaves Active.
	for _, addr := range []string{"192.168.111.1", "fd2e:6f44:5dd8:c956::1"} {
		out = render(peer{PeerAddress: addr, PeerASN: "64513"})
		assert.Contains(t, out, " neighbor "+addr+" disable-connected-check\n")
	}

	// The day-2 MachineConfig template must stay in sync with the bootstrap
	// asset for both constructs.
	day2, err := os.ReadFile(filepath.Join("..", "..", "templates", "master", "00-master", "on-prem", "files", "frr-k8s-conf.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(day2), "{{`{{- if and .HoldTime .KeepaliveTime}}`}}")
	assert.Contains(t, string(day2), "port {{`{{.Port}}`}}")
	assert.Contains(t, string(day2), "disable-connected-check")
}
