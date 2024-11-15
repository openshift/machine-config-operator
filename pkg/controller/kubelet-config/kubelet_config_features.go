package kubeletconfig

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/clarketm/json"
	osev1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	// openshiftOnlyFeatureGates contains selection of featureGates which will be rejected by native kubelet
	openshiftOnlyFeatureGates = []osev1.FeatureGateName{
		// TODO(jkyros): OpenShift carries a patch that makes kubelet/apiserver ignore feature gates it doesn't
		// understand, so we don't have to filter these out anymore, but we really need to come back and take the
		// hardcoded featureGates out of the kubelet.yaml template and inject them from the feature gate
		// accessor instead so we don't have to worry about keeping all the lists manually in sync.
	}
)

func (ctrl *Controller) featureWorker() {
	for ctrl.processNextFeatureWorkItem() {
	}
}

func (ctrl *Controller) processNextFeatureWorkItem() bool {
	key, quit := ctrl.featureQueue.Get()
	if quit {
		return false
	}
	defer ctrl.featureQueue.Done(key)

	err := ctrl.syncFeatureHandler(key)
	ctrl.handleFeatureErr(err, key)
	return true
}

func (ctrl *Controller) syncFeatureHandler(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing feature handler %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing feature handler %q (%v)", key, time.Since(startTime))
	}()

	cc, err := ctrl.ccLister.Get(commonconsts.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig: %w", err)
	}

	// Find all MachineConfigPools
	mcpPools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}

	// Grab APIServer to populate TLS settings in the default kubelet config
	apiServer, err := ctrl.apiserverLister.Get(commonconsts.APIServerInstanceName)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("could not get the TLSSecurityProfile from %v: %v", commonconsts.APIServerInstanceName, err)
	}

	for _, pool := range mcpPools {
		var nodeConfig *osev1.Node
		role := pool.Name
		// Fetch the Node Config object
		nodeConfig, err = ctrl.nodeConfigLister.Get(commonconsts.ClusterNodeInstanceName)
		if errors.IsNotFound(err) {
			nodeConfig = createNewDefaultNodeconfig()
		}
		// Get MachineConfig
		managedKey, err := getManagedFeaturesKey(pool, ctrl.client)
		if err != nil {
			return err
		}
		mc, err := ctrl.client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		isNotFound := errors.IsNotFound(err)
		if isNotFound {
			ignConfig := ctrlcommon.NewIgnConfig()
			mc, err = ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, ignConfig)
			if err != nil {
				return err
			}
		}

		rawCfgIgn, err := generateKubeConfigIgnFromFeatures(cc, ctrl.templatesDir, role, ctrl.featureGateAccess, nodeConfig, apiServer)
		if err != nil {
			return err
		}
		if rawCfgIgn == nil {
			continue
		}

		mc.Spec.Config.Raw = rawCfgIgn
		mc.ObjectMeta.Annotations = map[string]string{
			commonconsts.GeneratedByControllerVersionAnnotationKey: version.Hash,
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
			return fmt.Errorf("could not Create/Update MachineConfig: %w", err)
		}
		klog.Infof("Applied FeatureSet %v on MachineConfigPool %v", key, pool.Name)
		ctrlcommon.UpdateStateMetric(ctrlcommon.MCCSubControllerState, "machine-config-controller-kubelet-config", "Sync FeatureSet", pool.Name)
	}
	return ctrl.cleanUpDuplicatedMC(managedFeaturesKeyPrefix)

}

func (ctrl *Controller) enqueueFeature(feat *osev1.FeatureGate) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(feat)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %w", feat, err))
		return
	}
	ctrl.featureQueue.Add(key)
}

func (ctrl *Controller) updateFeature(old, cur interface{}) {
	oldFeature := old.(*osev1.FeatureGate)
	newFeature := cur.(*osev1.FeatureGate)
	if !reflect.DeepEqual(oldFeature.Spec, newFeature.Spec) {
		klog.V(4).Infof("Update Feature %s", newFeature.Name)
		ctrl.enqueueFeature(newFeature)
	}
}

func (ctrl *Controller) addFeature(obj interface{}) {
	features := obj.(*osev1.FeatureGate)
	klog.V(4).Infof("Adding Feature %s", features.Name)
	ctrl.enqueueFeature(features)
}

func (ctrl *Controller) deleteFeature(obj interface{}) {
	features, ok := obj.(*osev1.FeatureGate)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		features, ok = tombstone.Obj.(*osev1.FeatureGate)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a KubeletConfig %#v", obj))
			return
		}
	}
	klog.V(4).Infof("Deleted Feature %s and restored default config", features.Name)
}

// generateFeatureMap returns a map of enabled/disabled feature gate selection with exclusion list
//
//nolint:gocritic
func generateFeatureMap(featuregateAccess featuregates.FeatureGateAccess, exclusions ...osev1.FeatureGateName) (*map[string]bool, error) {
	rv := make(map[string]bool)
	if !featuregateAccess.AreInitialFeatureGatesObserved() {
		return nil, fmt.Errorf("initial feature gates are not observed")
	}

	features, err := featuregateAccess.CurrentFeatureGates()
	if err != nil {
		return nil, fmt.Errorf("could not get current feature gates: %w", err)
	}

	for _, feat := range features.KnownFeatures() {
		if features.Enabled(feat) {
			rv[string(feat)] = true
		} else {
			rv[string(feat)] = false
		}
	}

	// Remove features excluded due to being breaking for some reason
	for _, excluded := range exclusions {
		delete(rv, string(excluded))
	}
	return &rv, nil
}

func generateKubeConfigIgnFromFeatures(cc *mcfgv1.ControllerConfig, templatesDir, role string, featureGateAccess featuregates.FeatureGateAccess, nodeConfig *osev1.Node, apiServer *osev1.APIServer) ([]byte, error) {
	originalKubeConfig, err := generateOriginalKubeletConfigWithFeatureGates(cc, templatesDir, role, featureGateAccess, apiServer)
	if err != nil {
		return nil, err
	}
	if nodeConfig != nil && role == commonconsts.MachineConfigPoolWorker {
		updateOriginalKubeConfigwithNodeConfig(nodeConfig, originalKubeConfig)
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
	return rawCfgIgn, nil
}

func RunFeatureGateBootstrap(templateDir string, featureGateAccess featuregates.FeatureGateAccess, nodeConfig *osev1.Node, controllerConfig *mcfgv1.ControllerConfig, mcpPools []*mcfgv1.MachineConfigPool, apiServer *osev1.APIServer) ([]*mcfgv1.MachineConfig, error) {
	machineConfigs := []*mcfgv1.MachineConfig{}

	for _, pool := range mcpPools {
		role := pool.Name
		if nodeConfig == nil {
			nodeConfig = createNewDefaultNodeconfig()
		}
		rawCfgIgn, err := generateKubeConfigIgnFromFeatures(controllerConfig, templateDir, role, featureGateAccess, nodeConfig, apiServer)
		if err != nil {
			return nil, err
		}
		if rawCfgIgn == nil {
			continue
		}

		// Get MachineConfig
		managedKey, err := getManagedFeaturesKey(pool, nil)
		if err != nil {
			return nil, err
		}

		ignConfig := ctrlcommon.NewIgnConfig()
		mc, err := ctrlcommon.MachineConfigFromIgnConfig(role, managedKey, ignConfig)
		if err != nil {
			return nil, err
		}

		mc.Spec.Config.Raw = rawCfgIgn
		mc.ObjectMeta.Annotations = map[string]string{
			commonconsts.GeneratedByControllerVersionAnnotationKey: version.Hash,
		}

		machineConfigs = append(machineConfigs, mc)
	}

	return machineConfigs, nil
}
