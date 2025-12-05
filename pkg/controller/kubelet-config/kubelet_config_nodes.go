package kubeletconfig

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/clarketm/json"
	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/controller/osimagestream"
	"github.com/openshift/machine-config-operator/pkg/version"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
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

	err := ctrl.syncNodeConfigHandler(key)
	ctrl.handleNodeConfigErr(err, key)
	return true
}

func (ctrl *Controller) handleNodeConfigErr(err error, key string) {
	if err == nil {
		ctrl.nodeConfigQueue.Forget(key)
		return
	}

	if ctrl.nodeConfigQueue.NumRequeues(key) < maxRetries {
		klog.V(4).Infof("Error syncing node configuration %v: %v", key, err)
		ctrl.nodeConfigQueue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	klog.V(2).Infof("Dropping node config %q out of the queue: %v", key, err)
	ctrl.nodeConfigQueue.Forget(key)
	ctrl.nodeConfigQueue.AddAfter(key, 1*time.Minute)
}

// syncNodeConfigHandler syncs whenever there is a change on the nodes.config.openshift.io resource
// nodes.config.openshift.io object holds the cluster-wide information about the
// node specific features such as cgroup modes, workerlatencyprofiles, etc.
func (ctrl *Controller) syncNodeConfigHandler(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing nodeConfig handler %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing nodeConfig handler %q (%v)", key, time.Since(startTime))
	}()

	// Fetch the Node
	nodeConfig, err := getConfigNode(ctrl, key)
	if err != nil {
		err := fmt.Errorf("could not fetch Node: %w", err)
		return err
	}
	if err := ctrl.cleanUpDuplicatedMC(managedNodeConfigKeyPrefix); err != nil {
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

	// Grab APIServer to populate TLS settings in the default kubelet config
	apiServer, err := ctrl.apiserverLister.Get(ctrlcommon.APIServerInstanceName)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("could not get the TLSSecurityProfile from %v: %v", ctrlcommon.APIServerInstanceName, err)
	}

	var osImageStream *v1alpha1.OSImageStream
	if ctrl.osImageStreamLister != nil {
		osImageStream, err = ctrl.osImageStreamLister.Get(ctrlcommon.ClusterInstanceNameOSImageStream)
		// TODO @pablintino For now consider the situation where no OSImageStreams are available
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("could not get OSImageStream, err: %w", err)
		}
	}

	for _, pool := range mcpPools {
		role := pool.Name
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

		originalKubeConfig, err := generateOriginalKubeletConfigWithFeatureGates(
			cc,
			ctrl.templatesDir,
			role,
			ctrl.fgHandler,
			apiServer,
			osimagestream.TryGetOSImageStreamFromPoolListByPoolName(osImageStream, mcpPools, pool.Name),
		)
		if err != nil {
			return err
		}
		// the workerlatencyprofile's configuration change will be applied only on the worker nodes.
		if role == ctrlcommon.MachineConfigPoolWorker {
			// updating the kubelet configuration with the Node specific configuration.
			err = updateOriginalKubeConfigwithNodeConfig(nodeConfig, originalKubeConfig)
			if err != nil {
				return err
			}
		}
		// The following code updates the MC with the relevant CGroups version
		switch role {
		case ctrlcommon.MachineConfigPoolWorker, ctrlcommon.MachineConfigPoolMaster, ctrlcommon.MachineConfigPoolArbiter:
			err = updateMachineConfigwithCgroup(nodeConfig, mc)
			if err != nil {
				return err
			}
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
		klog.Infof("Applied Node configuration %v on MachineConfigPool %v", key, pool.Name)
		ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-kubelet-config", "Sync NodeConfig", pool.Name)
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

	// syncing the featuregate controller
	features, err := ctrl.featLister.Get(ctrlcommon.ClusterFeatureInstanceName)
	if err == nil {
		err := ctrl.syncFeatureHandler(features.Name)
		if err != nil {
			return fmt.Errorf("could not sync featuregate controller %v, err: %w", features, err)
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
	isValidWorkerLatencyProfleTransition := true
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
		ctrl.eventRecorder.Eventf(oldNode, corev1.EventTypeNormal, "ActionProhibited", message)
		klog.V(2).Infof("%s", message)
		return
	}
	if !reflect.DeepEqual(oldNode.Spec, newNode.Spec) {
		// skipping the update in case of the Worker-Latency-Profile type transition from "Default" to "LowUpdateSlowReaction" and vice-versa
		// (TODO) Ideally the user request has to be honoured, the transition need to be from Default -> Medium -> Low or vice-versa.
		// Restricting the request for now until this process is automated in future.
		if oldNode.Spec.WorkerLatencyProfile == osev1.LowUpdateSlowReaction {
			if newNode.Spec.WorkerLatencyProfile == osev1.DefaultUpdateDefaultReaction || newNode.Spec.WorkerLatencyProfile == "" {
				isValidWorkerLatencyProfleTransition = false
			}
		}
		if newNode.Spec.WorkerLatencyProfile == osev1.LowUpdateSlowReaction {
			if oldNode.Spec.WorkerLatencyProfile == osev1.DefaultUpdateDefaultReaction || oldNode.Spec.WorkerLatencyProfile == "" {
				isValidWorkerLatencyProfleTransition = false
			}
		}
		if !isValidWorkerLatencyProfleTransition {
			message := fmt.Sprintf("Skipping the Update Node event, name: %s, transition not allowed from old WorkerLatencyProfile: \"%s\" to new WorkerLatencyProfile: \"%s\"", newNode.Name, oldNode.Spec.WorkerLatencyProfile, newNode.Spec.WorkerLatencyProfile)
			ctrl.eventRecorder.Eventf(newNode, corev1.EventTypeNormal, "ActionProhibited", message)
			klog.Info(message)
			return
		}
		klog.V(4).Infof("Updating the node config resource, name: %s", newNode.Name)
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
		klog.V(2).Info(message)
		ctrl.eventRecorder.Eventf(nodeConfig, corev1.EventTypeNormal, "ActionProhibited", message)
		return
	}
	klog.V(4).Infof("Adding the node config resource, name: %s", nodeConfig.Name)
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
	klog.V(4).Infof("Deleted node config %s and restored default config", nodeConfig.Name)
}

func RunNodeConfigBootstrap(templateDir string, fgHandler ctrlcommon.FeatureGatesHandler, cconfig *mcfgv1.ControllerConfig, nodeConfig *osev1.Node, mcpPools []*mcfgv1.MachineConfigPool, apiServer *osev1.APIServer, osImageStream *v1alpha1.OSImageStream) ([]*mcfgv1.MachineConfig, error) {
	if nodeConfig == nil {
		return nil, fmt.Errorf("nodes.config.openshift.io resource not found")
	}

	configs := []*mcfgv1.MachineConfig{}

	for _, pool := range mcpPools {
		role := pool.Name
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
		originalKubeConfig, err := generateOriginalKubeletConfigWithFeatureGates(
			cconfig, templateDir, role, fgHandler, apiServer,
			osimagestream.TryGetOSImageStreamFromPoolListByPoolName(osImageStream, mcpPools, pool.Name),
		)
		if err != nil {
			return nil, err
		}
		if role == ctrlcommon.MachineConfigPoolWorker {
			// updating the kubelet configuration with the Node specific configuration.
			err = updateOriginalKubeConfigwithNodeConfig(nodeConfig, originalKubeConfig)
			if err != nil {
				return nil, err
			}
		}
		// The following code updates the MC with the relevant CGroups version
		err = updateMachineConfigwithCgroup(nodeConfig, mc)
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
