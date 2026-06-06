package operator

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/events"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/clock"
)

func testRenderConfig() *renderConfig {
	return &renderConfig{
		TargetNamespace: ctrlcommon.MCONamespace,
	}
}

func testOperatorForNetworkPolicies(t *testing.T, pools []*mcfgv1.MachineConfigPool) *Operator {
	t.Helper()

	kubeClient := fake.NewSimpleClientset()
	mcfgClient := fakeclientmachineconfigv1.NewSimpleClientset()
	mcfgInformerFactory := mcfginformers.NewSharedInformerFactory(mcfgClient, 0)
	mcpInformer := mcfgInformerFactory.Machineconfiguration().V1().MachineConfigPools()
	moscInformer := mcfgInformerFactory.Machineconfiguration().V1().MachineOSConfigs()

	for _, pool := range pools {
		require.NoError(t, mcpInformer.Informer().GetIndexer().Add(pool))
	}

	return &Operator{
		kubeClient:    kubeClient,
		mcpLister:     mcpInformer.Lister(),
		moscLister:    moscInformer.Lister(),
		libgoRecorder: events.NewInMemoryRecorder("test-operator", clock.RealClock{}),
	}
}

func layeredPool(name string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				ctrlcommon.LayeringEnabledPoolLabel: "",
			},
		},
	}
}

func listNetworkPolicies(t *testing.T, optr *Operator) []networkingv1.NetworkPolicy {
	t.Helper()
	npList, err := optr.kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	return npList.Items
}

func findPolicy(policies []networkingv1.NetworkPolicy, name string) *networkingv1.NetworkPolicy {
	for i := range policies {
		if policies[i].Name == name {
			return &policies[i]
		}
	}
	return nil
}

func TestSyncNetworkPolicies_StaticPoliciesCreated(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t, nil)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	names := make([]string, len(policies))
	for i, p := range policies {
		names[i] = p.Name
	}

	assert.Contains(t, names, "default-deny", "expected default-deny policy")
	assert.Contains(t, names, "allow-machine-config-operator", "expected allow-machine-config-operator policy")
	assert.Contains(t, names, "allow-machine-config-controller", "expected allow-machine-config-controller policy")
	assert.NotContains(t, names, "allow-machine-os-builder", "MOB policy should not be created without layered pools")
}

func TestSyncNetworkPolicies_DefaultDenySpec(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t, nil)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	denyPolicy := findPolicy(policies, "default-deny")
	require.NotNil(t, denyPolicy, "default-deny policy must exist")

	assert.Empty(t, denyPolicy.Spec.PodSelector.MatchLabels, "default-deny should select all pods with empty selector")
	assert.Contains(t, denyPolicy.Spec.PolicyTypes, networkingv1.PolicyTypeIngress, "default-deny must include Ingress policy type")
	assert.Contains(t, denyPolicy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress, "default-deny must include Egress policy type")
	assert.Empty(t, denyPolicy.Spec.Ingress, "default-deny should have no ingress rules")
	assert.Empty(t, denyPolicy.Spec.Egress, "default-deny should have no egress rules")
}

func TestSyncNetworkPolicies_AllowPolicySpecs(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t, nil)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)

	cases := []struct {
		name      string
		labelKey  string
		labelVal  string
		metricsPort int32
	}{
		{"allow-machine-config-operator", "k8s-app", "machine-config-operator", 9001},
		{"allow-machine-config-controller", "k8s-app", "machine-config-controller", 9001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := findPolicy(policies, tc.name)
			require.NotNil(t, policy, "policy %s must exist", tc.name)

			assert.Equal(t, tc.labelVal, policy.Spec.PodSelector.MatchLabels[tc.labelKey],
				"podSelector should match %s=%s", tc.labelKey, tc.labelVal)
			assert.Contains(t, policy.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
			assert.Contains(t, policy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)

			require.Len(t, policy.Spec.Egress, 1, "should have one egress rule (allow all)")
			assert.Empty(t, policy.Spec.Egress[0].Ports, "egress rule should allow all ports")
			assert.Empty(t, policy.Spec.Egress[0].To, "egress rule should allow all destinations")

			require.Len(t, policy.Spec.Ingress, 1, "should have one ingress rule")
			require.Len(t, policy.Spec.Ingress[0].Ports, 1, "ingress rule should have one port")
			assert.Equal(t, tc.metricsPort, policy.Spec.Ingress[0].Ports[0].Port.IntVal, "ingress port should be metrics port")
			assert.Empty(t, policy.Spec.Ingress[0].From, "ingress should not restrict source (kube-rbac-proxy handles auth)")
		})
	}
}

func TestSyncNetworkPolicies_MOBPolicyWithLayeredPools(t *testing.T) {
	pools := []*mcfgv1.MachineConfigPool{layeredPool("layered-worker")}
	optr := testOperatorForNetworkPolicies(t, pools)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	mobPolicy := findPolicy(policies, "allow-machine-os-builder")
	require.NotNil(t, mobPolicy, "MOB policy should be created when layered pools exist")

	assert.Equal(t, "machine-os-builder", mobPolicy.Spec.PodSelector.MatchLabels["k8s-app"])
	require.Len(t, mobPolicy.Spec.Egress, 1, "MOB should have one egress rule (allow all)")
	require.Len(t, mobPolicy.Spec.Ingress, 1, "MOB should have one ingress rule")
}

func TestSyncNetworkPolicies_MOBPolicyDeletedWithoutLayeredPools(t *testing.T) {
	pools := []*mcfgv1.MachineConfigPool{layeredPool("layered-worker")}
	optr := testOperatorForNetworkPolicies(t, pools)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	require.NotNil(t, findPolicy(policies, "allow-machine-os-builder"), "MOB policy should exist initially")

	optr.mcpLister = testOperatorForNetworkPolicies(t, nil).mcpLister

	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies = listNetworkPolicies(t, optr)
	assert.Nil(t, findPolicy(policies, "allow-machine-os-builder"), "MOB policy should be deleted when no layered pools exist")
	assert.NotNil(t, findPolicy(policies, "default-deny"), "static policies should still exist")
	assert.NotNil(t, findPolicy(policies, "allow-machine-config-operator"), "static policies should still exist")
	assert.NotNil(t, findPolicy(policies, "allow-machine-config-controller"), "static policies should still exist")
}

func TestSyncNetworkPolicies_DriftProtection(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t, nil)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policy, err := optr.kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Get(
		context.TODO(), "allow-machine-config-operator", metav1.GetOptions{})
	require.NoError(t, err)

	policy.Spec.Ingress = nil
	_, err = optr.kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Update(
		context.TODO(), policy, metav1.UpdateOptions{})
	require.NoError(t, err)

	modified, err := optr.kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Get(
		context.TODO(), "allow-machine-config-operator", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, modified.Spec.Ingress, "ingress should be empty after manual modification")

	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	restored, err := optr.kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Get(
		context.TODO(), "allow-machine-config-operator", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, restored.Spec.Ingress, 1, "ingress rules should be restored after resync")
}

func TestSyncNetworkPolicies_Idempotent(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t, nil)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)
	firstCount := len(listNetworkPolicies(t, optr))

	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)
	secondCount := len(listNetworkPolicies(t, optr))

	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)
	thirdCount := len(listNetworkPolicies(t, optr))

	assert.Equal(t, firstCount, secondCount, "policy count should be stable across syncs")
	assert.Equal(t, secondCount, thirdCount, "policy count should be stable across syncs")
	assert.Equal(t, 3, thirdCount, "should have exactly 3 static policies without layered pools")
}
