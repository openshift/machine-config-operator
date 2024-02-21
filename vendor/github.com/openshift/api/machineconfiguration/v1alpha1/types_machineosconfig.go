package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=machineosconfigs,scope=Cluster
// +kubebuilder:subresource:status
// +openshift:api-approved.openshift.io=https://github.com/openshift/api/pull/1773
// +openshift:enable:FeatureGate=OnClusterBuild
// +openshift:file-pattern=cvoRunLevel=0000_80,operatorName=machine-config,operatorOrdering=01
// +kubebuilder:metadata:labels=openshift.io/operator-managed=

// MachineOSConfig describes a build process managed by the MCO
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type MachineOSConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec describes the configuration of the machineosconfig
	// +kubebuilder:validation:Required
	Spec MachineOSConfigSpec `json:"spec"`

	// status describes the status of the machineosconfig
	// +optional
	Status MachineOSConfigStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineOSConfigList describes all of the Builds on the system
//
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type MachineOSConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MachineOSConfig `json:"items"`
}

// MachineOSConfigSpec describes user-configurable options as well as information about a build process.
type MachineOSConfigSpec struct {
	// machineConfigPool is the pool which the build is for
	// +kubebuilder:validation:Required
	MachineConfigPool MachineConfigPoolReference `json:"machineConfigPool"`
	// buildInputs is where user options for the build live
	// +kubebuilder:validation:Required
	BuildInputs BuildInputs `json:"buildInputs"`
	// +kubebuilder:validation:Required
	// rebuild allows the user to manually trigger a rebuild
	Rebuild MachineOSRebuild `json:"rebuild"`
}

// MachineOSConfigStatus describes the status of builds associated with this MachineOSConfig
type MachineOSConfigStatus struct {
	// buildHistory contains previous iterations of failed, interrupted, or succeded builds related to this config.
	// this will only detail the 3 previous builds associated with the MachineOSConfig.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=3
	// +optional
	BuildHistory []BuildHistory `json:"buildHistory,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

// BuildInputs holds all of the information needed to trigger a build
type BuildInputs struct {
	// imageBuilderType specifies the backend to be used to build the image.
	// +kubebuilder:default:=Default
	// +kubebuilder:validation:Enum:=Default;OpenShiftImageBuilder;PodImageBuilder
	// Valid options are: OpenShiftImageBuilder, PodImageBuilder, and Default (OpenShiftImageBuilder)
	ImageBuilderType MachineOSImageBuilderType `json:"imageBuilderType"`
	// baseOSExtensionsImagePullspec is the base Extensions image used in the build process
	// The format of the image pullspec is:
	// host[:port][/namespace]/name@sha256:<digest>
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=447
	// +kubebuilder:validation:XValidation:rule=`self.split('@').size() == 2 && self.split('@')[1].matches('^sha256:[a-f0-9]{64}$')`,message="the OCI Image reference must end with a valid '@sha256:<digest>' suffix, where '<digest>' is 64 characters long"
	// +kubebuilder:validation:XValidation:rule=`self.split('@')[0].matches('^([a-zA-Z0-9-]+\\.)+[a-zA-Z0-9-]+(:[0-9]{2,5})?/([a-zA-Z0-9-_]{0,61}/)?[a-zA-Z0-9-_.]*?$')`,message="the OCI Image name should follow the host[:port][/namespace]/name format, resembling a valid URL without the scheme"
	// +optional
	BaseOSExtensionsImagePullspec string `json:"baseOSExtensionsImagePullspec"`
	// baseOSImagePullspec is the base OSImage we use to build our custom image.
	// The format of the image pullspec is:
	// host[:port][/namespace]/name@sha256:<digest>
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=447
	// +kubebuilder:validation:XValidation:rule=`self.split('@').size() == 2 && self.split('@')[1].matches('^sha256:[a-f0-9]{64}$')`,message="the OCI Image reference must end with a valid '@sha256:<digest>' suffix, where '<digest>' is 64 characters long"
	// +kubebuilder:validation:XValidation:rule=`self.split('@')[0].matches('^([a-zA-Z0-9-]+\\.)+[a-zA-Z0-9-]+(:[0-9]{2,5})?/([a-zA-Z0-9-_]{0,61}/)?[a-zA-Z0-9-_.]*?$')`,message="the OCI Image name should follow the host[:port][/namespace]/name format, resembling a valid URL without the scheme"
	// +optional
	BaseOSImagePullspec string `json:"baseOSImagePullspec"`
	// baseImagePullSecret is the secret used to pull the base image.
	// +kubebuilder:validation:Required
	BaseImagePullSecret ImageSecretObjectReference `json:"baseImagePullSecret"`
	// finalImagePushSecret is the secret used to connect to a user registry.
	// the final image push and pull secrets should be separate for security concerns. If the final image push secret is somehow exfiltrated,
	// that gives someone the power to push images to the image repository. By comparison, if the final image pull secret gets exfiltrated,
	// that only gives someone to pull images from the image repository. It's basically the principle of least permissions.
	// this push secret will be used only by the MachineConfigController pod to push the image to the final destination. Not all nodes will need to push this image, most of them
	// will only need to pull the image in order to use it.
	// +kubebuilder:validation:Required
	FinalImagePushSecret ImageSecretObjectReference `json:"finalImagePushSecret"`
	// finalImagePullSecret is the secret used to pull the final produced image.
	// the final image push and pull secrets should be separate for security concerns. If the final image push secret is somehow exfiltrated,
	// that gives someone the power to push images to the image repository. By comparison, if the final image pull secret gets exfiltrated,
	// that only gives someone to pull images from the image repository. It's basically the principle of least permissions.
	// this pull secret will be used on all nodes in the pool. These nodes will need to pull the final OS image and boot into it using rpm-ostree or bootc.
	// +optional
	FinalImagePullSecret ImageSecretObjectReference `json:"finalImagePullSecret"`
	// finalImagePullspec describes the location of the final image.
	// The format of the image pullspec is:
	// host[:port][/namespace]/name@sha256:<digest> or host[:port][/namespace]/name:<tag>
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=447
	// +kubebuilder:validation:XValidation:rule=`(self.split('@').size() == 2 && self.split('@')[1].matches('^sha256:[a-f0-9]{64}$')) || (self.split(':').size() == 2 && self.split(':')[1].matches('^([a-zA-Z0-9-])+$'))`,message="the OCI Image reference must end with a valid '@sha256:<digest>' suffix or a valid :<tag>, where '<digest>' and '<tag>' is 64 characters long"
	// +kubebuilder:validation:XValidation:rule=`(self.split('@').size() == 2 && self.split('@')[0].matches('^([a-zA-Z0-9-]+\\.)+[a-zA-Z0-9-]+(:[0-9]{2,5})?/([a-zA-Z0-9-_]{0,61}/)?[a-zA-Z0-9-_.]*?$')) || (self.split(':').size() == 2 && self.split(':')[0].matches('^([a-zA-Z0-9-]+\\.)+[a-zA-Z0-9-]+(:[0-9]{2,5})?/([a-zA-Z0-9-_]{0,61}/)?[a-zA-Z0-9-_.]*?$'))`,message="the OCI Image name should follow the host[:port][/namespace]/name format, resembling a valid URL without the scheme"
	// +kubebuilder:validation:Required
	FinalImagePullspec string `json:"finalImagePullspec"`
	// containerFile describes the custom data the user has specified to build into the image.
	// +patchMergeKey=containerFileArch
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=containerFileArch
	// +optional
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=2
	// +optional
	Containerfile []MachineOSContainerfile `json:"containerFile" patchStrategy:"merge" patchMergeKey:"containerFileArch"`
}

// MachineOSContainerfile contains all custom content the user wants built into the image
type MachineOSContainerfile struct {
	// containerFileArch describes the architecture this containerfile is to be built for
	// this arch is optional. If the user does not specify an architecture, it is assumed
	// that the content can be applied to all architectures, or in a single arch cluster: the only architecture.
	// +kubebuilder:validation:Enum:=arm64;amd64;ppc64le;s390x;aarch64;x86_64;noArch
	// +kubebuilder:default:=noArch
	// +optional
	ContainerFileArch ContainerFileArch `json:"containerFileArch"`
	// content is the custom content to be built
	// +kubebuilder:validation:Required
	Content string `json:"content"`
}

type ContainerFileArch string

const (
	// describes the arm64 architecture
	Arm64 ContainerFileArch = "arm64"
	// describes the amd64 architecture
	Amd64 ContainerFileArch = "amd64"
	// describes the ppc64le architecture
	Ppc ContainerFileArch = "ppc64le"
	// describes the s390x architecture
	S390 ContainerFileArch = "s390x"
	// describes the aarch64 architecture
	Aarch64 ContainerFileArch = "aarch64"
	// describes the x86_64 architecture
	x86_64 ContainerFileArch = "x86_64"
	// describes a containerfile that can be applied to any arch
	noArch ContainerFileArch = "noArch"
)

// Refers to the name of a MachineConfigPool (e.g., "worker", "infra", etc.):
type MachineConfigPoolReference struct {
	// name of the MachineConfigPool object.
	// +kubebuilder:validation:MaxLength:=253
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*$`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// Refers to the name of an image registry push/pull secret needed in the build process.
type ImageSecretObjectReference struct {
	// name is the name of the secret used to push or pull this MachineOSConfig object.
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

type TriggerRebuild string

const (
	// RebuildNow indicates that the user wants to rebuild their image immediately
	RebuildNow TriggerRebuild = "RebuildNow"
	// RebuildDelayed indicates that the user wants to rebuild their image the next time the build controller is idle.
	// if there is a build in progress, a rebuild will be triggered the next time that build fails or succeeds. This is often used
	// when the user changes the top level configuration for a pool's build.
	RebuildDelayed TriggerRebuild = "RebuildDelayed"
	// NoRebuild indicates the user does not want a manual rebuild, this is the default.
	NoRebuild TriggerRebuild = "NoRebuild"
)

// MachineOSRebuild describes user options to manage rebuilding the image.
type MachineOSRebuild struct {
	// rebuildStrategy is the user provided manual rebuild method of choice
	// +kubebuilder:default:=NoRebuild
	// +kubebuilder:validation:Enum:=NoRebuild;RebuildNow;RebuildDelayed
	// +kubebuilder:validation:Required
	RebuildStrategy TriggerRebuild `json:"rebuildStrategy"`
	// maxRetries is the amount of times we want to auto-rebuild after build failures before we deem the update unreconcilable.
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Required
	MaxRetries int `json:"maxRetries"`
}

type MachineOSImageBuilderType string

const (
	// describes that the machine-os-builder will use the built in OpenshiftImageBuilder
	OCPBuilder MachineOSImageBuilderType = "OpenShiftImageBuilder"
	// describes that the machine-os-builder will use a custom pod builder that uses buildah
	PodBuilder MachineOSImageBuilderType = "PodImageBuilder"
	// Default means the cluster will use the OpenshiftImageBuilder
	DefaultBuilder MachineOSImageBuilderType = "Default"
)

// BuildHistory contains information about related builds
// these builds can be failed, interrupted, or succeeded.
type BuildHistory struct {
	// name is the name of the build
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// buildFailure contains an optional message of why this build ended prematurely.
	// +optional
	BuildFailure string `json:"buildFailure"`
	// configMaps references all config map objects created during the build process
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	// +optional
	ConfigMaps []BuildConfigMapObjectReference `json:"configmaps" patchStrategy:"merge" patchMergeKey:"name" `
	// buildPod references the build pod used in this previous build if it existed
	// +optional
	BuildPod BuildPodReference `json:"buildPod"`
}

// BuildPodReference refers to the pod used to build an image that either failed or was interrupted
type BuildPodReference struct {
	// name of the build pod used
	// Must be a lowercase RFC-1123 hostname (https://tools.ietf.org/html/rfc1123)
	// It may consist of only alphanumeric characters, hyphens (-) and periods (.)
	// and must be at most 253 characters in length.
	// +kubebuilder:validation:MaxLength:=253
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*$`
	Name string `json:"name"`
	// exitReason is how the pod exited
	ExitReason string `json:"exitReason"`
}

// BuildConfigMapObjectReference refers to the name and contents of a configmap used in a previous build
type BuildConfigMapObjectReference struct {
	// name of the configmap
	// +kubebuilder:validation:MaxLength:=253
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*$`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}
