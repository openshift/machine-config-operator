package fixtures

import (
	"bytes"
	"compress/gzip"
	"fmt"

	"github.com/clarketm/json"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/vincent-petithory/dataurl"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilrand "k8s.io/apimachinery/pkg/util/rand"

	coreosutils "github.com/coreos/ignition/config/util"
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

// Creates an Ign3 file whose contents are gzipped and encoded according to
// https://datatracker.ietf.org/doc/html/rfc2397
func CreateGzippedIgn3File(path, content string, mode int) (ign3types.File, error) {
	ign3File := ign3types.File{}

	buf := bytes.NewBuffer([]byte{})

	gzipWriter := gzip.NewWriter(buf)
	if _, err := gzipWriter.Write([]byte(content)); err != nil {
		return ign3File, err
	}

	if err := gzipWriter.Close(); err != nil {
		return ign3File, err
	}

	ign3File = CreateEncodedIgn3File(path, buf.String(), mode)
	ign3File.Contents.Compression = coreosutils.StrToPtr("gzip")

	return ign3File, nil
}

// Creates an Ign3 file whose contents are encoded according to
// https://datatracker.ietf.org/doc/html/rfc2397
func CreateEncodedIgn3File(path, content string, mode int) ign3types.File {
	encoded := dataurl.EncodeBytes([]byte(content))

	return CreateIgn3File(path, encoded, mode)
}

func CreateIgn3File(path, content string, mode int) ign3types.File {
	return ign3types.File{
		FileEmbedded1: ign3types.FileEmbedded1{
			Contents: ign3types.Resource{
				Source: &content,
			},
			Mode: &mode,
		},
		Node: ign3types.Node{
			Path: path,
			User: ign3types.NodeUser{
				Name: coreosutils.StrToPtr("root"),
			},
		},
	}
}
