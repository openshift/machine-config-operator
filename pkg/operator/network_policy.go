package operator

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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

// allowAllEgressNetworkPolicy allows all egress for every pod in the namespace.
// This covers the operator, controller, machine-os-builder, and ephemeral
// builder pods (which lack a k8s-app label) that need DNS and registry access.
func allowAllEgressNetworkPolicy(namespace string) *networkingv1ac.NetworkPolicyApplyConfiguration {
	return networkingv1ac.NetworkPolicy("allow-all-egress", namespace).
		WithSpec(networkingv1ac.NetworkPolicySpec().
			WithPodSelector(metav1ac.LabelSelector()).
			WithEgress(networkingv1ac.NetworkPolicyEgressRule()).
			WithPolicyTypes(networkingv1.PolicyTypeEgress))
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
			WithPolicyTypes(networkingv1.PolicyTypeIngress))
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
			WithPolicyTypes(networkingv1.PolicyTypeIngress))
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
			WithPolicyTypes(networkingv1.PolicyTypeIngress))
}

// desiredNetworkPolicySpec returns the expected spec for each managed policy
// so we can compare against the lister's cached copy before making API calls.
func desiredNetworkPolicySpec(name string) networkingv1.NetworkPolicySpec {
	switch name {
	case "default-deny":
		return networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		}
	case "allow-all-egress":
		return networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		}
	case "allow-machine-config-operator":
		return allowPolicySpec("machine-config-operator")
	case "allow-machine-config-controller":
		return allowPolicySpec("machine-config-controller")
	case "allow-machine-os-builder":
		return allowPolicySpec("machine-os-builder")
	default:
		return networkingv1.NetworkPolicySpec{}
	}
}

func allowPolicySpec(app string) networkingv1.NetworkPolicySpec {
	return networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": app}},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			Ports: []networkingv1.NetworkPolicyPort{{
				Protocol: ptr.To(corev1.ProtocolTCP),
				Port:     ptr.To(intstr.FromInt32(9001)),
			}},
		}},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
	}
}

// networkPolicyMatchesDesired checks the informer cache for an existing policy
// and returns true if its spec already matches the desired state. This avoids
// unnecessary API round-trips when nothing has changed.
func (optr *Operator) networkPolicyMatchesDesired(name, namespace string) bool {
	if optr.networkPolicyLister == nil {
		return false
	}
	existing, err := optr.networkPolicyLister.NetworkPolicies(namespace).Get(name)
	if err != nil {
		return false
	}
	return equality.Semantic.DeepEqual(existing.Spec, desiredNetworkPolicySpec(name))
}

func (optr *Operator) syncNetworkPolicies(_ *renderConfig, _ *configv1.ClusterOperator) error {
	ctx := context.TODO()
	ns := ctrlcommon.MCONamespace

	// Apply allow rules before default-deny so that traffic is never blocked
	// during the window between sequential resource applies.
	applyOpts := metav1.ApplyOptions{FieldManager: networkPolicyFieldManager, Force: true}
	policies := []*networkingv1ac.NetworkPolicyApplyConfiguration{
		allowAllEgressNetworkPolicy(ns),
		allowMCONetworkPolicy(ns),
		allowMCCNetworkPolicy(ns),
		allowMOBNetworkPolicy(ns),
	}

	for _, policy := range policies {
		name := *policy.Name
		if optr.networkPolicyMatchesDesired(name, ns) {
			continue
		}
		if _, err := optr.kubeClient.NetworkingV1().NetworkPolicies(ns).Apply(ctx, policy, applyOpts); err != nil {
			return err
		}
	}

	// Apply default-deny last via raw patch so empty ingress/egress slices are
	// preserved in the payload and SSA claims ownership of those fields.
	if !optr.networkPolicyMatchesDesired("default-deny", ns) {
		patchOpts := metav1.PatchOptions{FieldManager: networkPolicyFieldManager, Force: ptr.To(true)}
		if _, err := optr.kubeClient.NetworkingV1().NetworkPolicies(ns).Patch(
			ctx, "default-deny", types.ApplyPatchType, defaultDenyPatchBody(), patchOpts,
		); err != nil {
			return err
		}
	}

	return nil
}
