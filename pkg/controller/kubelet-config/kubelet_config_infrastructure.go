package kubeletconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/golang/glog"

	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	"github.com/openshift/machine-config-operator/pkg/version"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
)

func (ctrl *Controller) infraConfigWorker() {
	for ctrl.processNextInfraConfigWorkItem() {
	}
}

func (ctrl *Controller) processNextInfraConfigWorkItem() bool {
	key, quit := ctrl.infraConfigQueue.Get()
	if quit {
		return false
	}
	defer ctrl.infraConfigQueue.Done(key)

	err := ctrl.syncInfraConfigHandler(key.(string))
	ctrl.handleInfraConfigErr(err, key)
	return true
}

func (ctrl *Controller) handleInfraConfigErr(err error, key interface{}) {
	if err == nil {
		ctrl.infraConfigQueue.Forget(key)
		return
	}

	if ctrl.infraConfigQueue.NumRequeues(key) < maxRetries {
		glog.V(4).Infof("Error syncing infrastructure configuration %v: %v", key, err)
		ctrl.infraConfigQueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping infrastructure config %q out of the queue: %v", key, err)
	ctrl.infraConfigQueue.Forget(key)
	ctrl.infraConfigQueue.AddAfter(key, 1*time.Minute)
}

func (ctrl *Controller) syncInfraConfigHandler(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing infraConfig handler %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing infraConfig handler %q (%v)", key, time.Since(startTime))
	}()

	// Fetch the Feature Gates
	infraConfig, err := ctrl.infraConfigLister.Get(ctrlcommon.ClusterInfrastructureInstanceName)
	if err != nil {
		return fmt.Errorf("could not fetch Infra: %w", err)
	}

	if infraConfig.Status.CPUPartitioning != osev1.CPUPartitioningAllNodes {
		glog.V(4).Info("Skipping altering CPU partitioning, Infrastructure CPUPartitioningMode is not Set for AllNodes")
		return nil
	}

	// When it comes to single node, we'll need to do an extra check to account for upgrades
	// and pre-existing installs that are already configured for CPU Partitioning.
	// We collect the partitioning status of the nodes and save them by role.
	singleNodeRoleIsConfigured := map[string]bool{}
	if infraConfig.Status.InfrastructureTopology == osev1.SingleReplicaTopologyMode {
		nodes, err := ctrl.kubeClient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("could not fetch Infra: %w", err)
		}
		singleNodeRoleIsConfigured = collectNodePartitioningStatus(nodes.Items)
	}

	// Find all MachineConfigPools
	mcpPools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}

	for _, pool := range mcpPools {
		role := pool.Name

		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), cpuPartitionMCName(role), metav1.GetOptions{})
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err
		}
		if isNotFound {
			ignConfig := cpuPartitionIgn()
			mc, err = cpuPartitionMC(role, ignConfig)
			if err != nil {
				return err
			}
		} else {
			ignConfig := cpuPartitionIgn()
			rawIgnition, err := json.Marshal(ignConfig)
			if err != nil {
				return err
			}
			mc.Spec.Config = runtime.RawExtension{Raw: rawIgnition}
		}

		// If the MC for Partitioning doesn't exist and the single node role is already configured
		// We skip this as it's not needed.
		if isNotFound && singleNodeRoleIsConfigured[role] {
			continue
		}

		// Create or Update, on conflict retry
		if err := retry.RetryOnConflict(updateBackoff, func() error {
			var err error
			if isNotFound {
				_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
			} else {
				_, err = ctrl.client.MachineconfigurationV1().MachineConfigs().Update(context.TODO(), mc, metav1.UpdateOptions{})
			}
			return err
		}); err != nil {
			return fmt.Errorf("could not Create/Update MachineConfig, error: %w", err)
		}
		glog.Infof("Applied CPU Partitioning configuration %v on MachineConfigPool %v", key, pool.Name)
	}

	return nil
}

func (ctrl *Controller) enqueueInfraConfig(infraConfig *osev1.Infrastructure) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(infraConfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", infraConfig, err))
		return
	}
	ctrl.infraConfigQueue.Add(key)
}

func (ctrl *Controller) updateInfraConfig(old, cur interface{}) {
	oldInfra, ok := old.(*osev1.Infrastructure)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("couldn't retrieve the old object from the Update Infrastructure Config event %#v", old))
		return
	}
	newInfra, ok := cur.(*osev1.Infrastructure)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("couldn't retrieve the new object from the Update Infrastructure Config event %#v", cur))
		return
	}
	if newInfra.Name != ctrlcommon.ClusterInfrastructureInstanceName {
		message := fmt.Sprintf("The infrastructure.config.openshift.io \"%v\" is invalid: metadata.name Invalid value: \"%v\" : must be \"%v\"", newInfra.Name, newInfra.Name, ctrlcommon.ClusterInfrastructureInstanceName)
		ctrl.eventRecorder.Eventf(oldInfra, corev1.EventTypeNormal, "ActionProhibited", message)
		glog.V(2).Infof(message)
		return
	}
	if !reflect.DeepEqual(oldInfra.Status, newInfra.Status) {
		glog.V(4).Infof("Updating the infrastructure config resource, name: %s", newInfra.Name)
		ctrl.enqueueInfraConfig(newInfra)
	}
}

func (ctrl *Controller) addInfraConfig(obj interface{}) {
	infraConfig, ok := obj.(*osev1.Infrastructure)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("couldn't retrieve the object from the Add Infrastructure Config event %#v", obj))
		return
	}
	if infraConfig.Name != ctrlcommon.ClusterInfrastructureInstanceName {
		message := fmt.Sprintf("The infrastructure.config.openshift.io \"%v\" is invalid: metadata.name Invalid value: \"%v\" : must be \"%v\"", infraConfig.Name, infraConfig.Name, ctrlcommon.ClusterInfrastructureInstanceName)
		glog.V(2).Infof(message)
		ctrl.eventRecorder.Eventf(infraConfig, corev1.EventTypeNormal, "ActionProhibited", message)
		return
	}
	glog.V(4).Infof("Adding the infrastructure config resource, name: %s", infraConfig.Name)
	ctrl.enqueueInfraConfig(infraConfig)
}

func (ctrl *Controller) deleteInfraConfig(obj interface{}) {
	infraConfig, ok := obj.(*osev1.Infrastructure)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("couldn't retrieve the object from the Add Infrastructure Config event %#v", obj))
		return
	}
	if infraConfig.Name != ctrlcommon.ClusterInfrastructureInstanceName && infraConfig.Status.CPUPartitioning == osev1.CPUPartitioningAllNodes {
		glog.V(4).Infof("Infrastructure config %s was deleted, turning off CPUPinning is not supported, no action will be taken", infraConfig.Name)
	}
}

func RunInfraConfigBootstrap(infraConfig *osev1.Infrastructure, mcpPools []*mcfgv1.MachineConfigPool) ([]*mcfgv1.MachineConfig, error) {
	configs := []*mcfgv1.MachineConfig{}

	if infraConfig == nil {
		return configs, fmt.Errorf("infrastructure resource is nil, can not generate CPU Partitioning")
	}

	if infraConfig.Status.CPUPartitioning != osev1.CPUPartitioningAllNodes {
		return configs, nil
	}

	for _, pool := range mcpPools {
		role := pool.Name

		// Get MachineConfig
		mc, err := cpuPartitionMC(role, cpuPartitionIgn())
		if err != nil {
			return nil, err
		}
		configs = append(configs, mc)
	}

	return configs, nil
}

func cpuPartitionMC(role string, ignitionConfig ign3types.Config) (*mcfgv1.MachineConfig, error) {
	rawIgnition, err := json.Marshal(ignitionConfig)
	if err != nil {
		return nil, err
	}

	mc := &mcfgv1.MachineConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: mcfgv1.GroupVersion.String(),
			Kind:       "MachineConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: cpuPartitionMCName(role),
			Labels: map[string]string{
				"machineconfiguration.openshift.io/role": role,
			},
			Annotations: map[string]string{
				ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
			},
		},
		Spec: mcfgv1.MachineConfigSpec{},
	}
	mc.Spec.Config = runtime.RawExtension{Raw: rawIgnition}
	return mc, nil
}

func cpuPartitionIgn() ign3types.Config {
	mode := 420
	source := "data:text/plain;charset=utf-8;base64,ewogICJtYW5hZ2VtZW50IjogewogICAgImNwdXNldCI6ICIiCiAgfQp9Cg=="
	return ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Storage: ign3types.Storage{
			Files: []ign3types.File{{
				Node: ign3types.Node{
					Path: "/etc/kubernetes/openshift-workload-pinning",
				},
				FileEmbedded1: ign3types.FileEmbedded1{
					Contents: ign3types.Resource{
						Source: &source,
					},
					Mode: &mode,
				},
			}},
		},
	}
}

func cpuPartitionMCName(role string) string {
	return fmt.Sprintf("01-%s-cpu-partitioning", role)
}

func containsCPUResource(resources corev1.ResourceList) bool {
	for k := range resources {
		if strings.Contains(k.String(), workloadsCapacitySuffix) {
			return true
		}
	}
	return false
}

func collectNodePartitioningStatus(nodes []corev1.Node) map[string]bool {
	result := map[string]bool{}
	for _, node := range nodes {
		_, isMaster := node.Labels["node-role.kubernetes.io/master"]
		if isMaster {
			result["master"] = containsCPUResource(node.Status.Allocatable)
		} else {
			result["worker"] = containsCPUResource(node.Status.Allocatable)
		}
	}
	return result
}
