package e2e_2of2_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
)

const (
	networkPolicySyncPollInterval = 5 * time.Second
	networkPolicySyncPollTimeout  = 2 * time.Minute
)

var staticPolicyNames = []string{
	"default-deny",
	"allow-all-egress",
	"allow-machine-config-operator",
	"allow-machine-config-controller",
	"allow-machine-os-builder",
}

func skipIfNoNetworkPolicies(t *testing.T) {
	t.Helper()
	cs := framework.NewClientSet("")
	policies, err := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).List(
		context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, policies.Items, "expected at least one NetworkPolicy in MCO namespace")
}

func TestNetworkPolicies_DefaultPoliciesExist(t *testing.T) {
	skipIfNoNetworkPolicies(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()
	ns := ctrlcommon.MCONamespace

	policies, err := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	policyMap := make(map[string]*networkingv1.NetworkPolicy)
	for i := range policies.Items {
		policyMap[policies.Items[i].Name] = &policies.Items[i]
	}

	for _, name := range staticPolicyNames {
		assert.Contains(t, policyMap, name, "expected policy %s to exist", name)
	}

	deny := policyMap["default-deny"]
	if deny != nil {
		assert.Empty(t, deny.Spec.PodSelector.MatchLabels, "default-deny should select all pods")
		assert.Contains(t, deny.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
		assert.Contains(t, deny.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		assert.Empty(t, deny.Spec.Ingress, "default-deny should have no ingress rules")
		assert.Empty(t, deny.Spec.Egress, "default-deny should have no egress rules")
	}

	// Verify allow-all-egress policy covers all pods
	egressPolicy := policyMap["allow-all-egress"]
	if egressPolicy != nil {
		assert.Empty(t, egressPolicy.Spec.PodSelector.MatchLabels, "allow-all-egress should select all pods")
		assert.Contains(t, egressPolicy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		require.NotEmpty(t, egressPolicy.Spec.Egress, "allow-all-egress should have egress rules")
	}

	// Verify per-component ingress-only allow policies
	for _, tc := range []struct {
		name     string
		labelVal string
	}{
		{"allow-machine-config-operator", "machine-config-operator"},
		{"allow-machine-config-controller", "machine-config-controller"},
		{"allow-machine-os-builder", "machine-os-builder"},
	} {
		policy := policyMap[tc.name]
		if policy == nil {
			continue
		}

		assert.Equal(t, tc.labelVal, policy.Spec.PodSelector.MatchLabels["k8s-app"],
			"%s: podSelector should match k8s-app=%s", tc.name, tc.labelVal)
		assert.Contains(t, policy.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)

		require.NotEmpty(t, policy.Spec.Ingress, "%s: should have ingress rules", tc.name)
		require.NotEmpty(t, policy.Spec.Ingress[0].Ports, "%s: ingress should have ports", tc.name)
		assert.Equal(t, int32(9001), policy.Spec.Ingress[0].Ports[0].Port.IntVal,
			"%s: ingress port should be 9001", tc.name)
		assert.Equal(t, corev1.ProtocolTCP, *policy.Spec.Ingress[0].Ports[0].Protocol,
			"%s: ingress protocol should be TCP", tc.name)
	}
}

func TestNetworkPolicies_ModificationReverted(t *testing.T) {
	skipIfNoNetworkPolicies(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()
	ns := ctrlcommon.MCONamespace
	npClient := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns)

	policy, err := npClient.Get(ctx, "allow-machine-config-operator", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, policy.Spec.Ingress, "policy should have ingress rules before modification")

	policy.Spec.Ingress = nil
	_, err = npClient.Update(ctx, policy, metav1.UpdateOptions{})
	require.NoError(t, err)

	modified, err := npClient.Get(ctx, "allow-machine-config-operator", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, modified.Spec.Ingress, "ingress should be empty after modification")

	err = wait.PollUntilContextTimeout(ctx, networkPolicySyncPollInterval, networkPolicySyncPollTimeout, true,
		func(ctx context.Context) (bool, error) {
			restored, err := npClient.Get(ctx, "allow-machine-config-operator", metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			return len(restored.Spec.Ingress) > 0, nil
		})
	require.NoError(t, err, "operator should revert modified ingress rules")

	restored, err := npClient.Get(ctx, "allow-machine-config-operator", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, restored.Spec.Ingress, 1)
	require.Len(t, restored.Spec.Ingress[0].Ports, 1)
	assert.Equal(t, int32(9001), restored.Spec.Ingress[0].Ports[0].Port.IntVal,
		"restored policy should have metrics port 9001")
}

func TestNetworkPolicies_DeletionBlocked(t *testing.T) {
	skipIfNoNetworkPolicies(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()
	ns := ctrlcommon.MCONamespace
	npClient := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns)

	for _, name := range staticPolicyNames {
		err := npClient.Delete(ctx, name, metav1.DeleteOptions{})
		require.Error(t, err, "deleting managed policy %s should be blocked", name)
		require.True(t, k8serrors.IsInvalid(err),
			"error should be Invalid (422) from ValidatingAdmissionPolicy, got: %v", err)
		require.Contains(t, err.Error(), "MCO-managed NetworkPolicy",
			"error message should indicate the policy is MCO-managed")
	}

	// Verify all policies still exist after blocked deletion attempts
	policies, err := npClient.List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	policyNames := make(map[string]bool)
	for _, p := range policies.Items {
		policyNames[p.Name] = true
	}
	for _, expected := range staticPolicyNames {
		assert.True(t, policyNames[expected], "policy %s should still exist after blocked deletion", expected)
	}
}

func TestNetworkPolicies_MCOProcessesContinueWorking(t *testing.T) {
	skipIfNoNetworkPolicies(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()
	ns := ctrlcommon.MCONamespace

	co, err := cs.ClusterOperators().Get(ctx, "machine-config", metav1.GetOptions{})
	require.NoError(t, err, "should be able to get machine-config ClusterOperator")

	foundAvailable, foundDegraded := false, false
	for _, cond := range co.Status.Conditions {
		switch cond.Type {
		case configv1.OperatorAvailable:
			foundAvailable = true
			assert.Equal(t, configv1.ConditionTrue, cond.Status,
				"machine-config operator should be Available")
		case configv1.OperatorDegraded:
			foundDegraded = true
			assert.Equal(t, configv1.ConditionFalse, cond.Status,
				"machine-config operator should not be Degraded")
		}
	}
	assert.True(t, foundAvailable, "OperatorAvailable condition must be present on machine-config ClusterOperator")
	assert.True(t, foundDegraded, "OperatorDegraded condition must be present on machine-config ClusterOperator")

	pods, err := cs.Pods(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	componentRunning := map[string]bool{
		"machine-config-operator":   false,
		"machine-config-controller": false,
	}
	for _, pod := range pods.Items {
		app := pod.Labels["k8s-app"]
		if _, tracked := componentRunning[app]; tracked && pod.Status.Phase == corev1.PodRunning {
			componentRunning[app] = true
		}
	}
	for component, running := range componentRunning {
		assert.True(t, running, "%s pod should be Running", component)
	}

	policies, err := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	policyNames := make([]string, len(policies.Items))
	for i, p := range policies.Items {
		policyNames[i] = p.Name
	}
	for _, expected := range staticPolicyNames {
		assert.Contains(t, policyNames, expected,
			"policy %s should exist while MCO processes are running", expected)
	}
}

func TestNetworkPolicies_DefaultDenyModificationReverted(t *testing.T) {
	skipIfNoNetworkPolicies(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()
	ns := ctrlcommon.MCONamespace
	npClient := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns)

	deny, err := npClient.Get(ctx, "default-deny", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, deny.Spec.Ingress, "default-deny should have no ingress rules before modification")

	deny.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{
		{
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Protocol: npProtocolPtr(corev1.ProtocolTCP),
					Port:     npPortPtr(8080),
				},
			},
		},
	}
	_, err = npClient.Update(ctx, deny, metav1.UpdateOptions{})
	require.NoError(t, err)

	modified, err := npClient.Get(ctx, "default-deny", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, modified.Spec.Ingress, "default-deny should have injected ingress rule after modification")

	err = wait.PollUntilContextTimeout(ctx, networkPolicySyncPollInterval, networkPolicySyncPollTimeout, true,
		func(ctx context.Context) (bool, error) {
			restored, err := npClient.Get(ctx, "default-deny", metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			return len(restored.Spec.Ingress) == 0, nil
		})
	require.NoError(t, err, "operator should revert injected ingress rules on default-deny")

	restored, err := npClient.Get(ctx, "default-deny", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, restored.Spec.Ingress, "default-deny should have no ingress rules after revert")
	assert.Empty(t, restored.Spec.Egress, "default-deny should have no egress rules after revert")
}

func TestNetworkPolicies_AdminNetworkPolicyOverride(t *testing.T) {
	skipIfNoNetworkPolicies(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()
	ns := ctrlcommon.MCONamespace

	deny, err := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns).Get(ctx, "default-deny", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, deny.Spec.Ingress, "default-deny should block all ingress")

	overridePolicy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-additional-allow",
			Namespace: ns,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"k8s-app": "machine-config-operator"},
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: npProtocolPtr(corev1.ProtocolTCP),
							Port:     npPortPtr(8080),
						},
					},
				},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}

	_, err = cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns).Create(ctx, overridePolicy, metav1.CreateOptions{})
	require.NoError(t, err)
	defer func() {
		_ = cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns).Delete(ctx, "test-additional-allow", metav1.DeleteOptions{})
	}()

	created, err := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns).Get(ctx, "test-additional-allow", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "test-additional-allow", created.Name)

	err = wait.PollUntilContextTimeout(ctx, networkPolicySyncPollInterval, networkPolicySyncPollTimeout, true,
		func(ctx context.Context) (bool, error) {
			policies, err := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err
			}
			names := make(map[string]bool)
			for _, p := range policies.Items {
				names[p.Name] = true
			}
			for _, expected := range staticPolicyNames {
				if !names[expected] {
					return false, nil
				}
			}
			return names["test-additional-allow"], nil
		})
	require.NoError(t, err, "operator-managed policies should coexist with user-created policies")

	final, err := cs.GetKubeclient().NetworkingV1().NetworkPolicies(ns).Get(ctx, "test-additional-allow", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, final.Spec.Ingress, 1)
	require.Len(t, final.Spec.Ingress[0].Ports, 1)
	assert.Equal(t, int32(8080), final.Spec.Ingress[0].Ports[0].Port.IntVal,
		"user-created policy should retain its original port")
}

func npProtocolPtr(p corev1.Protocol) *corev1.Protocol {
	return &p
}

func npPortPtr(port int32) *intstr.IntOrString {
	p := intstr.FromInt32(port)
	return &p
}
