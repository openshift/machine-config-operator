package template

const (
	// GCPRoutesControllerKey is the key that references the gcp-routes-controller image in the controller
	GCPRoutesControllerKey string = "gcpRoutesControllerKey"

	// InfraImageKey is the key that references the infra image in the controller for crio.conf
	InfraImageKey string = "infraImageKey"

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
