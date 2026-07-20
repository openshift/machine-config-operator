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

	// FRRK8sKey is the key for the frr-k8s image used by the frr-k8s static pod.
	// A single image is used for all frr-k8s containers (controller, FRR daemon,
	// reloader, metrics, status) in OpenShift.
	FRRK8sKey string = "frrK8sImage"

	// KubeVIPKey is the key for the kube-vip image used by the kube-vip static pods.
	// kube-vip manages API and Ingress VIPs in Routing Table Mode for BGP-based
	// VIP management.
	KubeVIPKey string = "kubeVipImage"

	// KubeRbacProxyKey the key that references the kubeRbacProxy image
	KubeRbacProxyKey string = "kubeRbacProxyImage"

	// DockerRegistryKey is the key that references the docker-registry image in the controller
	DockerRegistryKey string = "dockerRegistryImage"
)
