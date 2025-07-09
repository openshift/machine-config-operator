package extended

const (
	// MachineConfigNamespace mco namespace
	MachineConfigNamespace = "openshift-machine-config-operator"
	// MachineConfigDaemon mcd container name
	MachineConfigDaemon = "machine-config-daemon"

	// MachineConfigPoolMaster master pool name
	MachineConfigPoolMaster = "master"
	// MachineConfigPoolWorker worker pool name
	MachineConfigPoolWorker = "worker"

	// ControllerContainer name of the controller container in the controller pod
	ControllerContainer = "machine-config-controller"
	// ControllerLabel label used to identify the controller pod
	ControllerLabel = "k8s-app"
	// ControllerLabelValue value used to identify the controller pod
	ControllerLabelValue = "machine-config-controller"

	// DefaultLayeringQuayRepository the quay repository that will be used by default to push auxiliary layering images
	DefaultLayeringQuayRepository = "quay.io/mcoqe/layering"
	// InternalRegistrySvcURL is the url to reach the internal registry service from inside a cluster
	InternalRegistrySvcURL = "image-registry.openshift-image-registry.svc:5000"

	// LayeringBaseImageReleaseInfo is the name of the layering base image in release info
	LayeringBaseImageReleaseInfo = "rhel-coreos"
	GenericMCTemplate            = "generic-machine-config-template.yml"

	// ExpirationDockerfileLabel Expiration label in Dockerfile
	ExpirationDockerfileLabel = `LABEL maintainer="mco-qe-team" quay.expires-after=24h`

	// DefaultMinutesWaitingPerNode is the  number of minutes per node that the MCPs will wait to become updated
	DefaultMinutesWaitingPerNode = 13

	// KernelChangeIncWait extra minutes that MCPs will wait per node if we change the kernel in a configuration
	KernelChangeIncWait = 5

	// ExtensionsChangeIncWait extra minutes that MCPs will wait per node if we change the extensions in a configuration
	ExtensionsChangeIncWait = 5

	// TrueString string for true value
	TrueString = "True"
	// FalseString string for false value
	FalseString = "False"
)
