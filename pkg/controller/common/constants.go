package common

const (
	// MCONamespace is the namespace that should be used for all API objects owned by the MCO by default
	MCONamespace = "openshift-machine-config-operator"

	// GeneratedByControllerVersionAnnotationKey is used to tag the machineconfigs generated by the controller with the version of the controller.
	GeneratedByControllerVersionAnnotationKey = "machineconfiguration.openshift.io/generated-by-controller-version"

	// ReleaseImageVersionAnnotationKey is used to tag the rendered machineconfigs & controller config with the release image version.
	ReleaseImageVersionAnnotationKey = "machineconfiguration.openshift.io/release-image-version"

	// OSImageURLOverriddenKey is used to tag a rendered machineconfig when OSImageURL has been overridden from default using machineconfig
	OSImageURLOverriddenKey = "machineconfiguration.openshift.io/os-image-url-overridden"

	// ControllerConfigName is the name of the ControllerConfig object that controllers use
	ControllerConfigName = "machine-config-controller"

	// KernelTypeDefault denominates the default kernel type
	KernelTypeDefault = "default"

	// KernelTypeRealtime denominates the realtime kernel type
	KernelTypeRealtime = "realtime"

	// MasterLabel defines the label associated with master node. The master taint uses the same label as taint's key
	MasterLabel = "node-role.kubernetes.io/master"

	// MCNameSuffixAnnotationKey is used to keep track of the machine config name associated with a CR
	MCNameSuffixAnnotationKey = "machineconfiguration.openshift.io/mc-name-suffix"

	// MaxMCNameSuffix is the maximum value of the name suffix of the machine config associated with kubeletconfig and containerruntime objects
	MaxMCNameSuffix int = 9

	// ClusterFeatureInstanceName is a singleton name for featureGate configuration
	ClusterFeatureInstanceName = "cluster"

	// ClusterNodeInstanceName is a singleton name for node configuration
	ClusterNodeInstanceName = "cluster"

	// MachineConfigPoolMaster is the MachineConfigPool name given to the master
	MachineConfigPoolMaster = "master"

	// MachineConfigPoolWorker is the MachineConfigPool name given to the worker
	MachineConfigPoolWorker = "worker"

	// LayeringEnabledPoolLabel is the label that enables the "layered" workflow path for a pool.
	LayeringEnabledPoolLabel = "machineconfiguration.openshift.io/layering-enabled"

	// ExperimentalLayeringPoolLabel is the label that enables the "layered" workflow path for a pool
	ExperimentalLayeringPoolLabel = "machineconfiguration.openshift.io/layered"

	// ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey is the annotation that signifies which rendered config
	// TODO(zzlotnik): Determine if we should use this still.
	ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey = "machineconfiguration.openshift.io/newestImageEquivalentConfig"

	OSImageBuildPodLabel = "machineconfiguration.openshift.io/buildPod"
)
