package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	fakeconfigclientset "github.com/openshift/client-go/config/clientset/versioned/fake"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcoplistersv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
)

// TestSyncBGPVIPPeersJSONRejectsUnsafePassword: a password Secret that is
// not valid, printable UTF-8 fails the render (Rendered=False) before any
// transport is produced, and the password appears in neither the error nor
// the status message nor the spec.
func TestSyncBGPVIPPeersJSONRejectsUnsafePassword(t *testing.T) {
	const marker = "SYNTHETIC LEAK"
	infra := buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP"))
	for name, password := range map[string][]byte{
		"NEL (U+0085)":  []byte(marker + "-\u0085-MARKER"),
		"DEL (U+007F)":  []byte(marker + "-\u007f-MARKER"),
		"invalid UTF-8": []byte(marker + "-\xff-MARKER"),
	} {
		t.Run(name, func(t *testing.T) {
			cr := buildBGPVIPConfigCR(func(cr *mcfgv1alpha1.BGPVIPConfig) {
				cr.Spec.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{
					PeerAddress:    "192.168.111.1",
					PeerASN:        64513,
					PasswordSecret: mcfgv1alpha1.BGPVIPPasswordSecret{Name: "bgp-peer-pw"},
				}}
			})
			var patched []byte
			dynamicClient := bgpRenderedStatusRecorder(t, cr, &patched)
			optr := &Operator{
				bgpVIPConfigLister: newBGPVIPConfigLister(t, cr),
				bgpVIPConfigClient: dynamicClient.Resource(bgpVIPConfigGVR),
				kubeClient:         fake.NewSimpleClientset(bgpPasswordSecret("bgp-peer-pw", password)),
			}
			spec := &mcfgv1.ControllerConfigSpec{}
			err := optr.syncBGPVIPPeersJSON(spec, infra)
			require.Error(t, err, "an unsafe password must fail the render")
			assert.NotContains(t, err.Error(), marker)
			assert.Empty(t, spec.BGPVIPPeersJSON, "nothing is rendered for the ControllerConfig")
			specJSON, jsonErr := json.Marshal(spec)
			require.NoError(t, jsonErr)
			assert.NotContains(t, string(specJSON), marker)
			require.NotNil(t, patched, "Rendered=False is reported for the failed render")
			cond := renderedCondition(t, patched)
			assert.Equal(t, "False", cond["status"])
			assert.NotContains(t, cond["message"], marker)
		})
	}
}

// newSyncAllTestOperator returns an Operator wired enough for syncAll to run
// a custom list of sync functions (same setup as TestInClusterBringUpStayOnErr).
func newSyncAllTestOperator(t *testing.T) *Operator {
	t.Helper()
	optr := &Operator{
		eventRecorder: &record.FakeRecorder{},
		fgHandler:     ctrlcommon.NewFeatureGatesHardcodedHandler([]configv1.FeatureGateName{}, []configv1.FeatureGateName{}),
	}
	optr.vStore = newVersionStore()
	optr.vStore.Set("operator", "test-version")
	optr.mcpLister = &mockMCPLister{pools: []*mcfgv1.MachineConfigPool{
		helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
		helpers.NewMachineConfigPool("workers", nil, helpers.WorkerSelector, "v0"),
	}}
	newIndexer := func() cache.Indexer {
		return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	}
	optr.mcopLister = mcoplistersv1.NewMachineConfigurationLister(newIndexer())
	nodeIndexer := newIndexer()
	optr.nodeLister = corev1listers.NewNodeLister(nodeIndexer)
	require.NoError(t, nodeIndexer.Add(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "first-node", Labels: map[string]string{"node-role/worker": ""}},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.21"}},
	}))
	co := &configv1.ClusterOperator{}
	kasOperator := &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver"},
		Status:     configv1.ClusterOperatorStatus{Versions: []configv1.OperandVersion{{Name: "kube-apiserver", Version: "1.21"}}},
	}
	configNodeIndexer := newIndexer()
	optr.nodeClusterLister = configlistersv1.NewNodeLister(configNodeIndexer)
	require.NoError(t, configNodeIndexer.Add(&configv1.Node{ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.ClusterNodeInstanceName}}))
	optr.configClient = fakeconfigclientset.NewSimpleClientset(co, kasOperator)
	operatorIndexer := newIndexer()
	optr.clusterOperatorLister = configlistersv1.NewClusterOperatorLister(operatorIndexer)
	require.NoError(t, operatorIndexer.Add(co))
	require.NoError(t, operatorIndexer.Add(kasOperator))
	optr.mcoCmLister = corev1listers.NewConfigMapLister(newIndexer())
	return optr
}

// TestBGPVIPBadSecretEditKeepsLastGoodControllerConfig: a BGPVIPConfig edit
// that references a Secret which does not exist fails the RenderConfig step,
// so syncAll never reaches the step that applies the ControllerConfig. The
// live ControllerConfig keeps its last good references (which the template
// controller keeps rendering), and the failure is reported on the CR.
func TestBGPVIPBadSecretEditKeepsLastGoodControllerConfig(t *testing.T) {
	infra := buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP"))
	optr := newSyncAllTestOperator(t)

	cr := buildBGPVIPConfigCR(func(cr *mcfgv1alpha1.BGPVIPConfig) {
		cr.Spec.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{
			PeerAddress:    "192.168.111.1",
			PeerASN:        64513,
			PasswordSecret: mcfgv1alpha1.BGPVIPPasswordSecret{Name: "bgp-peer-pw-new"},
		}}
	})
	var patched []byte
	dynamicClient := bgpRenderedStatusRecorder(t, cr, &patched)
	optr.bgpVIPConfigLister = newBGPVIPConfigLister(t, cr)
	optr.bgpVIPConfigClient = dynamicClient.Resource(bgpVIPConfigGVR)
	// Only the previously referenced Secret exists.
	optr.kubeClient = fake.NewSimpleClientset(bgpPasswordSecret("bgp-peer-pw", []byte("s3cret")))
	live := &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.ControllerConfigName},
		Spec:       mcfgv1.ControllerConfigSpec{BGPVIPPeersJSON: simplePeersTransport},
	}
	mcfgClient := fakeclientmachineconfigv1.NewSimpleClientset(live)
	optr.client = mcfgClient

	controllerConfigStepRan := false
	err := optr.syncAll([]syncFunc{
		{name: "RenderConfig", fn: func(_ *renderConfig, _ *configv1.ClusterOperator) error {
			return optr.syncBGPVIPPeersJSON(&mcfgv1.ControllerConfigSpec{}, infra)
		}},
		{name: "MachineConfigController", fn: func(_ *renderConfig, _ *configv1.ClusterOperator) error {
			controllerConfigStepRan = true
			return nil
		}},
	})
	require.Error(t, err)
	assert.False(t, controllerConfigStepRan, "the ControllerConfig must not be applied after a failed render")

	for _, a := range mcfgClient.Actions() {
		assert.False(t, a.GetResource().Resource == "controllerconfigs" && (a.GetVerb() == "update" || a.GetVerb() == "create"),
			"unexpected ControllerConfig write: %s", a.GetVerb())
	}
	got, getErr := mcfgClient.MachineconfigurationV1().ControllerConfigs().Get(context.TODO(), ctrlcommon.ControllerConfigName, metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, simplePeersTransport, got.Spec.BGPVIPPeersJSON, "the last good references stay in place")

	require.NotNil(t, patched)
	cond := renderedCondition(t, patched)
	assert.Equal(t, string(metav1.ConditionFalse), cond["status"])
	assert.Contains(t, cond["message"], "openshift-config/bgp-peer-pw-new")
}

// captureKlog redirects klog to a buffer at verbosity 4 for the duration of
// the test, so the operator's logging can be inspected.
func captureKlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var verbosity klog.Level
	require.NoError(t, verbosity.Set("4"))
	klog.LogToStderr(false)
	klog.SetOutput(&buf)
	t.Cleanup(func() {
		klog.Flush()
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
		require.NoError(t, verbosity.Set("0"))
	})
	return &buf
}

// TestBGPVIPUnsafePasswordNeverSurfacesInOperatorStatus drives an unsafe
// password Secret through the operator's real syncAll: the render fails, and
// the password appears in neither the returned error, the ClusterOperator
// status (whole object), operator events, the BGPVIPConfig status nor the
// logs.
func TestBGPVIPUnsafePasswordNeverSurfacesInOperatorStatus(t *testing.T) {
	const marker = "SYNTHETIC LEAK"
	infra := buildInfra(withPlatformType(configv1.BareMetalPlatformType), withBareMetalVIPManagement("BGP"))
	for name, password := range map[string][]byte{
		"NEL (U+0085)":  []byte(marker + "-\u0085-MARKER"),
		"invalid UTF-8": []byte(marker + "-\xff-MARKER"),
	} {
		t.Run(name, func(t *testing.T) {
			optr := newSyncAllTestOperator(t)
			recorder := record.NewFakeRecorder(100)
			optr.eventRecorder = recorder
			cr := buildBGPVIPConfigCR(func(cr *mcfgv1alpha1.BGPVIPConfig) {
				cr.Spec.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{
					PeerAddress:    "192.168.111.1",
					PeerASN:        64513,
					PasswordSecret: mcfgv1alpha1.BGPVIPPasswordSecret{Name: "bgp-peer-pw"},
				}}
			})
			var patched []byte
			dynamicClient := bgpRenderedStatusRecorder(t, cr, &patched)
			optr.bgpVIPConfigLister = newBGPVIPConfigLister(t, cr)
			optr.bgpVIPConfigClient = dynamicClient.Resource(bgpVIPConfigGVR)
			optr.kubeClient = fake.NewSimpleClientset(bgpPasswordSecret("bgp-peer-pw", password))
			optr.client = fakeclientmachineconfigv1.NewSimpleClientset()
			logs := captureKlog(t)

			err := optr.syncAll([]syncFunc{
				{name: "RenderConfig", fn: func(_ *renderConfig, _ *configv1.ClusterOperator) error {
					return optr.syncBGPVIPPeersJSON(&mcfgv1.ControllerConfigSpec{}, infra)
				}},
			})
			klog.Flush()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), marker)

			cos, listErr := optr.configClient.ConfigV1().ClusterOperators().List(context.TODO(), metav1.ListOptions{})
			require.NoError(t, listErr)
			degraded := false
			for i := range cos.Items {
				data, jsonErr := json.Marshal(cos.Items[i])
				require.NoError(t, jsonErr)
				assert.NotContains(t, string(data), marker)
				for _, cond := range cos.Items[i].Status.Conditions {
					if cond.Type == configv1.OperatorDegraded && cond.Status == configv1.ConditionTrue {
						degraded = true
					}
				}
			}
			assert.True(t, degraded, "the failed render is reported as Degraded")

			close(recorder.Events)
			for event := range recorder.Events {
				assert.NotContains(t, event, marker)
			}
			require.NotNil(t, patched)
			assert.NotContains(t, string(patched), marker)
			assert.NotContains(t, logs.String(), marker)
		})
	}
}
