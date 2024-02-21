package template

const (
	// MachineConfigOperatorKey is our own image used by e.g. machine-config-daemon-pull.service
	MachineConfigOperatorKey string = "machineConfigOperator"

	// APIServerWatcherKey is the key that references the apiserver-watcher image
	APIServerWatcherKey string = "apiServerWatcherKey"

	// InfraImageKey is the key that references the infra image in the controller for crio.conf
	InfraImageKey string = "infraImageKey"

	// KeepalivedKey is the key that references the keepalived-ipfailover image in the controller
	KeepalivedKey string = "keepalivedImage"

	// CorednsKey is the key that references the coredns image in the controller
	CorednsKey string = "corednsImage"

	// HaproxyKey is the key that references the haproxy-router image in the controller
	HaproxyKey string = "haproxyImage"

	// BaremetalRuntimeCfgKey is the key that references the baremetal-runtimecfg image in the controller
	BaremetalRuntimeCfgKey string = "baremetalRuntimeCfgImage"

	// KubeRbacProxyKey the key that references the kubeRbacProxy image
	KubeRbacProxyKey string = "kubeRbacProxyImage"
)
