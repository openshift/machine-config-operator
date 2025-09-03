package constants

// Label that associates any objects with on-cluster layering. Should be added
// to every object that BuildController creates or manages, ephemeral or not.
const (
	OnClusterLayeringLabelKey = "machineconfiguration.openshift.io/on-cluster-layering"
)

// Labels added to all ephemeral build objects the BuildController creates.
const (
	EphemeralBuildObjectLabelKey    = "machineconfiguration.openshift.io/ephemeral-build-object"
	RenderedMachineConfigLabelKey   = "machineconfiguration.openshift.io/rendered-machine-config"
	TargetMachineConfigPoolLabelKey = "machineconfiguration.openshift.io/target-machine-config-pool"
)

// Annotations added to all ephemeral build objects BuildController creates.
const (
	MachineOSBuildNameAnnotationKey      = "machineconfiguration.openshift.io/machine-os-build"
	MachineOSConfigNameAnnotationKey     = "machineconfiguration.openshift.io/machine-os-config"
	MachineOSConfigNameLabelKey          = MachineOSConfigNameAnnotationKey
	MachineOSBuildNameLabelKey           = MachineOSBuildNameAnnotationKey
	BuildTypeUIDAnnotationKey            = "machineconfiguration.openshift.io/buildtype-uid"
	RenderedImagePushSecretAnnotationKey = "machineconfiguration.openshift.io/rendered-image-push-secret"
)

// The MachineOSConfig will get updated with this annotation once a
// MachineOSBuild has been created and assigned to it. We should probably
// make this a field on the Status object.
const (
	CurrentMachineOSBuildAnnotationKey string = "machineconfiguration.openshift.io/current-machine-os-build"
)

// When this annotation is added to a MachineOSConfig, the current
// MachineOSBuild will be deleted, which will cause a rebuild to occur.
const (
	RebuildMachineOSConfigAnnotationKey string = "machineconfiguration.openshift.io/rebuild"
)

// Entitled build secret names
const (
	// Name of the etc-pki-entitlement secret from the openshift-config-managed namespace.
	EtcPkiEntitlementSecretName = "etc-pki-entitlement"

	// Name of the etc-pki-rpm-gpg secret.
	EtcPkiRpmGpgSecretName = "etc-pki-rpm-gpg"

	// Name of the etc-yum-repos-d ConfigMap.
	EtcYumReposDConfigMapName = "etc-yum-repos-d"
)

// Canonical secrets
const (
	// This label gets applied to all secrets that we've canonicalized as a way
	// to indicate that we created and own them.
	CanonicalSecretLabelKey string = "machineconfiguration.openshift.io/canonicalizedSecret"
	// This label is applied to all canonicalized secrets. Its value should
	// contain the original name of the secret that has been canonicalized.
	OriginalSecretNameLabelKey string = "machineconfiguration.openshift.io/originalSecretName"
)

// Entitled build annotation keys
const (
	entitlementsAnnotationKeyBase  = "machineconfiguration.openshift.io/has-"
	EtcPkiEntitlementAnnotationKey = entitlementsAnnotationKeyBase + EtcPkiEntitlementSecretName
	EtcYumReposDAnnotationKey      = entitlementsAnnotationKeyBase + EtcYumReposDConfigMapName
	EtcPkiRpmGpgAnnotationKey      = entitlementsAnnotationKeyBase + EtcPkiRpmGpgSecretName
)

// batchv1.Job configuration
const (
	JobMaxRetries  int32 = 3
	JobCompletions int32 = 1
)
