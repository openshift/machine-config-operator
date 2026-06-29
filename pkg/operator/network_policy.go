package operator

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	networkingv1ac "k8s.io/client-go/applyconfigurations/networking/v1"
	"k8s.io/utils/ptr"
)

const networkPolicyFieldManager = "machine-config-operator"

// defaultDenyPatchBody returns a raw JSON SSA patch for the default-deny policy.
// We use a raw patch instead of the typed Apply because the apply-config types
// tag Ingress/Egress with omitempty, and Go's json.Marshal drops empty slices
// with omitempty. Without explicit "ingress": [] and "egress": [] in the payload,
// SSA never claims ownership of those fields and cannot revert externally-added rules.
func defaultDenyPatchBody() []byte {
	return []byte(`{
		"apiVersion": "networking.k8s.io/v1",
		"kind": "NetworkPolicy",
		"metadata": {"name": "default-deny"},
		"spec": {
			"podSelector": {},
			"ingress": [],
			"egress": [],
			"policyTypes": ["Ingress", "Egress"]
		}
	}`)
}

func allowMCONetworkPolicy(namespace string) *networkingv1ac.NetworkPolicyApplyConfiguration {
	return networkingv1ac.NetworkPolicy("allow-machine-config-operator", namespace).
		WithSpec(networkingv1ac.NetworkPolicySpec().
			WithPodSelector(metav1ac.LabelSelector().
				WithMatchLabels(map[string]string{"k8s-app": "machine-config-operator"})).
			WithIngress(networkingv1ac.NetworkPolicyIngressRule().
				WithPorts(networkingv1ac.NetworkPolicyPort().
					WithProtocol(corev1.ProtocolTCP).
					WithPort(intstr.FromInt32(9001)))).
			WithEgress(networkingv1ac.NetworkPolicyEgressRule()).
			WithPolicyTypes(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))
}

func allowMCCNetworkPolicy(namespace string) *networkingv1ac.NetworkPolicyApplyConfiguration {
	return networkingv1ac.NetworkPolicy("allow-machine-config-controller", namespace).
		WithSpec(networkingv1ac.NetworkPolicySpec().
			WithPodSelector(metav1ac.LabelSelector().
				WithMatchLabels(map[string]string{"k8s-app": "machine-config-controller"})).
			WithIngress(networkingv1ac.NetworkPolicyIngressRule().
				WithPorts(networkingv1ac.NetworkPolicyPort().
					WithProtocol(corev1.ProtocolTCP).
					WithPort(intstr.FromInt32(9001)))).
			WithEgress(networkingv1ac.NetworkPolicyEgressRule()).
			WithPolicyTypes(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))
}

func allowMOBNetworkPolicy(namespace string) *networkingv1ac.NetworkPolicyApplyConfiguration {
	return networkingv1ac.NetworkPolicy("allow-machine-os-builder", namespace).
		WithSpec(networkingv1ac.NetworkPolicySpec().
			WithPodSelector(metav1ac.LabelSelector().
				WithMatchLabels(map[string]string{"k8s-app": "machine-os-builder"})).
			WithIngress(networkingv1ac.NetworkPolicyIngressRule().
				WithPorts(networkingv1ac.NetworkPolicyPort().
					WithProtocol(corev1.ProtocolTCP).
					WithPort(intstr.FromInt32(9001)))).
			WithEgress(networkingv1ac.NetworkPolicyEgressRule()).
			WithPolicyTypes(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress))
}

func (optr *Operator) syncNetworkPolicies(_ *renderConfig, _ *configv1.ClusterOperator) error {
	ctx := context.TODO()
	ns := ctrlcommon.MCONamespace

	// Apply default-deny via raw patch so empty ingress/egress slices are
	// preserved in the payload and SSA claims ownership of those fields.
	patchOpts := metav1.PatchOptions{FieldManager: networkPolicyFieldManager, Force: ptr.To(true)}
	if _, err := optr.kubeClient.NetworkingV1().NetworkPolicies(ns).Patch(
		ctx, "default-deny", types.ApplyPatchType, defaultDenyPatchBody(), patchOpts,
	); err != nil {
		return err
	}

	applyOpts := metav1.ApplyOptions{FieldManager: networkPolicyFieldManager, Force: true}
	policies := []*networkingv1ac.NetworkPolicyApplyConfiguration{
		allowMCONetworkPolicy(ns),
		allowMCCNetworkPolicy(ns),
		allowMOBNetworkPolicy(ns),
	}

	for _, policy := range policies {
		if _, err := optr.kubeClient.NetworkingV1().NetworkPolicies(ns).Apply(ctx, policy, applyOpts); err != nil {
			return err
		}
	}

	return nil
}
