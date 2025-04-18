package resourceread

import (
	"errors"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgalphav1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	opv1 "github.com/openshift/api/operator/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	mcfgScheme = runtime.NewScheme()
	mcfgCodecs = serializer.NewCodecFactory(mcfgScheme)

	mcfgAlphaScheme = runtime.NewScheme()

	opv1Scheme = runtime.NewScheme()
	opv1Codec  = serializer.NewCodecFactory(opv1Scheme)
)

func init() {
	if err := mcfgalphav1.AddToScheme(mcfgAlphaScheme); err != nil {
		panic(err)
	}
	if err := mcfgv1.AddToScheme(mcfgScheme); err != nil {
		panic(err)
	}
	if err := opv1.AddToScheme(opv1Scheme); err != nil {
		panic(err)
	}
}

// ReadMachineConfigV1 reads raw MachineConfig object from bytes. Returns MachineConfig and error.
func ReadMachineConfigV1(objBytes []byte) (*mcfgv1.MachineConfig, error) {
	if objBytes == nil {
		return nil, errors.New("invalid machine configuration")
	}

	m, err := runtime.Decode(mcfgCodecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), objBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to decode raw bytes to mcfgv1.SchemeGroupVersion: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("expected mcfgv1.SchemeGroupVersion but got nil")
	}

	mc, ok := m.(*mcfgv1.MachineConfig)
	if !ok {
		return nil, fmt.Errorf("expected *mcfvgv1.MachineConfig but found %T", m)
	}

	return mc, nil
}

// ReadMachineConfigV1OrDie reads raw  MachineConfig object from bytes. Panics on error.
func ReadMachineConfigV1OrDie(objBytes []byte) *mcfgv1.MachineConfig {
	mc, err := ReadMachineConfigV1(objBytes)
	if err != nil {
		panic(err)
	}
	return mc
}

// ReadMachineConfigPoolV1OrDie reads MachineConfigPool object from bytes. Panics on error.
func ReadMachineConfigPoolV1OrDie(objBytes []byte) *mcfgv1.MachineConfigPool {
	requiredObj, err := runtime.Decode(mcfgCodecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*mcfgv1.MachineConfigPool)
}

// ReadMachineConfigPoolV1OrDie reads MachineConfigPool object from bytes. Panics on error.
func ReadMachineConfigNodeV1OrDie(objBytes []byte) *mcfgv1.MachineConfigNode {
	requiredObj, err := runtime.Decode(mcfgCodecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*mcfgv1.MachineConfigNode)
}

// ReadControllerConfigV1OrDie reads ControllerConfig object from bytes. Panics on error.
func ReadControllerConfigV1OrDie(objBytes []byte) *mcfgv1.ControllerConfig {
	requiredObj, err := runtime.Decode(mcfgCodecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*mcfgv1.ControllerConfig)
}

func ReadMachineConfigurationV1OrDie(objBytes []byte) *opv1.MachineConfiguration {
	requiredObj, err := runtime.Decode(opv1Codec.UniversalDecoder(opv1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*opv1.MachineConfiguration)
}
