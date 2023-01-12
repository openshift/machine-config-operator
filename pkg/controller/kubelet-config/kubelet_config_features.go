package kubeletconfig

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/clarketm/json"
	"github.com/golang/glog"
	osev1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/cloudprovider"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	// openshiftOnlyFeatureGates contains selection of featureGates which will be rejected by native kubelet
	openshiftOnlyFeatureGates = []string{
		cloudprovider.ExternalCloudProviderFeature,
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

	err := ctrl.syncFeatureHandler(key.(string))
	ctrl.handleFeatureErr(err, key)
	return true
}

func (ctrl *Controller) syncFeatureHandler(key string) error {
	startTime := time.Now()
	glog.V(4).Infof("Started syncing feature handler %q (%v)", key, startTime)
	defer func() {
		glog.V(4).Infof("Finished syncing feature handler %q (%v)", key, time.Since(startTime))
	}()

	// Fetch the Feature
	features, err := ctrl.featLister.Get(ctrlcommon.ClusterFeatureInstanceName)
	if errors.IsNotFound(err) {
		glog.V(2).Infof("FeatureSet %v is missing, using default", key)
		features = &osev1.FeatureGate{
			Spec: osev1.FeatureGateSpec{
				FeatureGateSelection: osev1.FeatureGateSelection{
					FeatureSet: osev1.Default,
				},
			},
		}
	} else if err != nil {
		return err
	}

	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("could not get ControllerConfig: %w", err)
	}

	// Find all MachineConfigPools
	mcpPools, err := ctrl.mcpLister.List(labels.Everything())
	if err != nil {
		return err
	}

	for _, pool := range mcpPools {
		var nodeConfig *osev1.Node
		role := pool.Name
		// Fetch the Node Config object
		nodeConfig, err = ctrl.nodeConfigLister.Get(ctrlcommon.ClusterNodeInstanceName)
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

		rawCfgIgn, err := generateKubeConfigIgnFromFeatures(cc, ctrl.templatesDir, role, features, nodeConfig)
		if err != nil {
			return err
		}
		if rawCfgIgn == nil {
			continue
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
			return fmt.Errorf("Could not Create/Update MachineConfig: %w", err)
		}
		glog.Infof("Applied FeatureSet %v on MachineConfigPool %v", key, pool.Name)
	}

	return nil
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
		glog.V(4).Infof("Update Feature %s", newFeature.Name)
		ctrl.enqueueFeature(newFeature)
	}
}

func (ctrl *Controller) addFeature(obj interface{}) {
	features := obj.(*osev1.FeatureGate)
	glog.V(4).Infof("Adding Feature %s", features.Name)
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
	glog.V(4).Infof("Deleted Feature %s and restored default config", features.Name)
}

// generateFeatureMap returns a map of enabled/disabled feature gate selection with exclusion list
//
//nolint:gocritic
func generateFeatureMap(features *osev1.FeatureGate, exclusions ...string) (*map[string]bool, error) {
	rv := make(map[string]bool)
	if features == nil {
		features = createNewDefaultFeatureGate()
	}
	set, ok := osev1.FeatureSets[features.Spec.FeatureSet]
	if !ok {
		return &rv, fmt.Errorf("enabled FeatureSet %v does not have a corresponding config", features.Spec.FeatureSet)
	}
	for _, featEnabled := range set.Enabled {
		rv[featEnabled] = true
	}
	for _, featDisabled := range set.Disabled {
		rv[featDisabled] = false
	}
	// The CustomNoUpgrade options will override our defaults. This is
	// expected behavior and can potentially break a cluster.
	if features.Spec.FeatureSet == osev1.CustomNoUpgrade && features.Spec.CustomNoUpgrade != nil {
		for _, featEnabled := range features.Spec.CustomNoUpgrade.Enabled {
			rv[featEnabled] = true
		}
		for _, featDisabled := range features.Spec.CustomNoUpgrade.Disabled {
			rv[featDisabled] = false
		}
	}

	// Remove features excluded due to being breaking for some reason
	for _, excluded := range exclusions {
		delete(rv, excluded)
	}
	return &rv, nil
}

func generateKubeConfigIgnFromFeatures(cc *mcfgv1.ControllerConfig, templatesDir, role string, features *osev1.FeatureGate, nodeConfig *osev1.Node) ([]byte, error) {
	originalKubeConfig, err := generateOriginalKubeletConfigWithFeatureGates(cc, templatesDir, role, features)
	if err != nil {
		return nil, err
	}
	if nodeConfig != nil {
		updateOriginalKubeConfigwithNodeConfig(nodeConfig, originalKubeConfig)
	}
	defaultFeatures, err := generateFeatureMap(createNewDefaultFeatureGate(), openshiftOnlyFeatureGates...)
	if err != nil {
		return nil, err
	}

	// Check to see if configured FeatureGates are equivalent to the Default FeatureSet.
	if reflect.DeepEqual(originalKubeConfig.FeatureGates, *defaultFeatures) {
		// When there is no difference, this isn't an error, but no machine config should be created
		return nil, nil
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

func RunFeatureGateBootstrap(templateDir string, features *osev1.FeatureGate, nodeConfig *osev1.Node, controllerConfig *mcfgv1.ControllerConfig, mcpPools []*mcfgv1.MachineConfigPool) ([]*mcfgv1.MachineConfig, error) {
	machineConfigs := []*mcfgv1.MachineConfig{}

	for _, pool := range mcpPools {
		role := pool.Name
		if nodeConfig == nil {
			nodeConfig = createNewDefaultNodeconfig()
		}
		rawCfgIgn, err := generateKubeConfigIgnFromFeatures(controllerConfig, templateDir, role, features, nodeConfig)
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
			ctrlcommon.GeneratedByControllerVersionAnnotationKey: version.Hash,
		}

		machineConfigs = append(machineConfigs, mc)
	}

	return machineConfigs, nil
}
