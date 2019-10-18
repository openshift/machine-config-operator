package template

const (
	// EtcdImageKey is the key that references the etcd image in the controller
	EtcdImageKey string = "etcd"

	// SetupEtcdEnvKey is the key that references the setup-etcd-environment image in the controller
	SetupEtcdEnvKey string = "setupEtcdEnv"

	// GCPRoutesControllerKey is the key that references the gcp-routes-controller image in the controller
	GCPRoutesControllerKey string = "gcpRoutesController"

	// InfraImageKey is the key that references the infra image in the controller for crio.conf
	InfraImageKey string = "infraImage"

	// KubeClientAgentImageKey is the key that references the kube-client-agent image in the controller
	KubeClientAgentImageKey string = "kubeClientAgentImage"

	// KeepalivedKey is the key that references the keepalived-ipfailover image in the controller
	KeepalivedKey string = "keepalivedImage"

	// CorednsKey is the key that references the coredns image in the controller
	CorednsKey string = "corednsImage"

	// MdnsPublisherKey is the key that references the mdns-publisher image in the controller
	MdnsPublisherKey string = "mdnsPublisherImage"

	// HaproxyKey is the key that references the haproxy-router image in the controller
	HaproxyKey string = "haproxyImage"

	// BaremetalRuntimeCfgKey is the key that references the baremetal-runtimecfg image in the controller
	BaremetalRuntimeCfgKey string = "baremetalRuntimeCfgImage"
)
