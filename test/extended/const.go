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
)
