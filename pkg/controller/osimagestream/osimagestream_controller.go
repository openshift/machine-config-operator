package osimagestream

import (
	"context"
	"fmt"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/version"
	"k8s.io/apimachinery/pkg/api/errors"

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

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"k8s.io/klog/v2"
)

type Controller struct {
	client     mcfgclientset.Interface
	kubeClient clientset.Interface

	syncHandler func(mcp string) error

	ccLister             mcfglistersv1.ControllerConfigLister
	cmLister             corelisterv1.ConfigMapLister
	clusterVersionLister configlisters.ClusterVersionLister
	osImageStreamLister  mcfglistersv1alpha1.OSImageStreamLister

	cachesToSync []cache.InformerSynced
	bootedChan   chan error
}

func NewController(
	kubeClient clientset.Interface,
	mcfgClient mcfgclientset.Interface,
	ccInformer mcfginformersv1.ControllerConfigInformer,
	mcoCmInformer coreinformersv1.ConfigMapInformer,
	osImageStreamInformer mcfginformersv1alpha1.OSImageStreamInformer,

	clusterVersionInformer configinformersv1.ClusterVersionInformer,
) *Controller {
	ctrl := &Controller{
		client:               mcfgClient,
		kubeClient:           kubeClient,
		ccLister:             ccInformer.Lister(),
		cmLister:             mcoCmInformer.Lister(),
		clusterVersionLister: clusterVersionInformer.Lister(),
		osImageStreamLister:  osImageStreamInformer.Lister(),
		cachesToSync: []cache.InformerSynced{
			ccInformer.Informer().HasSynced,
			mcoCmInformer.Informer().HasSynced,
			clusterVersionInformer.Informer().HasSynced,
			osImageStreamInformer.Informer().HasSynced,
		},
		bootedChan: make(chan error, 1),
	}

	return ctrl
}

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
		}
		ctrl.bootedChan <- err
	}()
	<-stopCh
}

func (ctrl *Controller) WaitBoot() error {
	return <-ctrl.bootedChan
}

func (ctrl *Controller) boot() error {
	requiresUpdate, requiresCreate, err := ctrl.osImageStreamNeedsUpdate()
	if err != nil {
		return err
	}

	if !requiresUpdate {
		klog.Info("Skipping OSImageStream boot: OSImageStream is already up-to-date")
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
	osImageStream, err := BuildOsImageStreamRuntime(ctx, secret, cc, image, NewDefaultStreamSourceFactory(ctrl.cmLister, &DefaultImagesInspectorFactory{}))
	if err != nil {
		return fmt.Errorf("error building the OSImageStream at runtime: %w", err)
	}

	if requiresCreate {
		if _, err = ctrl.client.MachineconfigurationV1alpha1().OSImageStreams().Create(context.TODO(), osImageStream, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("error creating the OSImageStream at runtime: %w", err)
		}
	} else {
		if _, err = ctrl.client.
			MachineconfigurationV1alpha1().
			OSImageStreams().
			UpdateStatus(context.TODO(), osImageStream, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("error updating the OSImageStream at runtime: %w", err)
		}
	}

	return nil
}

func (ctrl *Controller) osImageStreamNeedsUpdate() (bool, bool, error) {
	osImageStream, err := ctrl.osImageStreamLister.Get(ClusterInstanceNameOSImageStream)
	if err != nil {
		if !errors.IsNotFound(err) {
			return false, false, fmt.Errorf("failed to retrieve existing OSImageStream: %v", err)
		}
		return true, true, nil
	}

	// If the resource was generated with the same controller version do not parse the streams again
	releaseVersion, ok := osImageStream.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey]
	if ok && releaseVersion == version.Hash {
		return false, false, nil
	}
	return true, false, nil
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
