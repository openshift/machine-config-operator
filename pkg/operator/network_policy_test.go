package operator

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/events"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/clock"
)

func newOperatorForNetworkPolicyTest(pools []*mcfgv1.MachineConfigPool) (*Operator, *fake.Clientset) {
	kubeClient := fake.NewSimpleClientset()
	mcfgClient := fakeclientmachineconfigv1.NewSimpleClientset()
	mcfgInformerFactory := mcfginformers.NewSharedInformerFactoryWithOptions(mcfgClient, 0, mcfginformers.WithNamespace(ctrlcommon.MCONamespace))
	mcpInformer := mcfgInformerFactory.Machineconfiguration().V1().MachineConfigPools()
	moscInformer := mcfgInformerFactory.Machineconfiguration().V1().MachineOSConfigs()

	for _, pool := range pools {
		mcpInformer.Informer().GetIndexer().Add(pool)
	}

	optr := &Operator{
		namespace:   ctrlcommon.MCONamespace,
		kubeClient:  kubeClient,
		mcpLister:   mcpInformer.Lister(),
		moscLister:  moscInformer.Lister(),
		libgoRecorder: events.NewInMemoryRecorder("test", clock.RealClock{}),
	}

	return optr, kubeClient
}

func renderConfigForTest() *renderConfig {
	return &renderConfig{
		TargetNamespace: ctrlcommon.MCONamespace,
	}
}

func TestSyncNetworkPoliciesStaticPolicies(t *testing.T) {
	optr, kubeClient := newOperatorForNetworkPolicyTest(nil)
	config := renderConfigForTest()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies, err := kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	policyNames := make([]string, len(policies.Items))
	for i, p := range policies.Items {
		policyNames[i] = p.Name
	}

	assert.Contains(t, policyNames, "default-deny-all")
	assert.Contains(t, policyNames, "allow-machine-config-operator")
	assert.Contains(t, policyNames, "allow-machine-config-controller")
	assert.NotContains(t, policyNames, "allow-machine-os-builder")
}

func TestSyncNetworkPoliciesDenyAll(t *testing.T) {
	optr, kubeClient := newOperatorForNetworkPolicyTest(nil)
	config := renderConfigForTest()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	np, err := kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Get(context.TODO(), "default-deny-all", metav1.GetOptions{})
	require.NoError(t, err)

	assert.Equal(t, metav1.LabelSelector{}, np.Spec.PodSelector)
	assert.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
	assert.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
	assert.Empty(t, np.Spec.Ingress)
	assert.Empty(t, np.Spec.Egress)
}

func TestSyncNetworkPoliciesMOBCreatedWithLayering(t *testing.T) {
	layeredPool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
	layeredPool.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""

	optr, kubeClient := newOperatorForNetworkPolicyTest([]*mcfgv1.MachineConfigPool{layeredPool})
	config := renderConfigForTest()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies, err := kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	policyNames := make([]string, len(policies.Items))
	for i, p := range policies.Items {
		policyNames[i] = p.Name
	}

	assert.Contains(t, policyNames, "allow-machine-os-builder")
	assert.Len(t, policies.Items, 4)
}

func TestSyncNetworkPoliciesMOBDeletedWithoutLayering(t *testing.T) {
	optr, kubeClient := newOperatorForNetworkPolicyTest(nil)
	config := renderConfigForTest()

	// Pre-create the MOB policy as if layering was previously enabled
	mobPolicy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "allow-machine-os-builder",
			Namespace: ctrlcommon.MCONamespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"k8s-app": "machine-os-builder"},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		},
	}
	_, err := kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Create(context.TODO(), mobPolicy, metav1.CreateOptions{})
	require.NoError(t, err)

	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies, err := kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	for _, p := range policies.Items {
		assert.NotEqual(t, "allow-machine-os-builder", p.Name)
	}
}

func TestSyncNetworkPoliciesDriftProtection(t *testing.T) {
	optr, kubeClient := newOperatorForNetworkPolicyTest(nil)
	config := renderConfigForTest()

	// Initial sync creates policies
	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	// Simulate user tampering: modify the deny-all policy to allow ingress
	denyAll, err := kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Get(context.TODO(), "default-deny-all", metav1.GetOptions{})
	require.NoError(t, err)

	denyAll.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}}
	_, err = kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Update(context.TODO(), denyAll, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Re-sync should revert the modification
	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	reverted, err := kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).Get(context.TODO(), "default-deny-all", metav1.GetOptions{})
	require.NoError(t, err)

	assert.Empty(t, reverted.Spec.Ingress)
	assert.Empty(t, reverted.Spec.Egress)
}

func TestSyncNetworkPoliciesIdempotent(t *testing.T) {
	optr, kubeClient := newOperatorForNetworkPolicyTest(nil)
	config := renderConfigForTest()

	err := optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	// Run again — should succeed without error
	err = optr.syncNetworkPolicies(config, nil)
	require.NoError(t, err)

	policies, err := kubeClient.NetworkingV1().NetworkPolicies(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, policies.Items, 3)
}
