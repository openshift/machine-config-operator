package buildrequest

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/distribution/reference"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// Holds all of the options used to produce a BuildRequest.
type BuildRequestOpts struct { //nolint:revive // This name is fine.
	MachineOSConfig  *mcfgv1alpha1.MachineOSConfig
	MachineOSBuild   *mcfgv1alpha1.MachineOSBuild
	MachineConfig    *mcfgv1.MachineConfig
	Images           *ctrlcommon.Images
	OSImageURLConfig *ctrlcommon.OSImageURLConfig

	BaseImagePullSecret  *corev1.Secret
	FinalImagePushSecret *corev1.Secret

	// Has /etc/pki/entitlement
	HasEtcPkiEntitlementKeys bool
	// Has /etc/yum.repos.d configs
	HasEtcYumReposDConfigs bool
	// Has /etc/pki/rpm-gpg configs
	HasEtcPkiRpmGpgKeys bool
}

// Gets the extensions image pullspec from the MachineOSConfig if available.
// Otherwise, it defaults to the value from the osimageurl ConfigMap.
func (b BuildRequestOpts) getExtensionsImagePullspec() string {
	if b.MachineOSConfig.Spec.BuildInputs.BaseOSExtensionsImagePullspec != "" {
		return b.MachineOSConfig.Spec.BuildInputs.BaseOSExtensionsImagePullspec
	}

	return b.OSImageURLConfig.BaseOSExtensionsContainerImage
}

// Gets the base OS image pullspec from the MachineOSConfig if available.
// Otherwise, it defaults to the value from the osimageurl ConfigMap.
func (b BuildRequestOpts) getBaseOSImagePullspec() string {
	if b.MachineOSConfig.Spec.BuildInputs.BaseOSImagePullspec != "" {
		return b.MachineOSConfig.Spec.BuildInputs.BaseOSImagePullspec
	}

	return b.OSImageURLConfig.BaseOSContainerImage
}

// Gets the release version value from the MachineOSConfig if available.
// Otherwise, it defaults to the value from the osimageurl ConfigMap.
func (b BuildRequestOpts) getReleaseVersion() string {
	if b.MachineOSConfig.Spec.BuildInputs.ReleaseVersion != "" {
		return b.MachineOSConfig.Spec.BuildInputs.ReleaseVersion
	}

	return b.OSImageURLConfig.ReleaseVersion
}

// Gets all of the image build request opts from the Kube API server.
func newBuildRequestOptsFromAPI(ctx context.Context, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) (*BuildRequestOpts, error) {
	og := optsGetter{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
	}

	return og.getOpts(ctx, mosb, mosc)
}

// Holds all of the private methods used to populate the BuildRequestOpts
// fields from the Kube API server.
type optsGetter struct {
	kubeclient clientset.Interface
	mcfgclient mcfgclientset.Interface
}

// TODO: Deduplicate this.
func (o *optsGetter) validateMachineOSConfig(mosc *mcfgv1alpha1.MachineOSConfig) error {
	if mosc.Spec.BuildInputs.BaseImagePullSecret.Name == "" {
		return fmt.Errorf("baseImagePullSecret empty for MachineOSConfig %s", mosc.Name)
	}

	if mosc.Spec.BuildInputs.RenderedImagePushSecret.Name == "" {
		return fmt.Errorf("renderedImagePushSecret empty for MachineOSConfig %s", mosc.Name)
	}

	if mosc.Spec.BuildInputs.RenderedImagePushspec == "" {
		return fmt.Errorf("renderedImagePushspec empty for MachineOSConfig %s", mosc.Name)
	}

	if _, err := reference.ParseNamed(mosc.Spec.BuildInputs.RenderedImagePushspec); err != nil {
		return fmt.Errorf("invalid renderedImagePushspec for MachineOSConfig %s: %w", mosc.Name, err)
	}

	return nil
}

// Validates that the required fields on a MachineOSBuild are set before beginning the build.
func (o *optsGetter) validateMachineOSBuild(mosb *mcfgv1alpha1.MachineOSBuild) error {
	if mosb.Spec.DesiredConfig.Name == "" {
		return fmt.Errorf("desiredConfig.name empty for MachineOSBuild %s", mosb.Name)
	}

	return nil
}

// Gets the BuildRequestOpts after making API queries to get all of the necessary info required.
func (o *optsGetter) getOpts(ctx context.Context, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) (*BuildRequestOpts, error) {
	if err := o.validateMachineOSConfig(mosc); err != nil {
		return nil, err
	}

	if err := o.validateMachineOSBuild(mosb); err != nil {
		return nil, err
	}

	opts, err := o.resolveEntitlements(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve entitlements for MachineOSBuild %s", mosb.Name)
	}

	imagesConfig, err := ctrlcommon.GetImagesConfig(ctx, o.kubeclient)
	if err != nil {
		return nil, fmt.Errorf("could not get images.json config: %w", err)
	}

	osImageURLConfig, err := ctrlcommon.GetOSImageURLConfig(ctx, o.kubeclient)
	if err != nil {
		return nil, fmt.Errorf("could not get osImageURL config: %w", err)
	}

	baseImagePullSecret, err := o.getValidatedSecret(ctx, mosc.Spec.BuildInputs.BaseImagePullSecret.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get base image pull secret %s: %w", mosc.Spec.BuildInputs.BaseImagePullSecret.Name, err)
	}

	finalImagePushSecret, err := o.getValidatedSecret(ctx, mosc.Spec.BuildInputs.RenderedImagePushSecret.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get final image push secret %s: %w", mosc.Spec.BuildInputs.RenderedImagePushSecret.Name, err)
	}

	mc, err := o.mcfgclient.MachineconfigurationV1().MachineConfigs().Get(ctx, mosb.Spec.DesiredConfig.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not retrieve machineconfig %s: %w", mosb.Spec.DesiredConfig.Name, err)
	}

	opts.Images = imagesConfig
	opts.MachineConfig = mc
	opts.OSImageURLConfig = osImageURLConfig
	opts.BaseImagePullSecret = baseImagePullSecret
	opts.FinalImagePushSecret = finalImagePushSecret
	opts.MachineOSConfig = mosc.DeepCopy()
	opts.MachineOSBuild = mosb.DeepCopy()

	return opts, nil
}

// Gets an image pull secret and validates that it is usable.
func (o *optsGetter) getValidatedSecret(ctx context.Context, name string) (*corev1.Secret, error) {
	secret, err := o.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not fetch secret %s: %w", name, err)
	}

	if err := utils.ValidatePullSecret(secret); err != nil {
		return nil, fmt.Errorf("could not validate secret %s: %w", name, err)
	}

	return secret, nil
}

// Determines whether the build makes use of entitlements based upon the
// presence (or lack thereof) of specific configmaps and secrets.
func (o *optsGetter) resolveEntitlements(ctx context.Context) (*BuildRequestOpts, error) {
	opts := &BuildRequestOpts{}

	etcPkiEntitlements, err := o.getOptionalSecret(ctx, constants.EtcPkiEntitlementSecretName)
	if err != nil {
		return nil, err
	}

	opts.HasEtcPkiEntitlementKeys = etcPkiEntitlements != nil

	etcPkiRpmGpgKeys, err := o.getOptionalSecret(ctx, constants.EtcPkiRpmGpgSecretName)
	if err != nil {
		return nil, err
	}

	opts.HasEtcPkiRpmGpgKeys = etcPkiRpmGpgKeys != nil

	etcYumReposDConfigs, err := o.getOptionalConfigMap(ctx, constants.EtcYumReposDConfigMapName)
	if err != nil {
		return nil, err
	}

	opts.HasEtcYumReposDConfigs = etcYumReposDConfigs != nil

	return opts, nil
}

// Fetches an optional secret to inject into the build. Returns a nil error if
// the secret is not found.
func (o *optsGetter) getOptionalSecret(ctx context.Context, secretName string) (*corev1.Secret, error) {
	optionalSecret, err := o.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err == nil {
		klog.Infof("Optional build secret %q found, will include in build", secretName)
		return optionalSecret, nil
	}

	if k8serrors.IsNotFound(err) {
		klog.Infof("Could not find optional secret %q, will not include in build", secretName)
		return nil, nil
	}

	return nil, fmt.Errorf("could not retrieve optional secret: %s: %w", secretName, err)
}

// Fetches an optional ConfigMap to inject into the build. Returns a nil error if
// the ConfigMap is not found.
func (o *optsGetter) getOptionalConfigMap(ctx context.Context, configmapName string) (*corev1.ConfigMap, error) {
	optionalConfigMap, err := o.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, configmapName, metav1.GetOptions{})
	if err == nil {
		klog.Infof("Optional build ConfigMap %q found, will include in build", configmapName)
		return optionalConfigMap, nil
	}

	if k8serrors.IsNotFound(err) {
		klog.Infof("Could not find ConfigMap %q, will not include in build", configmapName)
		return nil, nil
	}

	return nil, fmt.Errorf("could not retrieve optional ConfigMap: %s: %w", configmapName, err)
}
