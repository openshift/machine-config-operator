package buildrequest

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/distribution/reference"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// Holds all of the options used to produce a BuildRequest.
type BuildRequestOpts struct { //nolint:revive // This name is fine.
	MachineOSConfig  *mcfgv1.MachineOSConfig
	MachineOSBuild   *mcfgv1.MachineOSBuild
	MachineConfig    *mcfgv1.MachineConfig
	Images           *ctrlcommon.Images
	OSImageURLConfig *ctrlcommon.OSImageURLConfig

	BaseImagePullSecret  *corev1.Secret
	FinalImagePushSecret *corev1.Secret

	// Has user defined base image pull secret
	hasUserDefinedBaseImagePullSecret bool
	// Has /etc/pki/entitlement
	HasEtcPkiEntitlementKeys bool
	// Has /etc/yum.repos.d configs
	HasEtcYumReposDConfigs bool
	// Has /etc/pki/rpm-gpg configs
	HasEtcPkiRpmGpgKeys bool

	// Proxy Configurations
	Proxy *configv1.ProxyStatus
	// Additional trust bundles for proxy (user defined)
	AdditionalTrustBundle []byte
}

// Gets the packages for the extensions from the MachineConfig, if available.
func (b BuildRequestOpts) getExtensionsPackages() ([]string, error) {
	if len(b.MachineConfig.Spec.Extensions) == 0 {
		return nil, nil
	}

	return ctrlcommon.GetPackagesForSupportedExtensions(b.MachineConfig.Spec.Extensions)
}

// Gets all of the image build request opts from the Kube API server.
func newBuildRequestOptsFromAPI(ctx context.Context, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (*BuildRequestOpts, error) {
	og := optsGetter{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
	}

	opts, err := og.getOpts(ctx, mosb, mosc)
	if err != nil {
		return nil, fmt.Errorf("could not get buildrequestopts from API: %w", err)
	}

	if opts.MachineOSConfig == nil {
		return nil, fmt.Errorf("expected MachineOSConfig to not be nil")
	}

	if opts.MachineOSBuild == nil {
		return nil, fmt.Errorf("expected MachineSOBuild to not be nil")
	}

	if opts.MachineConfig == nil {
		return nil, fmt.Errorf("expected MachineConfig to not be nil")
	}

	if opts.Images == nil {
		return nil, fmt.Errorf("expected images to not be nil")
	}

	if opts.OSImageURLConfig == nil {
		return nil, fmt.Errorf("expected osimageurlconfig to not be nil")
	}

	if opts.BaseImagePullSecret == nil {
		return nil, fmt.Errorf("expected base image pull secret to not be nil")
	}

	if opts.FinalImagePushSecret == nil {
		return nil, fmt.Errorf("expected final image push secret to not be nil")
	}

	return opts, nil
}

// Holds all of the private methods used to populate the BuildRequestOpts
// fields from the Kube API server.
type optsGetter struct {
	kubeclient clientset.Interface
	mcfgclient mcfgclientset.Interface
}

// TODO: Deduplicate this.
func (o *optsGetter) validateMachineOSConfig(mosc *mcfgv1.MachineOSConfig) error {
	if mosc == nil {
		return fmt.Errorf("expected MachineOSConfig not to be nil")
	}

	if mosc.Spec.RenderedImagePushSecret.Name == "" {
		return fmt.Errorf("renderedImagePushSecret empty for MachineOSConfig %s", mosc.Name)
	}

	if mosc.Spec.RenderedImagePushSpec == "" {
		return fmt.Errorf("renderedImagePushspec empty for MachineOSConfig %s", mosc.Name)
	}

	if _, err := reference.ParseNamed(string(mosc.Spec.RenderedImagePushSpec)); err != nil {
		return fmt.Errorf("invalid renderedImagePushSpec for MachineOSConfig %s: %w", mosc.Name, err)
	}

	return nil
}

// Validates that the required fields on a MachineOSBuild are set before beginning the build.
func (o *optsGetter) validateMachineOSBuild(mosb *mcfgv1.MachineOSBuild) error {
	if mosb == nil {
		return fmt.Errorf("expected MachineOSBuild not to be nil")
	}

	if mosb.Spec.MachineConfig.Name == "" {
		return fmt.Errorf("machineConfig.name empty for MachineOSBuild %s", mosb.Name)
	}

	return nil
}

// Gets the BuildRequestOpts after making API queries to get all of the necessary info required.
func (o *optsGetter) getOpts(ctx context.Context, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (*BuildRequestOpts, error) {
	if err := o.validateMachineOSConfig(mosc); err != nil {
		return nil, fmt.Errorf("could not validate MachineOSConfig: %w", err)
	}

	if err := o.validateMachineOSBuild(mosb); err != nil {
		return nil, fmt.Errorf("could not validate MachineOSBuild: %w", err)
	}

	opts, err := o.resolveEntitlements(ctx, mosc)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve entitlements for MachineOSBuild %s: %w", mosb.Name, err)
	}

	imagesConfig, err := ctrlcommon.GetImagesConfig(ctx, o.kubeclient)
	if err != nil {
		return nil, fmt.Errorf("could not get images.json config: %w", err)
	}

	osImageURLConfig, err := ctrlcommon.GetOSImageURLConfig(ctx, o.kubeclient)
	if err != nil {
		return nil, fmt.Errorf("could not get osImageURL config: %w", err)
	}

	var baseImagePullSecretName string
	// Check if a base image pull secret was provided
	opts.hasUserDefinedBaseImagePullSecret = mosc.Spec.BaseImagePullSecret != nil
	if opts.hasUserDefinedBaseImagePullSecret {
		baseImagePullSecretName = mosc.Spec.BaseImagePullSecret.Name
	} else {
		// If not provided, fall back to the global pull secret copy in the MCO namespace
		klog.Infof("BaseImagePullSecret not defined for MachineOSConfig %s, falling back to global pull secret", mosc.Name)
		baseImagePullSecretName = ctrlcommon.GlobalPullSecretCopyName
	}

	baseImagePullSecret, err := o.getValidatedSecret(ctx, baseImagePullSecretName)
	if err != nil {
		return nil, fmt.Errorf("could not get base image pull secret %s: %w", mosc.Spec.BaseImagePullSecret.Name, err)
	}

	finalImagePushSecret, err := o.getValidatedSecret(ctx, mosc.Spec.RenderedImagePushSecret.Name)
	if err != nil {
		return nil, fmt.Errorf("could not get final image push secret %s: %w", mosc.Spec.RenderedImagePushSecret.Name, err)
	}

	mc, err := o.mcfgclient.MachineconfigurationV1().MachineConfigs().Get(ctx, mosb.Spec.MachineConfig.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not retrieve machineconfig %s: %w", mosb.Spec.MachineConfig.Name, err)
	}

	cc, err := o.mcfgclient.MachineconfigurationV1().ControllerConfigs().Get(ctx, ctrlcommon.ControllerConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not retrieve controllerconfig %s: %w", ctrlcommon.ControllerConfigName, err)
	}

	opts.Images = imagesConfig
	opts.MachineConfig = mc
	opts.OSImageURLConfig = osImageURLConfig
	opts.BaseImagePullSecret = baseImagePullSecret
	opts.FinalImagePushSecret = finalImagePushSecret
	opts.MachineOSConfig = mosc.DeepCopy()
	opts.MachineOSBuild = mosb.DeepCopy()
	opts.Proxy = cc.Spec.Proxy
	opts.AdditionalTrustBundle = cc.Spec.AdditionalTrustBundle

	return opts, nil
}

// Gets an image pull secret and validates that it is usable.
func (o *optsGetter) getValidatedSecret(ctx context.Context, name string) (*corev1.Secret, error) {
	secret, err := o.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not fetch secret %s: %w", name, err)
	}

	if err := secrets.ValidateKubernetesImageRegistrySecret(secret); err != nil {
		return nil, fmt.Errorf("could not validate secret %s: %w", name, err)
	}

	return secret, nil
}

// Determines whether the build makes use of entitlements based upon the
// presence (or lack thereof) of specific configmaps and secrets.
func (o *optsGetter) resolveEntitlements(ctx context.Context, mosc *mcfgv1.MachineOSConfig) (*BuildRequestOpts, error) {
	opts := &BuildRequestOpts{}

	etcPkiEntitlements, err := o.getOptionalSecret(ctx, constants.EtcPkiEntitlementSecretName+"-"+mosc.Spec.MachineConfigPool.Name)
	if err != nil {
		return nil, fmt.Errorf("could not determine status of optional Secret %q: %w", constants.EtcPkiEntitlementSecretName, err)
	}

	opts.HasEtcPkiEntitlementKeys = etcPkiEntitlements != nil

	etcPkiRpmGpgKeys, err := o.getOptionalSecret(ctx, constants.EtcPkiRpmGpgSecretName)
	if err != nil {
		return nil, fmt.Errorf("could not determine status of optional Secret %q: %w", constants.EtcPkiRpmGpgSecretName, err)
	}

	opts.HasEtcPkiRpmGpgKeys = etcPkiRpmGpgKeys != nil

	etcYumReposDConfigs, err := o.getOptionalConfigMap(ctx, constants.EtcYumReposDConfigMapName)
	if err != nil {
		return nil, fmt.Errorf("could not determine status of optional ConfigMap %q: %w", constants.EtcYumReposDConfigMapName, err)
	}

	opts.HasEtcYumReposDConfigs = etcYumReposDConfigs != nil

	return opts, nil
}

// Fetches an optional secret to inject into the build. Returns a nil error if
// the secret is not found.
func (o *optsGetter) getOptionalSecret(ctx context.Context, secretName string) (*corev1.Secret, error) {
	// TODO: Consider an implementation that uses listers instead of API clients just to cut down on API server traffic.
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
	// TODO: Consider an implementation that uses listers instead of API clients just to cut down on API server traffic.
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
