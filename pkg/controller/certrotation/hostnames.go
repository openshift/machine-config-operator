package certrotationcontroller

import (
	"context"
	"fmt"
	"net/url"

	configv1 "github.com/openshift/api/config/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
)

func (c *CertRotationController) syncHostnames() error {
	cfg, err := c.infraLister.Get("cluster")
	if err != nil {
		return fmt.Errorf("unable to get cluster infrastructure resource: %w", err)
	}

	serverIPs := getServerIPsFromInfra(cfg)

	if cfg.Status.APIServerInternalURL == "" {
		return fmt.Errorf("no APIServerInternalURL found in cluster infrastructure resource")
	}
	apiserverIntURL, err := url.Parse(cfg.Status.APIServerInternalURL)
	if err != nil {
		return fmt.Errorf("no APIServerInternalURL found in cluster infrastructure resource")
	}

	// Only attempt to get ARO cluster resource on Azure platform
	if cfg.Status.PlatformStatus != nil && cfg.Status.PlatformStatus.Type == configv1.AzurePlatformType {
		aroCluster, err := c.aroClient.AroV1alpha1().Clusters().Get(context.Background(), "cluster", metav1.GetOptions{})
		if err != nil {
			klog.Infof("ARO cluster resource not found or not accessible: %v", err)
		} else {
			klog.Infof("ARO cluster resource found w/ IPs: %s", aroCluster.Spec.APIIntIP)
			serverIPs = append(serverIPs, aroCluster.Spec.APIIntIP)
		}
	}

	hostnames := append([]string{apiserverIntURL.Hostname()}, serverIPs...)
	klog.Infof("syncing hostnames: %v", hostnames)
	c.hostnamesRotation.setHostnames(hostnames)
	return nil
}

func (c *CertRotationController) runHostnames() {
	for c.processHostnames() {
	}
}

func (c *CertRotationController) processHostnames() bool {
	dsKey, quit := c.hostnamesQueue.Get()
	if quit {
		return false
	}
	defer c.hostnamesQueue.Done(dsKey)

	err := c.syncHostnames()
	if err == nil {
		c.hostnamesQueue.Forget(dsKey)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("%v failed with : %v", dsKey, err))
	c.hostnamesQueue.AddRateLimited(dsKey)

	return true
}
