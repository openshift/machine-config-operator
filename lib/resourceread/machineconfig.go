package resourceread

import (
	"errors"
	"fmt"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	mcfgScheme = runtime.NewScheme()
	mcfgCodecs = serializer.NewCodecFactory(mcfgScheme)
)

func init() {
	if err := mcfgv1.AddToScheme(mcfgScheme); err != nil {
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
		return nil, fmt.Errorf("failed to decode raw bytes to mcfgv1.SchemeGroupVersion: %v", err)
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

// ReadRenderedMachineConfigV1 reads raw RenderedMachineConfig object from bytes. Returns RenderedMachineConfig and error.
func ReadRenderedMachineConfigV1(objBytes []byte) (*mcfgv1.RenderedMachineConfig, error) {
	if objBytes == nil {
		return nil, errors.New("invalid machine configuration")
	}

	m, err := runtime.Decode(mcfgCodecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), objBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to decode raw bytes to mcfgv1.SchemeGroupVersion: %v", err)
	}
	if m == nil {
		return nil, fmt.Errorf("expected mcfgv1.SchemeGroupVersion but got nil")
	}

	mc, ok := m.(*mcfgv1.RenderedMachineConfig)
	if !ok {
		return nil, fmt.Errorf("expected *mcfvgv1.RenderedMachineConfig but found %T", m)
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

// ReadRenderedMachineConfigV1OrDie reads raw  RenderedMachineConfig object from bytes. Panics on error.
func ReadRenderedMachineConfigV1OrDie(objBytes []byte) *mcfgv1.RenderedMachineConfig {
	mc, err := ReadRenderedMachineConfigV1(objBytes)
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

// ReadControllerConfigV1OrDie reads ControllerConfig object from bytes. Panics on error.
func ReadControllerConfigV1OrDie(objBytes []byte) *mcfgv1.ControllerConfig {
	requiredObj, err := runtime.Decode(mcfgCodecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*mcfgv1.ControllerConfig)
}
