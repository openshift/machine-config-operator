package resourceread

import (
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

// ReadMachineConfigV1OrDie reads MachineConfig object from bytes. Panics on error.
func ReadMachineConfigV1OrDie(objBytes []byte) *mcfgv1.MachineConfig {
	requiredObj, err := runtime.Decode(mcfgCodecs.UniversalDecoder(mcfgv1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*mcfgv1.MachineConfig)
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
