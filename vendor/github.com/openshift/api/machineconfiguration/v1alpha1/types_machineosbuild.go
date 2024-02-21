package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=machineosbuilds,scope=Cluster
// +kubebuilder:subresource:status
// +openshift:api-approved.openshift.io=https://github.com/openshift/api/pull/1773
// +openshift:enable:FeatureGate=OnClusterBuild
// +openshift:file-pattern=cvoRunLevel=0000_80,operatorName=machine-config,operatorOrdering=01
// +kubebuilder:metadata:labels=openshift.io/operator-managed=
// +kubebuilder:printcolumn:name="Prepared",type="string",JSONPath=.status.conditions[?(@.type=="Prepared")].status
// +kubebuilder:printcolumn:name="Building",type="string",JSONPath=.status.conditions[?(@.type=="Building")].status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=.status.conditions[?(@.type=="Ready")].status
// +kubebuilder:printcolumn:name="Interrupted",type="string",JSONPath=.status.conditions[?(@.type=="Interrupted")].status
// +kubebuilder:printcolumn:name="Restarted",type="string",JSONPath=.status.conditions[?(@.type=="Restarted")].status
// +kubebuilder:printcolumn:name="Failed",type="string",JSONPath=.status.conditions[?(@.type=="Failed")].status

// MachineOSBuild describes a build process managed by the MCO
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type MachineOSBuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec describes the configuration of the machine os build
	// +kubebuilder:validation:Required
	Spec MachineOSBuildSpec `json:"spec"`

	// status describes the lst observed state of this machine os build
	// +optional
	Status MachineOSBuildStatus `json:"status"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineOSBuildList describes all of the Builds on the system
//
// Compatibility level 4: No compatibility is provided, the API can change at any point for any reason. These capabilities should not be used by applications needing long term support.
// +openshift:compatibility-gen:level=4
type MachineOSBuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []MachineOSBuild `json:"items"`
}

// MachineOSBuildSpec describes user-configurable options as well as information about a build process.
type MachineOSBuildSpec struct {
	// machineConfigPool is the pool which the build is for
	// +kubebuilder:validation:Required
	MachineConfigPool MachineConfigPoolReference `json:"machineConfigPool"`
}

// MachineOSBuildStatus describes the state of a build and other helpful information.
type MachineOSBuildStatus struct {
	// conditions are state related conditions for the build. Valid types are:
	// BuildPrepared, Building, BuildFailed, BuildInterrupted, BuildRestarted, and Ready
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// buildName is the build object reference associated with this MachineOSBuildObject
	BuildName string `json:"buildName"`
	// buildStart describes when the build started.
	// +kubebuilder:validation:Required
	BuildStart *metav1.Time `json:"buildStart"`
	// buildEnd describes when the build ended.
	//+optional
	BuildEnd *metav1.Time `json:"buildEnd"`
	// desiredConfig is the desired config we want to build an image for.
	// +kubebuilder:validation:Required
	DesiredConfig RenderedMachineConfigReference `json:"desiredConfig"`
}

// BuildProgess highlights some of the key phases of a build to be tracked in Conditions.
type BuildProgress string

const (
	// prepared indicates that the build has finished preparing. A build is prepared
	// by gathering the build inputs, validating them, and making sure we can do an update as specified.
	MachineOSBuildPrepared BuildProgress = "Prepared"
	// building indicates that the build has been kicked off with the specified image builder
	MachineOSBuilding BuildProgress = "Building"
	// failed indicates that during the build or preparation process, the build failed.
	MachineOSBuildFailed BuildProgress = "Failed"
	// interrupted indicates that the user stopped the build process by modifying part of the build config
	MachineOSBuildInterrupted BuildProgress = "Interrupted"
	// restarted indicates that this build has been either interrupted or failed, and now a new
	// iteration of the process has begun.
	MachineOSBuildRestarted BuildProgress = "Restarted"
	// ready indicates that the build has completed and the image is ready to roll out.
	MachineOSReady BuildProgress = "Ready"
)

// Refers to the name of a (future) MachineOSImage (e.g., "worker-os-image-167651b10ec98af17971d6a47df9e22f", etc.):
type MachineOSImageReference struct {
	// name is the name of the MachineOSImage object attached to this MachineOSBuild.
	// Must be a lowercase RFC-1123 hostname (https://tools.ietf.org/html/rfc1123)
	// It may consist of only alphanumeric characters, hyphens (-) and periods (.)
	// and must be at most 253 characters in length.
	// +kubebuilder:validation:MaxLength:=253
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*$`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// Refers to the name of a rendered MachineConfig (e.g., "rendered-worker-ec40d2965ff81bce7cd7a7e82a680739", etc.):
// the build targets this MachineConfig, this is often used to tell us whether we need an update.
type RenderedMachineConfigReference struct {
	// name is the name of the rendered MachineConfig object.
	// +kubebuilder:validation:MaxLength:=253
	// +kubebuilder:validation:Pattern=`^([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])(\.([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]{0,61}[a-zA-Z0-9]))*$`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}
