package daemon

import (
	"time"

	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const osImagePrecacheInterval = 2 * time.Minute

// runOSImagePrecache periodically downloads upcoming OS images while the node
// remains schedulable. See docs/OSImagePrecaching.md.
func (dn *Daemon) runOSImagePrecache(stopCh <-chan struct{}) {
	if dn.NodeUpdaterClient == nil {
		klog.V(4).Info("OS image precache disabled: node updater client unavailable")
		return
	}

	klog.Info("Starting OS image precache loop")
	workqueue.ParallelizeUntil(stopCh, 1, func(_ int) {
		ticker := time.NewTicker(osImagePrecacheInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				dn.syncOSImagePrecache()
			}
		}
	})
}

func (dn *Daemon) syncOSImagePrecache() {
	node, err := dn.nodeLister.Get(dn.name)
	if err != nil {
		klog.Warningf("OS image precache: failed to get node %s: %v", dn.name, err)
		return
	}

	desiredImage := node.Annotations[constants.DesiredImageAnnotationKey]
	currentImage := node.Annotations[constants.CurrentImageAnnotationKey]
	if desiredImage == "" || desiredImage == currentImage {
		return
	}

	bootedImage, _, _, err := dn.NodeUpdaterClient.GetBootedOSImageURL()
	if err != nil {
		klog.Warningf("OS image precache: failed to read booted OS image: %v", err)
		return
	}
	if desiredImage == bootedImage {
		return
	}

	desiredDrain := node.Annotations[constants.DesiredDrainerAnnotationKey]
	lastAppliedDrain := node.Annotations[constants.LastAppliedDrainerAnnotationKey]
	if desiredDrain != "" && desiredDrain != lastAppliedDrain {
		klog.V(4).Infof("OS image precache: skipping %s while drain is in progress", desiredImage)
		return
	}

	klog.Infof("OS image precache: downloading layers for %s", desiredImage)
	if err := dn.NodeUpdaterClient.DownloadOSImage(desiredImage); err != nil {
		klog.Warningf("OS image precache failed for %s: %v", desiredImage, err)
		return
	}
	klog.Infof("OS image precache completed for %s", desiredImage)
}
