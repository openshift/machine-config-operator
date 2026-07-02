package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestSyncNetworkPolicies_DriftProtection(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t)
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


func TestSyncNetworkPolicies_DeletionRecreation(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)
	require.Len(t, listNetworkPolicies(t, optr), 4, "should start with 4 policies")

	err = optr.kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Delete(
		context.TODO(), "allow-machine-config-operator", metav1.DeleteOptions{})
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	require.Len(t, policies, 3, "should have 3 policies after deletion")
	assert.Nil(t, findPolicy(policies, "allow-machine-config-operator"), "deleted policy should be gone")

	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies = listNetworkPolicies(t, optr)
	require.Len(t, policies, 4, "deleted policy should be re-created after resync")

	recreated := findPolicy(policies, "allow-machine-config-operator")
	require.NotNil(t, recreated, "allow-machine-config-operator must be re-created")
	assert.Equal(t, "machine-config-operator", recreated.Spec.PodSelector.MatchLabels["k8s-app"])
	require.Len(t, recreated.Spec.Ingress, 1, "re-created policy should have ingress rule")
	require.Len(t, recreated.Spec.Ingress[0].Ports, 1)
	assert.Equal(t, int32(9001), recreated.Spec.Ingress[0].Ports[0].Port.IntVal, "re-created policy should have metrics port")
	require.Len(t, recreated.Spec.Egress, 1, "re-created policy should have egress rule")
}

func TestSyncNetworkPolicies_AllDeletedRecreation(t *testing.T) {
	optr := testOperatorForNetworkPolicies(t)
	config := testRenderConfig()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)
	require.Len(t, listNetworkPolicies(t, optr), 4)

	allPolicies := []string{"default-deny", "allow-machine-config-operator", "allow-machine-config-controller", "allow-machine-os-builder"}
	for _, name := range allPolicies {
		err = optr.kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Delete(
			context.TODO(), name, metav1.DeleteOptions{})
		require.NoError(t, err)
	}
	require.Empty(t, listNetworkPolicies(t, optr), "all policies should be deleted")

	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies := listNetworkPolicies(t, optr)
	require.Len(t, policies, 4, "all policies should be re-created after resync")

	names := make([]string, len(policies))
	for i, p := range policies {
		names[i] = p.Name
	}
	for _, expected := range allPolicies {
		assert.Contains(t, names, expected, "policy %s should be re-created", expected)
	}
}

