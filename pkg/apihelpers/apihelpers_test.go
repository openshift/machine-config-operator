package apihelpers

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func newKubeletConfig(name string, generation, observedGeneration int64, conditionType mcfgv1.KubeletConfigStatusConditionType, conditionStatus corev1.ConditionStatus, selector map[string]string) *mcfgv1.KubeletConfig {
	return &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Generation: generation,
		},
		Spec: mcfgv1.KubeletConfigSpec{
			MachineConfigPoolSelector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
		},
		Status: mcfgv1.KubeletConfigStatus{
			ObservedGeneration: observedGeneration,
			Conditions: []mcfgv1.KubeletConfigCondition{
				{
					Type:   conditionType,
					Status: conditionStatus,
				},
			},
		},
	}
}

func newContainerRuntimeConfig(name string, generation, observedGeneration int64, conditionType mcfgv1.ContainerRuntimeConfigStatusConditionType, selector map[string]string) *mcfgv1.ContainerRuntimeConfig {
	return &mcfgv1.ContainerRuntimeConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Generation: generation,
		},
		Spec: mcfgv1.ContainerRuntimeConfigSpec{
			MachineConfigPoolSelector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
		},
		Status: mcfgv1.ContainerRuntimeConfigStatus{
			ObservedGeneration: observedGeneration,
			Conditions: []mcfgv1.ContainerRuntimeConfigCondition{
				{
					Type: conditionType,
				},
			},
		},
	}
}

func newMachineConfig(name string) *mcfgv1.MachineConfig {
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

type testListers struct {
	crcLister mcfglistersv1.ContainerRuntimeConfigLister
	mckLister mcfglistersv1.KubeletConfigLister
	mcLister  mcfglistersv1.MachineConfigLister
}

func buildListers(t *testing.T, kubeletConfigs []*mcfgv1.KubeletConfig, containerRuntimeConfigs []*mcfgv1.ContainerRuntimeConfig, machineConfigs []*mcfgv1.MachineConfig) testListers {
	t.Helper()

	mckIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, kc := range kubeletConfigs {
		require.NoError(t, mckIndexer.Add(kc))
	}

	crcIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, crc := range containerRuntimeConfigs {
		require.NoError(t, crcIndexer.Add(crc))
	}

	mcIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, mc := range machineConfigs {
		require.NoError(t, mcIndexer.Add(mc))
	}

	return testListers{
		crcLister: mcfglistersv1.NewContainerRuntimeConfigLister(crcIndexer),
		mckLister: mcfglistersv1.NewKubeletConfigLister(mckIndexer),
		mcLister:  mcfglistersv1.NewMachineConfigLister(mcIndexer),
	}
}

func TestAreMCGeneratingSubControllersCompletedForPool(t *testing.T) {
	workerLabels := map[string]string{"node-role.kubernetes.io/worker": ""}

	tests := []struct {
		name               string
		kubeletConfigs     []*mcfgv1.KubeletConfig
		containerRTConfigs []*mcfgv1.ContainerRuntimeConfig
		machineConfigs     []*mcfgv1.MachineConfig
		poolName           string
		poolLabels         map[string]string
		errorContains      string
	}{
		{
			name:       "no KubeletConfig or ContainerRuntimeConfig matching the pool",
			poolName:   "custom-pool",
			poolLabels: map[string]string{"custom-label": "true"},
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("worker-default", 1, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
			},
		},
		{
			name:       "KubeletConfig reconciled and generated MC exists",
			poolName:   "worker",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("worker-default", 1, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
			},
		},
		{
			name:       "KubeletConfig reconciled with KubeletConfigAccepted condition",
			poolName:   "worker",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("worker-default", 2, 2, mcfgv1.KubeletConfigAccepted, corev1.ConditionTrue, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
			},
		},
		{
			name:       "KubeletConfig reconciled but generated MC does NOT exist yet",
			poolName:   "custom-pool",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("worker-default", 1, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
			},
			errorContains: "expected at least 1 generated kubelet MachineConfig",
		},
		{
			name:       "KubeletConfig generation mismatch",
			poolName:   "worker",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("worker-default", 2, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
			},
			errorContains: "status for KubeletConfig worker-default is being reported for 1, expecting it for 2",
		},
		{
			name:       "KubeletConfig not completed (failure condition)",
			poolName:   "worker",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("worker-default", 1, 1, mcfgv1.KubeletConfigFailure, corev1.ConditionTrue, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
			},
			errorContains: "KubeletConfig has not completed",
		},
		{
			name:       "KubeletConfig not completed (no conditions)",
			poolName:   "worker",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "worker-default", Generation: 1},
					Spec: mcfgv1.KubeletConfigSpec{
						MachineConfigPoolSelector: &metav1.LabelSelector{MatchLabels: workerLabels},
					},
					Status: mcfgv1.KubeletConfigStatus{ObservedGeneration: 1},
				},
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
			},
			errorContains: "KubeletConfig has not completed",
		},
		{
			name:       "multiple KubeletConfigs matching pool, only one MC exists",
			poolName:   "worker",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("kc-1", 1, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
				newKubeletConfig("kc-2", 1, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
			},
			errorContains: "expected at least 2 generated kubelet MachineConfig",
		},
		{
			name:       "multiple KubeletConfigs matching pool, all MCs exist",
			poolName:   "worker",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("kc-1", 1, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
				newKubeletConfig("kc-2", 1, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
				newMachineConfig("99-worker-generated-kubelet-1"),
			},
		},
		{
			name:       "ContainerRuntimeConfig not completed (failure condition)",
			poolName:   "worker",
			poolLabels: workerLabels,
			containerRTConfigs: []*mcfgv1.ContainerRuntimeConfig{
				newContainerRuntimeConfig("crc-1", 1, 1, mcfgv1.ContainerRuntimeConfigFailure, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-containerruntime"),
			},
			errorContains: "ContainerRuntimeConfig has not completed",
		},
		{
			name:       "ContainerRuntimeConfig not completed (no conditions)",
			poolName:   "worker",
			poolLabels: workerLabels,
			containerRTConfigs: []*mcfgv1.ContainerRuntimeConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "crc-1", Generation: 1},
					Spec: mcfgv1.ContainerRuntimeConfigSpec{
						MachineConfigPoolSelector: &metav1.LabelSelector{MatchLabels: workerLabels},
					},
					Status: mcfgv1.ContainerRuntimeConfigStatus{ObservedGeneration: 1},
				},
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-containerruntime"),
			},
			errorContains: "ContainerRuntimeConfig has not completed",
		},
		{
			name:       "ContainerRuntimeConfig reconciled but generated MC missing",
			poolName:   "worker",
			poolLabels: workerLabels,
			containerRTConfigs: []*mcfgv1.ContainerRuntimeConfig{
				newContainerRuntimeConfig("crc-1", 1, 1, mcfgv1.ContainerRuntimeConfigSuccess, workerLabels),
			},
			errorContains: "expected at least 1 generated containerruntime MachineConfig",
		},
		{
			name:       "ContainerRuntimeConfig reconciled and generated MC exists",
			poolName:   "worker",
			poolLabels: workerLabels,
			containerRTConfigs: []*mcfgv1.ContainerRuntimeConfig{
				newContainerRuntimeConfig("crc-1", 1, 1, mcfgv1.ContainerRuntimeConfigSuccess, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-containerruntime"),
			},
		},
		{
			name:       "ContainerRuntimeConfig generation mismatch",
			poolName:   "worker",
			poolLabels: workerLabels,
			containerRTConfigs: []*mcfgv1.ContainerRuntimeConfig{
				newContainerRuntimeConfig("crc-1", 2, 1, mcfgv1.ContainerRuntimeConfigSuccess, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-containerruntime"),
			},
			errorContains: "status for ContainerRuntimeConfig crc-1 is being reported for 1, expecting it for 2",
		},
		{
			name:       "both KubeletConfig and ContainerRuntimeConfig present and complete",
			poolName:   "worker",
			poolLabels: workerLabels,
			kubeletConfigs: []*mcfgv1.KubeletConfig{
				newKubeletConfig("worker-default", 1, 1, mcfgv1.KubeletConfigSuccess, corev1.ConditionTrue, workerLabels),
			},
			containerRTConfigs: []*mcfgv1.ContainerRuntimeConfig{
				newContainerRuntimeConfig("crc-1", 1, 1, mcfgv1.ContainerRuntimeConfigSuccess, workerLabels),
			},
			machineConfigs: []*mcfgv1.MachineConfig{
				newMachineConfig("99-worker-generated-kubelet"),
				newMachineConfig("99-worker-generated-containerruntime"),
			},
		},
		{
			name:       "no configs at all",
			poolName:   "worker",
			poolLabels: workerLabels,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := buildListers(t, tt.kubeletConfigs, tt.containerRTConfigs, tt.machineConfigs)
			err := AreMCGeneratingSubControllersCompletedForPool(l.crcLister, l.mckLister, l.mcLister, tt.poolName, tt.poolLabels)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
