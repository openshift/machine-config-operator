package operator

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	networkingv1ac "k8s.io/client-go/applyconfigurations/networking/v1"
)

const networkPolicyFieldManager = "machine-config-operator"

func defaultDenyNetworkPolicy(namespace string) *networkingv1ac.NetworkPolicyApplyConfiguration {
	spec := networkingv1ac.NetworkPolicySpec().
		WithPodSelector(metav1ac.LabelSelector()).
		WithPolicyTypes(networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress)
	// Explicitly set empty slices so SSA claims ownership of ingress/egress
	// fields and reverts any externally-added rules. WithIngress()/WithEgress()
	// with no args is a no-op, and omitempty drops nil slices from the payload.
	spec.Ingress = []networkingv1ac.NetworkPolicyIngressRuleApplyConfiguration{}
	spec.Egress = []networkingv1ac.NetworkPolicyEgressRuleApplyConfiguration{}
	return networkingv1ac.NetworkPolicy("default-deny", namespace).WithSpec(spec)
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
	applyOpts := metav1.ApplyOptions{FieldManager: networkPolicyFieldManager, Force: true}

	policies := []*networkingv1ac.NetworkPolicyApplyConfiguration{
		defaultDenyNetworkPolicy(ns),
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
