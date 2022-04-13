package kubeletconfig

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/clarketm/json"
	"github.com/golang/glog"
	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
)

func (ctrl *Controller) nodeConfigWorker() {
	for ctrl.processNextNodeConfigWorkItem() {
	}
}

func (ctrl *Controller) processNextNodeConfigWorkItem() bool {
	key, quit := ctrl.nodeConfigQueue.Get()
	if quit {
		return false
	}
	defer ctrl.nodeConfigQueue.Done(key)

	err := ctrl.syncNodeConfigHandler(key.(string))
	ctrl.handleNodeConfigErr(err, key)
	return true
}

func (ctrl *Controller) handleNodeConfigErr(err error, key interface{}) {
	if err == nil {
		ctrl.nodeConfigQueue.Forget(key)
		return
	}

	if ctrl.nodeConfigQueue.NumRequeues(key) < maxRetries {
		glog.V(4).Infof("Error syncing node configuration %v: %v", key, err)
		ctrl.nodeConfigQueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	glog.V(2).Infof("Dropping node config %q out of the queue: %v", key, err)
	ctrl.nodeConfigQueue.Forget(key)
	ctrl.nodeConfigQueue.AddAfter(key, 1*time.Minute)
}

// syncNodeConfigHandler syncs whenever there is a change on the nodes.config.openshift.io resource
// nodes.config.openshift.io object holds the cluster-wide information about the
// node specific features such as cgroup modes, workerlatencyprofiles, etc.
func (ctrl *Controller) syncNodeConfigHandler(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing nodeConfig handler %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing nodeConfig handler %q (%v)", key, time.Since(startTime))
	}()

	// Fetch the Feature Gates
	features, err := ctrl.featLister.Get(ctrlcommon.ClusterFeatureInstanceName)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("Using the default featuregates")
		features = createNewDefaultFeatureGate()
	} else if err != nil {
		return err
	}
	// Fetch the Node
	nodeConfig, err := ctrl.nodeConfigLister.Get(ctrlcommon.ClusterNodeInstanceName)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("Node configuration %v is missing", key)
		return fmt.Errorf("missing node configuration, key: %v", key)
	} else if err != nil {
		err := fmt.Errorf("could not fetch Node: %w", err)
		return err
	}

	// Fetch the controllerconfig
	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig, err: %w", err)
	}
	// Find all MachineConfigPools
	mcpPools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}

	for _, pool := range mcpPools {
		role := pool.Name
		// the workerlatencyprofile's configuration change will be applied only on the worker nodes.
		if role != ctrlcommon.MachineConfigPoolWorker {
			continue
		}
		// Get MachineConfig
		key, err := getManagedNodeConfigKey(pool, ctrl.client)
		if err != nil {
			return err
		}
		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), key, metav1.GetOptions{})
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err
		}
		if isNotFound {
			ignConfig := ctrlcommon.NewIgnConfig()
			mc, err = ctrlcommon.MachineConfigFromIgnConfig(role, key, ignConfig)
			if err != nil {
				return err
			}
		}
		// checking if the Node spec is empty and accordingly returning from here.
		if reflect.DeepEqual(nodeConfig.Spec, osev1.NodeSpec{}) {
			glog.V(2).Info("empty Node resource found")
			ctrl.updateNodeConfigStatus(pool, nodeConfig)
			return nil
		}
		// updating the nodeConfig status based on the machineconfigpool status
		err = ctrl.updateNodeConfigStatus(pool, nodeConfig)
		if err != nil {
			return err
		}
		// restricting the sync if the nodeConfig status condition is still Progressing.
		if !isReadyforUpdate(nodeConfig) {
			return fmt.Errorf("unable to modify the kubelet configuration on the node, node condition status not ready")
		}
		originalKubeConfig, err := generateOriginalKubeletConfigWithFeatureGates(cc, ctrl.templatesDir, role, features)
		if err != nil {
			return err
		}
		// updating the kubelet configuration with the Node specific configuration.
		err = updateOriginalKubeConfigwithNodeConfig(nodeConfig, originalKubeConfig)
		if err != nil {
			return err
		}
		// Encode the new config into raw JSON
		cfgIgn, err := kubeletConfigToIgnFile(originalKubeConfig)
		if err != nil {
			return err
		}
		tempIgnConfig := ctrlcommon.NewIgnConfig()
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, *cfgIgn)
		rawCfgIgn, err := json.Marshal(tempIgnConfig)
		if err != nil {
			return err
		}
		mc.Spec.Config.Raw = rawCfgIgn
		mc.ObjectMeta.Annotations = map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
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
			return fmt.Errorf("Could not Create/Update MachineConfig, error: %w", err)
		}
		glog.Infof("Applied Node configuration %v on MachineConfigPool %v", key, pool.Name)

		// updating the nodeConfig status based on the machineconfigpool status
		err = ctrl.updateNodeConfigStatus(pool, nodeConfig)
		if err != nil {
			return err
		}
	}
	// fetch the kubeletconfigs
	kcs, err := ctrl.mckLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("could not get KubeletConfigs, err: %w", err)
	}
	for _, kc := range kcs {
		// updating the existing kubeletconfigs with the updated nodeconfig
		err := ctrl.syncKubeletConfig(kc.Name)
		if err != nil {
			return fmt.Errorf("could not update KubeletConfig %v, err: %w", kc, err)
		}
	}

	return nil
}

func (ctrl *Controller) enqueueNodeConfig(nodeConfig *osev1.Node) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(nodeConfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %w", nodeConfig, err))
		return
	}
	ctrl.nodeConfigQueue.Add(key)
}

func (ctrl *Controller) updateNodeConfig(old, cur interface{}) {
	oldNode, ok := old.(*osev1.Node)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("Couldn't retrieve the old object from the Update Node Config event %#v", old))
		return
	}
	newNode, ok := cur.(*osev1.Node)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("Couldn't retrieve the new object from the Update Node Config event %#v", cur))
		return
	}
	if newNode.Name != ctrlcommon.ClusterNodeInstanceName {
		message := fmt.Sprintf("The node.config.openshift.io \"%v\" is invalid: metadata.name Invalid value: \"%v\" : must be \"%v\"", newNode.Name, newNode.Name, ctrlcommon.ClusterNodeInstanceName)
		glog.V(2).Infof(message)
		ctrl.updateNodeConfigDegradedStatus(newNode, message, "UpdateProhibited")
		return
	}
	if !reflect.DeepEqual(oldNode.Spec, newNode.Spec) {
		// skipping the update in case of the Worker-Latency-Profile type transition from "Default" to "LowUpdateSlowReaction" and vice-versa
		// (TODO) Ideally the user request has to be honoured, the transition need to be from Default -> Medium -> Low or vice-versa.
		// Restricting the request for now until this process is automated in future.
		if (oldNode.Spec.WorkerLatencyProfile == osev1.DefaultUpdateDefaultReaction && newNode.Spec.WorkerLatencyProfile == osev1.LowUpdateSlowReaction) || (oldNode.Spec.WorkerLatencyProfile == osev1.LowUpdateSlowReaction && newNode.Spec.WorkerLatencyProfile == osev1.DefaultUpdateDefaultReaction) {
			message := fmt.Sprintf("Skipping the Update Node event, name: %s, transition not allowed from old WorkerLatencyProfile: %s to new WorkerLatencyProfile: %s", newNode.Name, oldNode.Spec.WorkerLatencyProfile, newNode.Spec.WorkerLatencyProfile)
			glog.Infof(message)
			ctrl.updateNodeConfigDegradedStatus(newNode, message, "UpdateSkipped")
			return
		}
		glog.V(4).Infof("Updating the node config resource, name: %s", newNode.Name)
		ctrl.enqueueNodeConfig(newNode)
	}
}

func (ctrl *Controller) addNodeConfig(obj interface{}) {
	nodeConfig, ok := obj.(*osev1.Node)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("Couldn't retrieve the object from the Add Node Config event %#v", obj))
		return
	}
	if nodeConfig.Name != ctrlcommon.ClusterNodeInstanceName {
		message := fmt.Sprintf("The node.config.openshift.io \"%v\" is invalid: metadata.name Invalid value: \"%v\" : must be \"%v\"", nodeConfig.Name, nodeConfig.Name, ctrlcommon.ClusterNodeInstanceName)
		glog.V(2).Infof(message)
		ctrl.updateNodeConfigDegradedStatus(nodeConfig, message, "UpdateProhibited")
		return
	}
	glog.V(4).Infof("Adding the node config resource, name: %s", nodeConfig.Name)
	ctrl.enqueueNodeConfig(nodeConfig)
}

func (ctrl *Controller) deleteNodeConfig(obj interface{}) {
	nodeConfig, ok := obj.(*osev1.Node)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		nodeConfig, ok = tombstone.Obj.(*osev1.Node)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a NodeConfig %#v", obj))
			return
		}
	}
	glog.V(4).Infof("Deleted node config %s and restored default config", nodeConfig.Name)
}

// isReadyforUpdate returns a boolean based on the condition statuses present in the node config CR.
// The node config resource is considered to be not-ready if any status condition
// of any operator type is "Progressing". Otherwise, the resource is ready for the update.
func isReadyforUpdate(nodeConfig *osev1.Node) bool {
	var conditionTypes = []string{
		osev1.KubeAPIServerProgressing,
		osev1.KubeControllerManagerProgressing,
		osev1.KubeletProgressing,
		osev1.KubeAPIServerDegraded,
		osev1.KubeControllerManagerDegraded,
		osev1.KubeletDegraded,
	}
	// if the node config resource is empty, it is ready for the update
	// to hold the new configuration update request.
	if nodeConfig == nil {
		return true
	}
	if len(nodeConfig.Status.WorkerLatencyProfileStatus.Conditions) > 0 {
		for _, condition := range nodeConfig.Status.WorkerLatencyProfileStatus.Conditions {
			for _, conditionType := range conditionTypes {
				if conditionType == condition.Type {
					return false
				}
			}
		}
	}
	return true
}

// updateNodeConfigDegradedStatus updates the degraded status of the node config based on the message and reason
func (ctrl *Controller) updateNodeConfigDegradedStatus(nodeConfig *osev1.Node, message, reason string) error {
	if nodeConfig == nil {
		return fmt.Errorf("unable to update the node config status")
	}
	var nodeConfigCondition metav1.Condition

	nodeConfigCondition.Type = osev1.KubeletDegraded
	nodeConfigCondition.Status = metav1.ConditionUnknown
	nodeConfigCondition.LastTransitionTime = metav1.Now()
	nodeConfigCondition.Reason = reason
	nodeConfigCondition.Message = message
	return ctrl.updateMCOStatus(nodeConfig, nodeConfigCondition)
}

// updateNodeConfigStatus updates the status of the node config object based on the machineconfigpool status
func (ctrl *Controller) updateNodeConfigStatus(pool *mcfgv1.MachineConfigPool, nodeConfig *osev1.Node) error {
	if pool == nil || nodeConfig == nil {
		return fmt.Errorf("unable to update the nodeConfig status, incomplete data, machineConfigpool : %v, nodeConfig: %v", pool, nodeConfig)
	}
	var nodeConfigCondition metav1.Condition

	nodeConfigCondition.Type = osev1.KubeletComplete
	nodeConfigCondition.LastTransitionTime = metav1.Now()
	nodeConfigCondition.Status = metav1.ConditionUnknown
	nodeConfigCondition.Reason = "ConditionUnknown"
	nodeConfigCondition.Message = "Condition Unknown"

	switch {
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded):
		cond := mcfgv1.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolRenderDegraded)
		nodeConfigCondition.Type = osev1.KubeletDegraded
		nodeConfigCondition.Status = metav1.ConditionStatus(cond.Status)
		nodeConfigCondition.Reason = "MachineConfigPoolRenderDegraded"
		nodeConfigCondition.Message = "Machine Config Pool Render Degraded"
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolNodeDegraded):
		cond := mcfgv1.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolNodeDegraded)
		nodeConfigCondition.Type = osev1.KubeletDegraded
		nodeConfigCondition.Status = metav1.ConditionStatus(cond.Status)
		nodeConfigCondition.Reason = "MachineConfigPoolNodeDegraded"
		nodeConfigCondition.Message = "Machine Config Pool Node Degraded"
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdated):
		cond := mcfgv1.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolUpdated)
		nodeConfigCondition.Type = osev1.KubeletComplete
		nodeConfigCondition.Status = metav1.ConditionStatus(cond.Status)
		nodeConfigCondition.Reason = "MachineConfigPoolUpdated"
		nodeConfigCondition.Message = "Machine Config Pool Updated"
	case mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating):
		cond := mcfgv1.GetMachineConfigPoolCondition(pool.Status, mcfgv1.MachineConfigPoolUpdating)
		nodeConfigCondition.Type = osev1.KubeletProgressing
		nodeConfigCondition.Status = metav1.ConditionStatus(cond.Status)
		nodeConfigCondition.Reason = "MachineConfigPoolUpdating"
		nodeConfigCondition.Message = fmt.Sprintf("%d (ready %d) out of %d nodes are updating to latest configuration %s", pool.Status.UpdatedMachineCount, pool.Status.ReadyMachineCount, pool.Status.MachineCount, pool.Spec.Configuration.Name)
	}
	return ctrl.updateMCOStatus(nodeConfig, nodeConfigCondition)
}

// updateMCOStatus updates the MCO specific status condition present in the node config CR
func (ctrl *Controller) updateMCOStatus(nodeConfig *osev1.Node, nodeConfigCondition metav1.Condition) error {
	var (
		index     int
		isPresent bool
	)
	var kubeletConditionTypes = []string{
		osev1.KubeletProgressing,
		osev1.KubeletComplete,
		osev1.KubeletDegraded,
	}
	for index = range nodeConfig.Status.WorkerLatencyProfileStatus.Conditions {
		for _, conditionType := range kubeletConditionTypes {
			if nodeConfig.Status.WorkerLatencyProfileStatus.Conditions[index].Type == conditionType {
				isPresent = true
				break
			}
		}
		if isPresent {
			break
		}
	}
	if isPresent {
		nodeConfig.Status.WorkerLatencyProfileStatus.Conditions[index] = *nodeConfigCondition.DeepCopy()
	} else {
		nodeConfig.Status.WorkerLatencyProfileStatus.Conditions = append(nodeConfig.Status.WorkerLatencyProfileStatus.Conditions, nodeConfigCondition)
	}
	_, err := ctrl.configClient.ConfigV1().Nodes().UpdateStatus(context.TODO(), nodeConfig, metav1.UpdateOptions{})
	return err
}

func RunNodeConfigBootstrap(templateDir string, features *osev1.FeatureGate, cconfig *mcfgv1.ControllerConfig, nodeConfig *osev1.Node, mcpPools []*mcfgv1.MachineConfigPool) ([]*mcfgv1.MachineConfig, error) {
	configs := []*mcfgv1.MachineConfig{}

	for _, pool := range mcpPools {
		role := pool.Name
		if role != ctrlcommon.MachineConfigPoolWorker {
			continue
		}
		// Get MachineConfig
		key, err := getManagedNodeConfigKey(pool, nil)
		if err != nil {
			return nil, err
		}
		ignConfig := ctrlcommon.NewIgnConfig()
		mc, err := ctrlcommon.MachineConfigFromIgnConfig(role, key, ignConfig)
		if err != nil {
			return nil, err
		}
		originalKubeConfig, err := generateOriginalKubeletConfigWithFeatureGates(cconfig, templateDir, role, features)
		if err != nil {
			return nil, err
		}
		// updating the kubelet configuration with the Node specific configuration.
		err = updateOriginalKubeConfigwithNodeConfig(nodeConfig, originalKubeConfig)
		if err != nil {
			return nil, err
		}
		// Encode the new config into raw JSON
		cfgIgn, err := kubeletConfigToIgnFile(originalKubeConfig)
		if err != nil {
			return nil, err
		}
		tempIgnConfig := ctrlcommon.NewIgnConfig()
		tempIgnConfig.Storage.Files = append(tempIgnConfig.Storage.Files, *cfgIgn)
		rawCfgIgn, err := json.Marshal(tempIgnConfig)
		if err != nil {
			return nil, err
		}
		mc.Spec.Config.Raw = rawCfgIgn
		mc.ObjectMeta.Annotations = map[string]string{
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
		}
		configs = append(configs, mc)
	}

	return configs, nil
}
