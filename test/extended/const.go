package extended

const (
	// MAPINamespace is the MachineAPI namespace
	MAPINamespace = "openshift-machine-api"

	// MAPIMachinesetQualifiedName is the fully qualified name of the MAPI MachineSet Resource
	MAPIMachinesetQualifiedName = "machinesets.machine.openshift.io"

	// GoldenBootImagesConfigMap is the configmap that stores the bootimages refs of the current OCP release
	GoldenBootImagesConfigMap = "coreos-bootimages"

	// MAPIMasterMachineLabelSelector is the label used to select the control-plane nodes
	MAPIMasterMachineLabelSelector = "machine.openshift.io/cluster-api-machine-role=master"

	// Labels and Annotations required for determining architecture of a machineset
	MachineSetArchAnnotationKey = "capacity.cluster-autoscaler.kubernetes.io/labels"
	ArchLabelKey                = "kubernetes.io/arch="

	// MachineConfigNamespace mco namespace
	MachineConfigNamespace = "openshift-machine-config-operator"

	// TrueString string for true value
	TrueString = "True"
	// FalseString string for false value
	FalseString = "False"

	// CurrentMachineConfigAnnotationKey is used to fetch current MachineConfig for a machine
	CurrentMachineConfigAnnotationKey = "machineconfiguration.openshift.io/currentConfig"
	// DesiredMachineConfigAnnotationKey is used to specify the desired MachineConfig for a machine
	DesiredMachineConfigAnnotationKey = "machineconfiguration.openshift.io/desiredConfig"
	// CurrentImageAnnotationKey is used to get the current OS image pullspec for a machine
	CurrentImageAnnotationKey = "machineconfiguration.openshift.io/currentImage"
	// DesiredImageAnnotationKey is used to specify the desired OS image pullspec for a machine
	DesiredImageAnnotationKey = "machineconfiguration.openshift.io/desiredImage"
	// MachineConfigDaemonStateAnnotationKey is used to fetch the state of the daemon on the machine.
	MachineConfigDaemonStateAnnotationKey = "machineconfiguration.openshift.io/state"

	// MachineConfigDaemonStateDone is set by daemon when it is done applying an update.
	MachineConfigDaemonStateDone = "Done"
	// MachineConfigDaemonStateDegraded is set by daemon when an error not caused by a bad MachineConfig
	// is thrown during an update.
	MachineConfigDaemonStateDegraded = "Degraded"
	// MachineConfigDaemonStateUnreconcilable is set by the daemon when a MachineConfig cannot be applied.
	MachineConfigDaemonStateUnreconcilable = "Unreconcilable"

	// MachineConfigPoolConditionUpdating indicates the pool is updating
	MachineConfigPoolConditionUpdating = "Updating"
	// MachineConfigPoolConditionUpdated indicates the pool has been updated
	MachineConfigPoolConditionUpdated = "Updated"
	// MachineConfigPoolConditionDegraded indicates the pool is degraded
	MachineConfigPoolConditionDegraded = "Degraded"

	// NodeRoleLabelPrefix is the prefix for node role labels
	NodeRoleLabelPrefix = "node-role.kubernetes.io/"
	// WorkerNodeRole is the worker role label
	WorkerNodeRole = "worker"
	// MasterNodeRole is the master role label
	MasterNodeRole = "master"
	// ControlPlaneNodeRole is the control-plane role label
	ControlPlaneNodeRole = "control-plane"
)
