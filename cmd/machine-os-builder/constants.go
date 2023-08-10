package main

// TODO: Export all of htese from the BuildController package and import /
// consume them instead of replicating them like this.

// on-cluster-build-custom-dockerfile ConfigMap name.
const (
	customDockerfileConfigMapName = "on-cluster-build-custom-dockerfile"
)

// on-cluster-build-config ConfigMap keys.
const (
	// Name of ConfigMap which contains knobs for configuring the build controller.
	onClusterBuildConfigMapName = "on-cluster-build-config"

	// The on-cluster-build-config ConfigMap key which contains a K8s secret capable of pulling of the base OS image.
	baseImagePullSecretNameConfigKey = "baseImagePullSecretName"

	// The on-cluster-build-config ConfigMap key which contains a K8s secret capable of pushing the final OS image.
	finalImagePushSecretNameConfigKey = "finalImagePushSecretName"

	// The on-cluster-build-config ConfigMap key which contains the pullspec of where to push the final OS image (e.g., registry.hostname.com/org/repo:tag).
	finalImagePullspecConfigKey = "finalImagePullspec"
)

// machine-config-osimageurl ConfigMap keys.
const (
	// TODO: Is this a constant someplace else?
	machineConfigOSImageURLConfigMapName = "machine-config-osimageurl"

	// The machine-config-osimageurl ConfigMap key which contains the pullspec of the base OS image (e.g., registry.hostname.com/org/repo:tag).
	baseOSContainerImageConfigKey = "baseOSContainerImage"

	// The machine-config-osimageurl ConfigMap key which contains the pullspec of the base OS image (e.g., registry.hostname.com/org/repo:tag).
	baseOSExtensionsContainerImageConfigKey = "baseOSExtensionsContainerImage"

	// The machine-config-osimageurl ConfigMap key which contains the current OpenShift release version.
	releaseVersionConfigKey = "releaseVersion"

	// The machine-config-osimageurl ConfigMap key which contains the osImageURL
	// value. I don't think we actually use this anywhere though.
	osImageURLConfigKey = "osImageURL"
)

const (
	//	onClusterBuildConfigMapName  string = "on-cluster-build-config"
	imageBuilderTypeConfigMapKey string = "imageBuilderType"
	openshiftImageBuilder        string = "openshift-image-builder"
	customPodImageBuilder        string = "custom-pod-builder"
)

const globalPullSecretCopyName string = "global-pull-secret-copy"
