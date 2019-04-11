package template

const (
	// EtcdImageKey is the key that references the etcd image in the controller
	EtcdImageKey string = "etcd"

	// SetupEtcdEnvKey is the key that references the setup-etcd-environment image in the controller
	SetupEtcdEnvKey string = "setupEtcdEnv"

	// InfraImageKey is the key that references the infra image in the controller for crio.conf
	InfraImageKey string = "infraImage"

	// KubeClientAgentImageKey is the key that references the kube-client-agent image in the controller
	KubeClientAgentImageKey string = "kubeClientAgentImage"

	// EtcdQuorumGuardImageKey is the key that references the etcd-quorum-guard image
	EtcdQuorumGuardImageKey string = "etcdQuorumGuardImage"
)
