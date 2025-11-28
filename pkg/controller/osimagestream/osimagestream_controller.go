package osimagestream

import (
	"context"
	"fmt"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/version"
	corelisterv1 "k8s.io/client-go/listers/core/v1"

	configinformersv1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlisters "github.com/openshift/client-go/config/listers/config/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	mcfginformersv1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1"
	mcfginformersv1alpha1 "github.com/openshift/client-go/machineconfiguration/informers/externalversions/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	clientset "k8s.io/client-go/kubernetes"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"

	"k8s.io/klog/v2"
)

// Controller manages the OSImageStream singleton resource lifecycle.
type Controller struct {
	client     mcfgclientset.Interface
	kubeClient clientset.Interface

	ccLister             mcfglistersv1.ControllerConfigLister
	clusterVersionLister configlisters.ClusterVersionLister
	osImageStreamLister  mcfglistersv1alpha1.OSImageStreamLister
	cmLister             corelisterv1.ConfigMapLister

	cachesToSync []cache.InformerSynced
	bootedChan   chan error
	// osImageStream holds the OSImageStream resource after successful boot
	osImageStream *v1alpha1.OSImageStream

	imageStreamFactory ImageStreamFactory
}

// NewController creates a new OSImageStream controller.
func NewController(
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcoCmInformer coreinformersv1.ConfigMapInformer,
	osImageStreamInformer mcfginformersv1alpha1.OSImageStreamInformer,
	clusterVersionInformer configinformersv1.ClusterVersionInformer,
	imageStreamFactory ImageStreamFactory,
) *Controller {
	ctrl := &Controller{
		client:               mcfgClient,
		kubeClient:           kubeClient,
		ccLister:             ccInformer.Lister(),
		clusterVersionLister: clusterVersionInformer.Lister(),
		osImageStreamLister:  osImageStreamInformer.Lister(),
		cmLister:             mcoCmInformer.Lister(),
		cachesToSync: []cache.InformerSynced{
			ccInformer.Informer().HasSynced,
			mcoCmInformer.Informer().HasSynced,
			clusterVersionInformer.Informer().HasSynced,
			osImageStreamInformer.Informer().HasSynced,
		},
		bootedChan:         make(chan error, 1),
		imageStreamFactory: imageStreamFactory,
	}

	// Default to the "full/real" implementation if not factory was provided
	if imageStreamFactory == nil {
		ctrl.imageStreamFactory = NewDefaultStreamSourceFactory(ctrl.cmLister, &DefaultImagesInspectorFactory{})
	}

	return ctrl
}

// Run starts the controller and boots the OSImageStream resource.
func (ctrl *Controller) Run(stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()

	if !cache.WaitForCacheSync(stopCh, ctrl.cachesToSync...) {
		utilruntime.HandleError(fmt.Errorf("caches did not sync"))
		return
	}

	klog.Info("Starting MachineConfigController-OSImageStreamController")
	defer klog.Info("Shutting down MachineConfigController-OSImageStreamController")

	go func() {
		err := ctrl.boot()
		if err != nil {
			klog.Errorf("Error booting OSImageStreamController: %v", err)
		} else {
			klog.Infof(
				"OSImageStreamController booted successfully. Available streams: %s. Default stream: %s",
				GetStreamSetsNames(ctrl.osImageStream.Status.AvailableStreams),
				ctrl.osImageStream.Status.DefaultStream,
			)
		}

		ctrl.bootedChan <- err
	}()
	<-stopCh
}

// WaitBoot blocks until the boot process completes and returns any error encountered.
func (ctrl *Controller) WaitBoot() error {
	return <-ctrl.bootedChan
}

// boot initializes or updates the OSImageStream resource.
// It checks if an update is needed, fetches release images, and creates or updates the resource accordingly.
func (ctrl *Controller) boot() error {
	existingOSImageStream, err := ctrl.getExistingOSImageStream()
	if err != nil {
		return err
	}

	if !osImageStreamRequiresUpdate(existingOSImageStream) {
		klog.Info("Skipping OSImageStream boot: OSImageStream is already up-to-date")
		ctrl.osImageStream = existingOSImageStream
		return nil
	}

	image, err := GetReleasePayloadImage(ctrl.clusterVersionLister)
	if err != nil {
		return fmt.Errorf("error getting the Release Image digest from the ClusterVersion for the initial OSImageStream load: %w", err)
	}

	secret, cc, err := ctrl.getSysContextObjects()
	if err != nil {
		return fmt.Errorf("error getting the required dependencies for the initial OSImageStream load: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()
	osImageStream, err := BuildOsImageStreamRuntime(ctx, secret, cc, image, ctrl.imageStreamFactory)
	if err != nil {
		return fmt.Errorf("error building the OSImageStream at runtime: %w", err)
	}

	var updateOSImageStream *v1alpha1.OSImageStream
	if existingOSImageStream == nil {
		klog.V(4).Infof("Creating OSImageStream singleton instance as it doesn't exist")
		updateOSImageStream, err = ctrl.client.MachineconfigurationV1alpha1().OSImageStreams().Create(context.TODO(), osImageStream, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("error creating the OSImageStream at runtime: %w", err)
		}
	} else {
		oldVersion := existingOSImageStream.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey]
		klog.V(4).Infof("Updating the OSImageStream singleton as it was created by a previous version (%s). New version: %s", oldVersion, version.Hash)
		// Update metadata/spec first (mainly for annotations)
		existingOSImageStream.ObjectMeta.Annotations = osImageStream.ObjectMeta.Annotations
		updateOSImageStream, err = ctrl.client.MachineconfigurationV1alpha1().OSImageStreams().Update(context.TODO(), existingOSImageStream, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("error updating the OSImageStream at runtime: %w", err)
		}
	}

	// Update the status subresource (both for newly created and updated resources)
	updateOSImageStream.Status = osImageStream.Status
	if _, err = ctrl.client.
		MachineconfigurationV1alpha1().
		OSImageStreams().
		UpdateStatus(context.TODO(), updateOSImageStream, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("error updating the OSImageStream status at runtime: %w", err)
	}
	ctrl.osImageStream = updateOSImageStream
	return nil
}

// getExistingOSImageStream retrieves the existing OSImageStream from the lister.
// Returns nil if the OSImageStream does not exist.
func (ctrl *Controller) getExistingOSImageStream() (*v1alpha1.OSImageStream, error) {
	osImageStream, err := ctrl.osImageStreamLister.Get(ctrlcommon.ClusterInstanceNameOSImageStream)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to retrieve existing OSImageStream: %v", err)
		}
		return nil, nil
	}
	return osImageStream, nil
}

// osImageStreamRequiresUpdate checks if the OSImageStream needs to be created or updated.
// Returns true if osImageStream is nil or if its version annotation doesn't match the current version.
func osImageStreamRequiresUpdate(osImageStream *v1alpha1.OSImageStream) bool {
	if osImageStream == nil {
		return true
	}
	releaseVersion, ok := osImageStream.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey]
	return !ok || releaseVersion != version.Hash
}

func (ctrl *Controller) getSysContextObjects() (*corev1.Secret, *mcfgv1.ControllerConfig, error) {
	cc, err := ctrl.ccLister.Get(ctrlcommon.ControllerConfigName)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get ControllerConfig for OSImageStream initial load: %v", err)
	}

	clusterPullSecret, err := ctrl.kubeClient.CoreV1().Secrets(cc.Spec.PullSecret.Namespace).Get(context.TODO(), cc.Spec.PullSecret.Name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("could not get the cluster PullSecret for OSImageStream initial load: %v", err)
	}
	return clusterPullSecret, cc, nil
}
