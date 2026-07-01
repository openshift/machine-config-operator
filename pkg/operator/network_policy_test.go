package operator

import (
	"context"
	"testing"

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

func testOperatorForNetworkPolicies(t *testing.T) *Operator {
	t.Helper()

	kubeClient := fake.NewClientset()

	return &Operator{
		kubeClient:    kubeClient,
		libgoRecorder: events.NewInMemoryRecorder("test-operator", clock.RealClock{}),
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
	optr := testOperatorForNetworkPolicies(t)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	names := make([]string, len(policies))
	for i, p := range policies {
		names[i] = p.Name
	}

	assert.Contains(t, names, "default-deny", "expected default-deny policy")
	assert.Contains(t, names, "allow-all-egress", "expected allow-all-egress policy")
	assert.Contains(t, names, "allow-machine-config-operator", "expected allow-machine-config-operator policy")
	assert.Contains(t, names, "allow-machine-config-controller", "expected allow-machine-config-controller policy")
	assert.Contains(t, names, "allow-machine-os-builder", "expected allow-machine-os-builder policy")
}

func TestSyncNetworkPolicies_DefaultDenySpec(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t)
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

func TestSyncNetworkPolicies_AllowAllEgressSpec(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	egressPolicy := findPolicy(policies, "allow-all-egress")
	require.NotNil(t, egressPolicy, "allow-all-egress policy must exist")

	assert.Empty(t, egressPolicy.Spec.PodSelector.MatchLabels, "allow-all-egress should select all pods with empty selector")
	assert.Contains(t, egressPolicy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
	assert.NotContains(t, egressPolicy.Spec.PolicyTypes, networkingv1.PolicyTypeIngress, "allow-all-egress should not set Ingress policy type")
	require.Len(t, egressPolicy.Spec.Egress, 1, "should have one egress rule (allow all)")
	assert.Empty(t, egressPolicy.Spec.Egress[0].Ports, "egress rule should allow all ports")
	assert.Empty(t, egressPolicy.Spec.Egress[0].To, "egress rule should allow all destinations")
}

func TestSyncNetworkPolicies_AllowPolicySpecs(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)

	cases := []struct {
		name        string
		labelKey    string
		labelVal    string
		metricsPort int32
	}{
		{"allow-machine-config-operator", "k8s-app", "machine-config-operator", 9001},
		{"allow-machine-config-controller", "k8s-app", "machine-config-controller", 9001},
		{"allow-machine-os-builder", "k8s-app", "machine-os-builder", 9001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := findPolicy(policies, tc.name)
			require.NotNil(t, policy, "policy %s must exist", tc.name)

			assert.Equal(t, tc.labelVal, policy.Spec.PodSelector.MatchLabels[tc.labelKey],
				"podSelector should match %s=%s", tc.labelKey, tc.labelVal)
			assert.Contains(t, policy.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
			assert.NotContains(t, policy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress, "ingress-only allow policies should not set Egress policy type")

			assert.Empty(t, policy.Spec.Egress, "ingress-only allow policies should have no egress rules")

			require.Len(t, policy.Spec.Ingress, 1, "should have one ingress rule")
			require.Len(t, policy.Spec.Ingress[0].Ports, 1, "ingress rule should have one port")
			assert.Equal(t, tc.metricsPort, policy.Spec.Ingress[0].Ports[0].Port.IntVal, "ingress port should be metrics port")
			assert.Empty(t, policy.Spec.Ingress[0].From, "ingress should not restrict source (kube-rbac-proxy handles auth)")
		})
	}
}

func TestSyncNetworkPolicies_MOBPolicyAlwaysCreated(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	mobPolicy := findPolicy(policies, "allow-machine-os-builder")
	require.NotNil(t, mobPolicy, "MOB policy should always be created even without layered pools")

	assert.Equal(t, "machine-os-builder", mobPolicy.Spec.PodSelector.MatchLabels["k8s-app"])
	assert.Empty(t, mobPolicy.Spec.Egress, "MOB ingress-only policy should have no egress rules")
	require.Len(t, mobPolicy.Spec.Ingress, 1, "MOB should have one ingress rule")
}

func TestSyncNetworkPolicies_Idempotent(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policiesBefore := listNetworkPolicies(t, optr)

	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policiesAfter := listNetworkPolicies(t, optr)
	assert.Len(t, policiesAfter, len(policiesBefore), "re-sync should produce the same number of policies")
}

