//go:build !fcos && !scos

package operator

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"github.com/openshift/machine-config-operator/pkg/version"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/cri-api/pkg/errors"
	"k8s.io/klog/v2"
)

func (optr *Operator) syncOSImageStream(_ *renderConfig, _ *configv1.ClusterOperator) error {
	klog.V(4).Info("OSImageStream sync started")
	defer func() {
		klog.V(4).Info("OSImageStream sync complete")
	}()

	// This sync runs once per version. Before performing the streams fetching
	// process, that takes time as it requires inspecting images, ensure this function
	// needs to build the stream.
	existingOSImageStream, updateRequired, err := optr.isOSImageStreamBuildRequired()
	if !updateRequired || err != nil {
		return err
	}

	// If the code reaches this point the OSImageStream CR is not
	// present (new cluster) or it's out-dated (cluster update).
	// Build the new OSImageStream and push it.
	return optr.buildOSImageStream(existingOSImageStream)

}

func (optr *Operator) buildOSImageStream(existingOSImageStream *v1alpha1.OSImageStream) error {
	klog.Info("Starting building of the OSImageStream instance")

	// Get the release payload image from ClusterVersion
	image, err := osimagestream.GetReleasePayloadImage(optr.clusterVersionLister)
	if err != nil {
		return fmt.Errorf("error getting the Release Image digest from the ClusterVersion for OSImageStream sync: %w", err)
	}

	// Get the cluster pull secret from well-known location
	clusterPullSecret, err := optr.kubeClient.CoreV1().Secrets("openshift-config").Get(context.TODO(), "pull-secret", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get the cluster PullSecret for OSImageStream sync: %w", err)
	}

	// Build a minimal ControllerConfig with image registry certs and proxy settings
	// We can't use renderConfig (it runs after us) so we build it here directly
	minimalCC, err := optr.buildMinimalControllerConfigForOSImageStream()
	if err != nil {
		return fmt.Errorf("could not build minimal ControllerConfig for OSImageStream: %w", err)
	}

	// Build the OSImageStream using the default factory
	// Use a longer timeout to account for DNS/network delays during cluster bootstrap
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer buildCancel()

	sysCtxBuilder, err := optr.getSysContextBuilder(clusterPullSecret, minimalCC)
	if err != nil {
		return fmt.Errorf("could not build SysContext for OSImageStream build: %w", err)
	}

	imageStreamFactory := osimagestream.NewDefaultStreamSourceFactory(optr.mcoCmLister, &osimagestream.DefaultImagesInspectorFactory{})
	osImageStream, err := osimagestream.BuildOsImageStreamRuntime(buildCtx, sysCtxBuilder, image, imageStreamFactory)
	if err != nil {
		return fmt.Errorf("error building the OSImageStream: %w", err)
	}

	// Create or update the OSImageStream resource
	var updateOSImageStream *v1alpha1.OSImageStream
	if existingOSImageStream == nil {
		klog.V(4).Info("Creating OSImageStream singleton instance")
		updateOSImageStream, err = optr.client.MachineconfigurationV1alpha1().OSImageStreams().Create(context.TODO(), osImageStream, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("error creating the OSImageStream: %w", err)
		}
		klog.Infof("Created OSImageStream with %d available streams, default stream: %s",
			len(osImageStream.Status.AvailableStreams), osImageStream.Status.DefaultStream)
	} else {
		oldVersion := existingOSImageStream.Annotations[ctrlcommon.ReleaseImageVersionAnnotationKey]
		klog.V(4).Infof("Updating OSImageStream (previous version: %s, new version: %s)", oldVersion, version.Hash)
		// Update metadata/spec first (mainly for annotations)
		existingOSImageStream.ObjectMeta.Annotations = osImageStream.ObjectMeta.Annotations
		updateOSImageStream, err = optr.client.MachineconfigurationV1alpha1().OSImageStreams().Update(context.TODO(), existingOSImageStream, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("error updating the OSImageStream: %w", err)
		}
	}

	// Update the status subresource (both for newly created and updated resources)
	updateOSImageStream.Status = osImageStream.Status
	if _, err = optr.client.
		MachineconfigurationV1alpha1().
		OSImageStreams().
		UpdateStatus(context.TODO(), updateOSImageStream, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("error updating the OSImageStream status: %w", err)
	}

	klog.Infof("OSImageStream synced successfully. Available streams: %s. Default stream: %s",
		osimagestream.GetStreamSetsNames(updateOSImageStream.Status.AvailableStreams),
		updateOSImageStream.Status.DefaultStream)
	return nil
}

// getSysContextBuilder creates a configured SysContextBuilder for container image operations
// by combining cluster credentials, registry configuration, and mirror policies.
func (optr *Operator) getSysContextBuilder(clusterPullSecret *corev1.Secret, minimalCC *mcfgv1.ControllerConfig) (*imageutils.SysContextBuilder, error) {
	sysCtxBuilder := imageutils.NewSysContextBuilder().
		WithSecret(clusterPullSecret).
		WithControllerConfig(minimalCC)

	imageConfig, err := optr.imgLister.Get("cluster")
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("could not get image configuration: %w", err)
	}

	icspRules, err := optr.icspLister.List(labels.Everything())
	if err != nil && errors.IsNotFound(err) {
		icspRules = []*apioperatorsv1alpha1.ImageContentSourcePolicy{}
	} else if err != nil {
		return nil, fmt.Errorf("could not get ICSP rules: %w", err)
	}

	// Find all ImageDigestMirrorSet objects
	idmsRules, err := optr.idmsLister.List(labels.Everything())
	if err != nil && errors.IsNotFound(err) {
		idmsRules = []*configv1.ImageDigestMirrorSet{}
	} else if err != nil {
		return nil, fmt.Errorf("could not get IDMS rules: %w", err)
	}

	// Find all ImageTagMirrorSet objects
	itmsRules, err := optr.itmsLister.List(labels.Everything())
	if err != nil && errors.IsNotFound(err) {
		itmsRules = []*configv1.ImageTagMirrorSet{}
	} else if err != nil {
		return nil, fmt.Errorf("could not get ITMS rules: %w", err)
	}

	// Add the registries config to the builder only if there are rules applied
	if imageConfig != nil || len(icspRules) != 0 || len(idmsRules) != 0 || len(itmsRules) != 0 {
		registriesConf, err := imageutils.GenerateRegistriesConfig(imageConfig, icspRules, idmsRules, itmsRules)
		if err != nil {
			return nil, fmt.Errorf("could not build registries configuration: %w", err)
		}
		sysCtxBuilder.WithRegistriesConfig(registriesConf)
	}

	return sysCtxBuilder, nil
}

func (optr *Operator) isOSImageStreamBuildRequired() (*v1alpha1.OSImageStream, bool, error) {
	// Check if the feature is enabled
	if !osimagestream.IsFeatureEnabled(optr.fgHandler) {
		klog.V(4).Info("OSImageStream feature is not enabled, skipping sync")
		return nil, false, nil
	}

	// Get the existing OSImageStream if it exists
	existingOSImageStream, err := optr.getExistingOSImageStream()
	if err != nil {
		return nil, true, err
	}

	// Check if an update is needed
	if !osImageStreamRequiresUpdate(existingOSImageStream) {
		klog.V(4).Info("OSImageStream is already up-to-date, skipping sync")
		return nil, false, nil
	}
	return existingOSImageStream, true, nil
}

// buildMinimalControllerConfigForOSImageStream builds a minimal ControllerConfig with the bare minimum for the
// osimagestream package to build an image inspection environment. The bare minimum consists of the image registry certs
// and the proxy configuration, if present.
// This is necessary because OSImageStream must run before RenderConfig.
func (optr *Operator) buildMinimalControllerConfigForOSImageStream() (*mcfgv1.ControllerConfig, error) {
	imgRegistryData, imgRegistryUsrData, err := optr.getImageRegistryBundles()
	if err != nil {
		return nil, fmt.Errorf("could not get image registry bundles: %w", err)
	}

	cc := &mcfgv1.ControllerConfig{
		Spec: mcfgv1.ControllerConfigSpec{
			ImageRegistryBundleData:     imgRegistryData,
			ImageRegistryBundleUserData: imgRegistryUsrData,
		},
	}

	// Optional: If the cluster has a proxy configured consume it, as image inspection
	// may need to go through the proxy too.
	proxy, err := optr.proxyLister.Get("cluster")
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("could not get proxy configuration: %w", err)
	}
	if proxy != nil {
		cc.Spec.Proxy = &proxy.Status
	}

	trustBundle, err := optr.getTrustedBundle(proxy)
	if err != nil {
		return nil, fmt.Errorf("could not get trusted CA bundle: %w", err)
	}
	cc.Spec.AdditionalTrustBundle = trustBundle

	return cc, nil
}

// getExistingOSImageStream retrieves the existing OSImageStream from the lister.
// Returns nil if the OSImageStream does not exist.
func (optr *Operator) getExistingOSImageStream() (*v1alpha1.OSImageStream, error) {
	osImageStream, err := optr.osImageStreamLister.Get(ctrlcommon.ClusterInstanceNameOSImageStream)
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
