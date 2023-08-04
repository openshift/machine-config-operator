package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

var (
	ccRequeueDelay = 1 * time.Minute
)

func (dn *Daemon) handleControllerConfigEvent(obj interface{}) {
	controllerConfig := obj.(*mcfgv1.ControllerConfig)
	klog.V(4).Infof("Updating ControllerConfig %s", controllerConfig.Name)
	dn.enqueueControllerConfig(controllerConfig)
}

func (dn *Daemon) enqueueControllerConfig(controllerConfig *mcfgv1.ControllerConfig) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(controllerConfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", controllerConfig, err))
		return
	}
	dn.ccQueue.AddRateLimited(key)
}

func (dn *Daemon) enqueueControllerConfigAfter(controllerConfig *mcfgv1.ControllerConfig, after time.Duration) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(controllerConfig)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %w", controllerConfig, err))
		return
	}

	dn.ccQueue.AddAfter(key, after)
}

func (dn *Daemon) controllerConfigWorker() {
	for dn.processNextControllerConfigWorkItem() {
	}
}

func (dn *Daemon) processNextControllerConfigWorkItem() bool {
	key, quit := dn.ccQueue.Get()
	if quit {
		return false
	}
	defer dn.ccQueue.Done(key)

	err := dn.syncControllerConfigHandler(key.(string))
	dn.handleControllerConfigErr(err, key)

	return true
}

func (dn *Daemon) handleControllerConfigErr(err error, key interface{}) {
	if err == nil {
		dn.ccQueue.Forget(key)
		return
	}

	if err := dn.updateErrorState(err); err != nil {
		klog.Errorf("Could not update annotation: %v", err)
	}
	// This is at V(2) since the updateErrorState() call above ends up logging too
	klog.V(2).Infof("Error syncing ControllerConfig %v (retries %d): %v", key, dn.ccQueue.NumRequeues(key), err)
	dn.ccQueue.AddRateLimited(key)
}

func (dn *Daemon) syncControllerConfigHandler(key string) error {
	startTime := time.Now()
	klog.V(4).Infof("Started syncing ControllerConfig %q (%v)", key, startTime)
	defer func() {
		klog.V(4).Infof("Finished syncing ControllerConfig %q (%v)", key, time.Since(startTime))
	}()

	if key != ctrlcommon.ControllerConfigName {
		// In theory there are no other ControllerConfigs other than the machine-config one
		// but to future-proof just in case, we don't need to sync on other changes
		return nil
	}

	controllerConfig, err := dn.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return fmt.Errorf("Could not get ControllerConfig: %v", err)
	}

	if dn.node == nil {
		// Node has not yet initialized, wait to resync
		dn.enqueueControllerConfigAfter(controllerConfig, ccRequeueDelay)
		return nil
	}

	// Write the latest cert to disk, if the controllerconfig resourceVersion has updated
	// Also annotate the latest config we've seen, so as to not write unnecessarily
	currentNodeControllerConfigResource := dn.node.Annotations[constants.ControllerConfigResourceVersionKey]

	if currentNodeControllerConfigResource != controllerConfig.ObjectMeta.ResourceVersion {
		kubeAPIServerServingCABytes := controllerConfig.Spec.KubeAPIServerServingCAData

		if err := writeFileAtomicallyWithDefaults(caBundleFilePath, kubeAPIServerServingCABytes); err != nil {
			return err
		}

		for _, CA := range controllerConfig.Spec.ImageRegistryBundleData {
			if err := os.MkdirAll(filepath.Join(imageCAFilePath, CA.File), defaultDirectoryPermissions); err != nil {
				return err
			}
			if err := writeFileAtomicallyWithDefaults(filepath.Join(imageCAFilePath, CA.File, "ca.crt"), CA.Data); err != nil {
				return err
			}
		}

		for _, CA := range controllerConfig.Spec.ImageRegistryBundleUserData {
			if err := os.MkdirAll(filepath.Join(imageCAFilePath, CA.File), defaultDirectoryPermissions); err != nil {
				return err
			}
			if err := writeFileAtomicallyWithDefaults(filepath.Join(imageCAFilePath, CA.File, "ca.crt"), CA.Data); err != nil {
				return err
			}
		}

		annos := map[string]string{
			constants.ControllerConfigResourceVersionKey: controllerConfig.ObjectMeta.ResourceVersion,
		}
		if _, err := dn.nodeWriter.SetAnnotations(annos); err != nil {
			return fmt.Errorf("failed to set ControllerConfigResourceVersion annotation on node: %w", err)
		}
		klog.Infof("Certificate was synced from controllerconfig resourceVersion %s", controllerConfig.ObjectMeta.ResourceVersion)
	}

	return nil
}
