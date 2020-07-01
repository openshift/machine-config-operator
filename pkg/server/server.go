package server

import (
	"fmt"
	"net/url"

	"github.com/clarketm/json"
	igntypes "github.com/coreos/ignition/v2/config/v3_1/types"
	"github.com/vincent-petithory/dataurl"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

const (
	// defaultMachineKubeConfPath defines the default location
	// of the KubeConfig file on the machine.
	defaultMachineKubeConfPath = "/etc/kubernetes/kubeconfig"

	// machineConfigContentPath contains the raw machine-config content
	// served by the MCS. This is extremely useful when debugging drifts
	// between the installer and the MCO itself.
	// This is different than /etc/machine-config-daemon/currentconfig written
	// by the MCD but it's gonna be the same 99% of the time. The reason we
	// need this is that on bootstrap + first install we don't have the MCD
	// running and writing that file.
	machineConfigContentPath = "/etc/mcs-machine-config-content.json"
)

// kubeconfigFunc fetches the kubeconfig that needs to be served.
type kubeconfigFunc func() (kubeconfigData []byte, rootCAData []byte, err error)

// appenderFunc appends Config.
type appenderFunc func(*mcfgv1.MachineConfig) error

// Server defines the interface that is implemented by different
// machine config server implementations.
type Server interface {
	GetConfig(poolRequest) (*runtime.RawExtension, error)
}

func getAppenders(currMachineConfig string, f kubeconfigFunc) []appenderFunc {
	appenders := []appenderFunc{
		// append machine annotations file.
		func(mc *mcfgv1.MachineConfig) error { return appendNodeAnnotations(&mc.Spec.Config, currMachineConfig) },
		// append kubeconfig.
		func(mc *mcfgv1.MachineConfig) error { return appendKubeConfig(&mc.Spec.Config, f) },
		// append the machineconfig content
		//nolint:gocritic
		func(mc *mcfgv1.MachineConfig) error { return appendInitialMachineConfig(mc) },
	}
	return appenders
}

// machineConfigToRawIgnition converts a MachineConfig object into raw Ignition.
func machineConfigToRawIgnition(mccfg *mcfgv1.MachineConfig) (*runtime.RawExtension, error) {
	tmpcfg := mccfg.DeepCopy()
	tmpIgnCfg := ctrlcommon.NewIgnConfig()
	rawTmpIgnCfg, err := json.Marshal(tmpIgnCfg)
	if err != nil {
		return nil, fmt.Errorf("error marshalling Ignition config: %v", err)
	}
	tmpcfg.Spec.Config.Raw = rawTmpIgnCfg
	serialized, err := json.Marshal(tmpcfg)
	if err != nil {
		return nil, fmt.Errorf("error marshalling MachineConfig: %v", err)
	}
	err = appendFileToRawIgnition(&mccfg.Spec.Config, daemonconsts.MachineConfigEncapsulatedPath, string(serialized))
	if err != nil {
		return nil, fmt.Errorf("error appending file to raw Ignition config: %v", err)
	}
	return &mccfg.Spec.Config, nil
}

// appendInitialMachineConfig saves the full serialized MachineConfig that was served
// by the MCS when the node first booted.  This currently is only used as a debugging aid
// in cases where there is unexpected "drift" between the initial bootstrap MC/Ignition and the one
// computed by the cluster.
func appendInitialMachineConfig(mc *mcfgv1.MachineConfig) error {
	mcJSON, err := json.MarshalIndent(mc, "", "    ")
	if err != nil {
		return err
	}
	appendFileToRawIgnition(&mc.Spec.Config, machineConfigContentPath, string(mcJSON))
	return nil
}

func appendKubeConfig(rawExt *runtime.RawExtension, f kubeconfigFunc) error {
	kcData, _, err := f()
	if err != nil {
		return err
	}
	err = appendFileToRawIgnition(rawExt, defaultMachineKubeConfPath, string(kcData))
	if err != nil {
		return err
	}
	return nil
}

func appendNodeAnnotations(rawExt *runtime.RawExtension, currConf string) error {

	anno, err := getNodeAnnotation(currConf)
	if err != nil {
		return err
	}
	err = appendFileToRawIgnition(rawExt, daemonconsts.InitialNodeAnnotationsFilePath, anno)
	if err != nil {
		return err
	}
	return nil
}

func getNodeAnnotation(conf string) (string, error) {
	nodeAnnotations := map[string]string{
		daemonconsts.CurrentMachineConfigAnnotationKey:     conf,
		daemonconsts.DesiredMachineConfigAnnotationKey:     conf,
		daemonconsts.MachineConfigDaemonStateAnnotationKey: daemonconsts.MachineConfigDaemonStateDone,
	}
	contents, err := json.Marshal(nodeAnnotations)
	if err != nil {
		return "", fmt.Errorf("could not marshal node annotations, err: %v", err)
	}
	return string(contents), nil
}

func appendFileToRawIgnition(rawExt *runtime.RawExtension, outPath, contents string) error {
	conf, err := ctrlcommon.IgnParseWrapper(rawExt.Raw)
	if err != nil {
		return fmt.Errorf("failed to append file. Parsing Ignition config failed with error: %v", err)
	}
	fileMode := int(420)
	file := igntypes.File{
		Node: igntypes.Node{
			Path: outPath,
		},
		FileEmbedded1: igntypes.FileEmbedded1{
			Contents: igntypes.Resource{
				Source: ctrlcommon.StrToPtr(getEncodedContent(contents)),
			},
			Mode: &fileMode,
		},
	}
	if len(conf.Storage.Files) == 0 {
		conf.Storage.Files = make([]igntypes.File, 0)
	}
	conf.Storage.Files = append(conf.Storage.Files, file)
	rawExt.Raw, err = json.Marshal(conf)
	if err != nil {
		return err
	}
	return nil
}

func getDecodedContent(inp string) (string, error) {
	d, err := dataurl.DecodeString(inp)
	if err != nil {
		return "", err
	}

	return string(d.Data), nil
}

func getEncodedContent(inp string) string {
	return (&url.URL{
		Scheme: "data",
		Opaque: "," + dataurl.Escape([]byte(inp)),
	}).String()
}
