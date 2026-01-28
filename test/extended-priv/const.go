package extended

import "time"

const (
	// Ignition default version
	IgnitionDefaultVersion = "3.5.0"

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
	// GenericMCTemplate is the name of a MachineConfig template that can be fully configured by parameters
	GenericMCTemplate = "generic-machine-config-template.yml"

	// NodeAnnotationState state of the mc
	NodeAnnotationState = "machineconfiguration.openshift.io/state"

	// AWSPlatform value used to identify aws infrastructure
	AWSPlatform = "aws"
	// GCPPlatform value used to identify gcp infrastructure
	GCPPlatform = "gcp"
	// AzurePlatform value used to identify azure infrastructure
	AzurePlatform = "azure"
	// VspherePlatform value used to identify Vsphere infrastructure
	VspherePlatform = "vsphere"

	// ExpirationDockerfileLabel Expiration label in Dockerfile
	ExpirationDockerfileLabel = `LABEL maintainer="mco-qe-team" quay.expires-after=24h`

	// DefaultExpectTimeout is the default timeout for expect operations
	DefaultExpectTimeout = 10 * time.Second

	// DefaultMinutesWaitingPerNode is the  number of minutes per node that the MCPs will wait to become updated
	DefaultMinutesWaitingPerNode = 13

	// KernelChangeIncWait extra minutes that MCPs will wait per node if we change the kernel in a configuration
	KernelChangeIncWait = 5

	// ExtensionsChangeIncWait extra minutes that MCPs will wait per node if we change the extensions in a configuration
	ExtensionsChangeIncWait = 5

	// MachineAPINamespace is the MachineAPI namespace
	MachineAPINamespace = "openshift-machine-api"

	// Machine phase Provisioning
	MachinePhaseProvisioning = "Provisioning"
	// Machine phase Deleting
	MachinePhaseDeleting = "Deleting"
	// We use full name to get machineset/machine xref: https://access.redhat.com/solutions/7040368
	// MachineSetFullName is the machineset fully qualified name
	MachineSetFullName = "machineset.machine.openshift.io"
	// MachineFullName is the machine fully qualified name
	MachineFullName = "machine.machine.openshift.io"

	// MachineSetResource is the resource name for machinesets
	MachineSetResource = "machinesets"
	// ControlPlaneMachineSetResource is the resource name for controlplanemachinesets
	ControlPlaneMachineSetResource = "controlplanemachinesets"

	// TrueString string for true value
	TrueString = "True"
	// FalseString string for false value
	FalseString = "False"

	// BusyBoxImage the multiplatform busybox image stored in openshifttest
	BusyBoxImage = "quay.io/openshifttest/busybox@sha256:c5439d7db88ab5423999530349d327b04279ad3161d7596d2126dfb5b02bfd1f"
	// AlpineImage the multiplatform alpine image stored in openshifttest
	AlpineImage = "quay.io/openshifttest/alpine@sha256:dc1536cbff0ba235d4219462aeccd4caceab9def96ae8064257d049166890083"

	// Constants for NodeDisruptionPolicy
	NodeDisruptionPolicyActionNone         = "None"
	NodeDisruptionPolicyActionReboot       = "Reboot"
	NodeDisruptionPolicyActionReload       = "Reload"
	NodeDisruptionPolicyActionRestart      = "Restart"
	NodeDisruptionPolicyActionDrain        = "Drain"
	NodeDisruptionPolicyActionDaemonReload = "DaemonReload"
)
