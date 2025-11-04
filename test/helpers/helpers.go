package helpers

import (
	"fmt"

	"github.com/clarketm/json"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

var (
	// MasterSelector returns a label selector for masters nodes
	MasterSelector = metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/master", "")
	// WorkerSelector returns a label selector for workers nodes
	WorkerSelector = metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/worker", "")
	// InfraSelector returns a label selector for infra nodes
	InfraSelector = metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/infra", "")
)

// StrToPtr returns a pointer to a string
func StrToPtr(s string) *string {
	return &s
}

// BoolToPtr returns a pointer to a bool
func BoolToPtr(b bool) *bool {
	return &b
}

// IntToPtr returns a pointer to an int
func IntToPtr(i int) *int {
	return &i
}

// NewMachineConfig returns a basic machine config with supplied labels, osurl & files added
func NewMachineConfig(name string, labels map[string]string, osurl string, files []ign3types.File) *mcfgv1.MachineConfig {
	return NewMachineConfigExtended(
		name,
		labels,
		nil,
		files,
		[]ign3types.Unit{},
		[]ign3types.SSHAuthorizedKey{},
		[]string{},
		false,
		[]string{},
		"",
		osurl,
	)
}

// NewMachineConfig returns a basic machine config with supplied labels, osurl & files added
func NewMachineConfigWithAnnotation(name string, labels, annotations map[string]string, osurl string, files []ign3types.File) *mcfgv1.MachineConfig {
	return NewMachineConfigExtended(
		name,
		labels,
		annotations,
		files,
		[]ign3types.Unit{},
		[]ign3types.SSHAuthorizedKey{},
		[]string{},
		false,
		[]string{},
		"",
		osurl,
	)
}

// NewMachineConfigExtended returns a more comprehensive machine config
func NewMachineConfigExtended(
	name string,
	labels map[string]string,
	annotations map[string]string,
	files []ign3types.File,
	units []ign3types.Unit,
	sshkeys []ign3types.SSHAuthorizedKey,
	extensions []string,
	fips bool,
	kernelArguments []string,
	kernelType, osurl string,
) *mcfgv1.MachineConfig {
	if labels == nil {
		labels = map[string]string{}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}

	ignConfig := &ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Storage: ign3types.Storage{
			Files: files,
		},
		Systemd: ign3types.Systemd{
			Units: units,
		},
		Passwd: ign3types.Passwd{},
	}

	if len(sshkeys) != 0 {
		ignConfig.Passwd = ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				{
					Name:              "core",
					SSHAuthorizedKeys: sshkeys,
				},
			},
		}
	}

	return &mcfgv1.MachineConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: mcfgv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
			UID:         types.UID(utilrand.String(5)),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: MarshalOrDie(ignConfig),
			},
			Extensions:      extensions,
			FIPS:            fips,
			KernelArguments: kernelArguments,
			KernelType:      kernelType,
			OSImageURL:      osurl,
		},
	}
}

// NewMachineConfigPool returns a MCP with supplied mcSelector, nodeSelector and machineconfig
func NewMachineConfigPool(name string, mcSelector, nodeSelector *metav1.LabelSelector, currentMachineConfig string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		TypeMeta: metav1.TypeMeta{
			APIVersion: mcfgv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"machineconfiguration.openshift.io/mco-built-in":                         "",
				fmt.Sprintf("pools.operator.machineconfiguration.openshift.io/%s", name): "",
			},
			UID: types.UID(utilrand.String(5)),
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			NodeSelector:          nodeSelector,
			MachineConfigSelector: mcSelector,
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{
					Name: currentMachineConfig,
				},
			},
		},
		Status: mcfgv1.MachineConfigPoolStatus{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{
					Name: currentMachineConfig,
				},
			},
			Conditions: []mcfgv1.MachineConfigPoolCondition{
				{
					Type:               mcfgv1.MachineConfigPoolRenderDegraded,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.Unix(0, 0),
					Reason:             "",
					Message:            "",
				},
				{
					Type:               mcfgv1.MachineConfigPoolPinnedImageSetsDegraded,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.Unix(0, 0),
					Reason:             "",
					Message:            "",
				},
			},
		},
	}
}

func NewOpaqueSecret(name, namespace, content string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"entitlement-key.pem": []byte(content),
			"entitlement.pem":     []byte(content),
		},
		Type: corev1.SecretTypeOpaque,
	}
}
func NewOpaqueSecretWithOwnerPool(name, namespace, content string, pool mcfgv1.MachineConfigPool) *corev1.Secret {
	// Work around https://github.com/kubernetes/kubernetes/issues/3030 and https://github.com/kubernetes/kubernetes/issues/80609
	pool.APIVersion = mcfgv1.GroupVersion.String()
	pool.Kind = "MachineConfigPool"
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: pool.APIVersion,
					Kind:       pool.Kind,
					Name:       pool.ObjectMeta.Name,
					UID:        pool.ObjectMeta.UID,
				},
			},
		},
		Data: map[string][]byte{
			"entitlement-key.pem": []byte(content),
			"entitlement.pem":     []byte(content),
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func NewDockerCfgJSONSecret(name, namespace, content string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(content),
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}
}

// CreateMachineConfigFromIgnitionWithMetadata returns a MachineConfig object from an Ignition config, name, and role label
func CreateMachineConfigFromIgnitionWithMetadata(ignCfg interface{}, name, role string) *mcfgv1.MachineConfig {
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"machineconfiguration.openshift.io/role": role},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: MarshalOrDie(ignCfg),
			},
		},
	}
}

// CreateMachineConfigFromIgnition returns a MachineConfig object from an Ignition config passed to it
func CreateMachineConfigFromIgnition(ignCfg interface{}) *mcfgv1.MachineConfig {
	return &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: MarshalOrDie(ignCfg),
			},
		},
	}
}

// MarshalOrDie returns a marshalled interface or panics
func MarshalOrDie(input interface{}) []byte {
	bytes, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return bytes
}

// NewNodeWithReady creates a new node with the specified configuration and ready status
func NewNodeWithReady(name, currentConfig, desiredConfig string, status corev1.ConditionStatus) *corev1.Node {
	nb := NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithStatus(corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}})
	return nb.Node()
}

// NewLayeredNodeWithReady creates a new layered node with the specified configuration, images, and ready status
func NewLayeredNodeWithReady(name, currentConfig, desiredConfig, currentImage, desiredImage string, status corev1.ConditionStatus) *corev1.Node {
	nb := NewNodeBuilder(name)
	nb.WithCurrentConfig(currentConfig)
	nb.WithDesiredConfig(desiredConfig)
	nb.WithCurrentImage(currentImage)
	nb.WithDesiredImage(desiredImage)
	nb.WithStatus(corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}})
	return nb.Node()
}

// GetNamesFromNodes extracts node names from a slice of nodes
func GetNamesFromNodes(nodes []*corev1.Node) []string {
	// When there are no nodes, return an empty
	if len(nodes) == 0 {
		return []string{}
	}

	// Loop through the nodes to return a list of their names.
	names := make([]string, len(nodes))
	for i, node := range nodes {
		names[i] = node.Name
	}
	return names
}
