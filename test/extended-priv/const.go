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

	// ImageRegistryCertificatesDir is the path were the image registry certificates will be stored in a node. Example: /etc/docker/certs.d/mycertname/ca.crt
	ImageRegistryCertificatesDir = "/etc/docker/certs.d"

	// ImageRegistryCertificatesFileName is the name of the image registry certificates. Example: /etc/docker/certs.d/mycertname/ca.crt
	ImageRegistryCertificatesFileName = "ca.crt"

	// SecurePort is the tls secured port to serve ignition configs
	IgnitionSecurePort = 22623
	// InsecurePort is the port to serve ignition configs w/o tls
	IgnitionInsecurePort = 22624

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
	// TestSSLImage the testssl image stored in openshiftest
	TestSSLImage = "quay.io/openshifttest/testssl@sha256:ad6fb8002cb9cfce3ddc8829fd6e7e0d997aeb1faf972650f3e5d7603f90c6ef"

	// Constants for NodeDisruptionPolicy
	NodeDisruptionPolicyActionNone         = "None"
	NodeDisruptionPolicyActionReboot       = "Reboot"
	NodeDisruptionPolicyActionReload       = "Reload"
	NodeDisruptionPolicyActionRestart      = "Restart"
	NodeDisruptionPolicyActionDrain        = "Drain"
	NodeDisruptionPolicyActionDaemonReload = "DaemonReload"
	// BootImageSkewEnforcement feature constants

	// RHCOSVersionMode is used to signify that the cluster boot image is described via RHCOS version
	RHCOSVersionMode = "RHCOSVersion"
	// OCPVersionMode is used to signify that the cluster boot image is described via OCP version
	OCPVersionMode = "OCPVersion"
	// SkewEnforcementManualMode indicates boot image updates require manual intervention
	SkewEnforcementManualMode = "Manual"
	// SkewEnforcementAutomaticMode indicates boot image updates are applied automatically
	SkewEnforcementAutomaticMode = "Automatic"
	// SkewEnforcementNoneMode indicates boot image skew enforcement is disabled
	SkewEnforcementNoneMode = "None"

	// MachineConfigServer mcs container name
	MachineConfigServer = "machine-config-server"

	// TLS Security Version
	VersionTLS10 = 0x0301
	VersionTLS11 = 0x0302
	VersionTLS12 = 0x0303
	VersionTLS13 = 0x0304

	// Extension names
	ipsecExtension               = "ipsec"
	usbguardExtension            = "usbguard"
	kerberosExtension            = "kerberos"
	kernelDevelExtension         = "kernel-devel"
	sandboxedContainersExtension = "sandboxed-containers"
	sysstatExtension             = "sysstat"
)

// KernelType represents the type of kernel
type KernelType string

const (
	// KernelTypeRealtime represents realtime kernel
	KernelTypeRealtime KernelType = "realtime"
	// KernelType64kPages represents 64k-pages kernel
	KernelType64kPages KernelType = "64k-pages"
)

var (
	// Map with all available extensions and the packages they install
	AllExtenstions = map[string][]string{
		ipsecExtension:               {"NetworkManager-libreswan", "libreswan"},
		usbguardExtension:            {"usbguard"},
		kerberosExtension:            {"krb5-workstation", "libkadm5"},
		kernelDevelExtension:         {"kernel-devel", "kernel-headers"},
		sandboxedContainersExtension: {"kata-containers"},
		sysstatExtension:             {"sysstat"},
	}

	// OSImageStream constants
	// OSImageStreamRHEL9 represents the RHEL 9 OS image stream
	OSImageStreamRHEL9 = "rhel-9"
	// OSImageStreamRHEL10 represents the RHEL 10 OS image stream
	OSImageStreamRHEL10 = "rhel-10"

	// DefaultOSImageStream is the default OS image stream name
	DefaultOSImageStream = OSImageStreamRHEL9

	// SupportedOSImageStreams is the list of supported OS image streams
	SupportedOSImageStreams = []string{
		OSImageStreamRHEL9,
		OSImageStreamRHEL10,
	}
)
