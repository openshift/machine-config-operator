// Package mco stores all MCO automated test cases
package mco

import (
	"time"
)

const (
	// MachineConfigNamespace mco namespace
	MachineConfigNamespace = "openshift-machine-config-operator"
	// MachineConfigDaemon mcd container name
	MachineConfigDaemon = "machine-config-daemon"
	// MachineConfigOperator mco container name
	MachineConfigOperator = "machine-config-operator"
	// MachineConfigDaemonEvents cluster role binding
	MachineConfigDaemonEvents = "machine-config-daemon-events"

	// MachineConfigPoolMaster master pool name
	MachineConfigPoolMaster = "master"
	// MachineConfigPoolWorker worker pool name
	MachineConfigPoolWorker = "worker"

	// ControllerDeployment name of the deployment deploying the machine config controller
	ControllerDeployment = "machine-config-controller"
	// ControllerContainer name of the controller container in the controller pod
	ControllerContainer = "machine-config-controller"
	// ControllerLabel label used to identify the controller pod
	ControllerLabel = "k8s-app"
	// ControllerLabelValue value used to identify the controller pod
	ControllerLabelValue = "machine-config-controller"

	// EnvVarLayeringTestImageRepository environment variable to define the image repository used by layering test cases
	EnvVarLayeringTestImageRepository = "LAYERING_TEST_IMAGE_REPOSITORY"

	// DefaultLayeringQuayRepository the quay repository that will be used by default to push auxiliary layering images
	DefaultLayeringQuayRepository = "quay.io/mcoqe/layering"
	// InternalRegistrySvcURL is the url to reach the internal registry service from inside a cluster
	InternalRegistrySvcURL = "image-registry.openshift-image-registry.svc:5000"

	// LayeringBaseImageReleaseInfo is the name of the layering base image in release info
	LayeringBaseImageReleaseInfo = "rhel-coreos"
	// TmplHypershiftMcConfigMap template file name:hypershift-cluster-mc-configmap.yaml, it's used to create mc for hosted cluster
	TmplHypershiftMcConfigMap = "hypershift-cluster-mc-configmap.yaml"
	// TmplAddSSHAuthorizedKeyForWorker template file name: change-worker-add-ssh-authorized-key
	TmplAddSSHAuthorizedKeyForWorker = "change-worker-add-ssh-authorized-key.yaml"
	// GenericMCTemplate is the name of a MachineConfig template that can be fully configured by parameters
	GenericMCTemplate = "generic-machine-config-template.yml"

	// HypershiftCrNodePool keyword: nodepool
	HypershiftCrNodePool = "nodepool"
	// HypershiftCrHostedCluster keyword: hostedcluster
	HypershiftCrHostedCluster = "hostedcluster"
	// HypershiftNsClusters namespace: clusters
	HypershiftNsClusters = "clusters"
	// HypershiftNs operator namespace: hypershift
	HypershiftNs = "hypershift"
	// HypershiftAwsMachine keyword: awsmachine
	HypershiftAwsMachine = "awsmachine"

	// NodeAnnotationCurrentConfig current config
	NodeAnnotationCurrentConfig = "machineconfiguration.openshift.io/currentConfig"
	// NodeAnnotationDesiredConfig desired config
	NodeAnnotationDesiredConfig = "machineconfiguration.openshift.io/desiredConfig"
	// NodeAnnotationDesiredDrain desired drain id
	NodeAnnotationDesiredDrain = "machineconfiguration.openshift.io/desiredDrain"
	// NodeAnnotationLastAppliedDrain last applied drain id
	NodeAnnotationLastAppliedDrain = "machineconfiguration.openshift.io/lastAppliedDrain"
	// NodeAnnotationReason failure reason
	NodeAnnotationReason = "machineconfiguration.openshift.io/reason"
	// NodeAnnotationState state of the mc
	NodeAnnotationState = "machineconfiguration.openshift.io/state"

	// TestCtxKeyBucket hypershift test s3 bucket name
	TestCtxKeyBucket = "bucket"
	// TestCtxKeyNodePool hypershift test node pool name
	TestCtxKeyNodePool = "nodepool"
	// TestCtxKeyCluster hypershift test hosted cluster name
	TestCtxKeyCluster = "cluster"
	// TestCtxKeyConfigMap hypershift test config map name
	TestCtxKeyConfigMap = "configmap"
	// TestCtxKeyKubeConfig hypershift test kubeconfig of hosted cluster
	TestCtxKeyKubeConfig = "kubeconfig"
	// TestCtxKeyFilePath hypershift test filepath in machine config
	TestCtxKeyFilePath = "filepath"
	// TestCtxKeySkipCleanUp indicates whether clean up should be skipped
	TestCtxKeySkipCleanUp = "skipCleanUp"

	// AWSPlatform value used to identify aws infrastructure
	AWSPlatform = "aws"
	// GCPPlatform value used to identify gcp infrastructure
	GCPPlatform = "gcp"
	// AzurePlatform value used to identify azure infrastructure
	AzurePlatform = "azure"
	// NonePlatform value used to identify a None Platform value
	NonePlatform = "none"
	// BaremetalPlatform value used to identify baremetal infrastructure
	BaremetalPlatform = "baremetal"
	// KniPlatform value used to identify KNI infrastructure
	KniPlatform = "kni"
	// NutanixPlatform value used to identify Nutanix infrastructure
	NutanixPlatform = "nutanix"
	// OpenstackPlatform value used to identify Openstack infrastructure
	OpenstackPlatform = "openstack"
	// OvirtPlatform value used to identify Ovirt infrastructure
	OvirtPlatform = "ovirt"
	// VspherePlatform value used to identify Vsphere infrastructure
	VspherePlatform = "vsphere"
	// AlibabaCloudPlatform value used to identify AlibabaCloud infrastructure
	AlibabaCloudPlatform = "alibabacloud"

	// ExpirationDockerfileLabel Expiration label in Dockerfile
	ExpirationDockerfileLabel = `LABEL maintainer="mco-qe-team" quay.expires-after=2h`

	layeringTestsTmpNamespace   = "layering-tests-imagestreams"
	layeringRegistryAdminSAName = "test-registry-sa"

	// DefaultExpectTimeout is the default tiemout for expect commands
	DefaultExpectTimeout = 10 * time.Second

	// DefaultMinutesWaitingPerNode is the  number of minutes per node that the MCPs will wait to become updated
	DefaultMinutesWaitingPerNode = 10

	// KernelChangeIncWait exta minutes that MCPs will wait per node if we change the kernel in a configuration
	KernelChangeIncWait = 5

	// ExtensionsChangeIncWait exta minutes that MCPs will wait per node if we change the extensions in a configuration
	ExtensionsChangeIncWait = 5

	// ImageRegistryCertificatesDir is the path were the image registry certificates will be stored in a node. Example: /etc/docker/certs.d/mycertname/ca.crt
	ImageRegistryCertificatesDir = "/etc/docker/certs.d"

	// ImageRegistryCertificatesFileName is the name of the image registry certificates. Example: /etc/docker/certs.d/mycertname/ca.crt
	ImageRegistryCertificatesFileName = "ca.crt"

	// SecurePort is the tls secured port to serve ignition configs
	IgnitionSecurePort = 22623
	// InsecurePort is the port to serve ignition configs w/o tls
	IgnitionInsecurePort = 22624

	// Machine phase Provisioning
	MachinePhaseProvisioning = "Provisioning"
	// Machine phase Deleting
	MachinePhaseDeleting = "Deleting"
	// We use full name to get machineset/machine xref: https://access.redhat.com/solutions/7040368
	// Machineset fully qualified name
	MachineSetFullName = "machineset.machine.openshift.io"
	// Machine fully qualified name
	MachineFullName = "machine.machine.openshift.io"

	// TrueString string for true value
	TrueString = "True"
	// FalseString string for true value
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
	NodeDisruptionPolicyFiles              = "files"
	NodeDisruptionPolicyUnits              = "units"
	NodeDisruptionPolicySshkey             = "sshkey"

	// Regexp used to know if MCD logs is reporting that crio was reloaded
	MCDCrioReloadedRegexp = "crio.* reloaded successfully"

	// MachineConfigNodesFeature name of the MachineConfigNodes feature
	MachineConfigNodesFeature = "MachineConfigNodes"

	// MachineConfigServer mcs container name
	MachineConfigServer = "machine-config-server"

	// TLS Security Version
	VersionTLS10 = 0x0301
	VersionTLS11 = 0x0302
	VersionTLS12 = 0x0303
	VersionTLS13 = 0x0304
)

var (
	// OnPremPlatforms describes all the on-prem platforms
	// xref: https://github.com/openshift/machine-config-operator/blob/752667ba9dfcdefd12222ab422201fa3f9846aca/pkg/controller/template/render.go#L593
	OnPremPlatforms = map[string]string{
		BaremetalPlatform: "openshift-kni-infra",
		NutanixPlatform:   "openshift-nutanix-infra",
		OpenstackPlatform: "openshift-openstack-infra",
		OvirtPlatform:     "openshift-ovirt-infra",
		VspherePlatform:   "openshift-vsphere-infra",
	}
)
