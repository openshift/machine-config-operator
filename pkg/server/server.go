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
type appenderFunc func(*igntypes.Config, *mcfgv1.MachineConfig) error

// Server defines the interface that is implemented by different
// machine config server implementations.
type Server interface {
	GetConfig(poolRequest) (*runtime.RawExtension, error)
}

func getAppenders(currMachineConfig string, f kubeconfigFunc) []appenderFunc {
	appenders := []appenderFunc{
		// append machine annotations file.
		func(cfg *igntypes.Config, mc *mcfgv1.MachineConfig) error {
			return appendNodeAnnotations(cfg, currMachineConfig)
		},
		// append kubeconfig.
		func(cfg *igntypes.Config, mc *mcfgv1.MachineConfig) error { return appendKubeConfig(cfg, f) },
		// append the machineconfig content
		appendInitialMachineConfig,
		// This has to come last!!!
		appendEncapsulated,
	}
	return appenders
}

func appendEncapsulated(conf *igntypes.Config, mc *mcfgv1.MachineConfig) error {
	// since we're encapsulating the MC, we don't need the whole ignition section, that's why
	// we do this dance here to empty out the ignition section itself. We're just interested into the MC fields
	tmpIgnCfg := ctrlcommon.NewIgnConfig()
	rawTmpIgnCfg, err := json.Marshal(tmpIgnCfg)
	if err != nil {
		return fmt.Errorf("error marshalling Ignition config: %v", err)
	}
	tmpcfg := mc.DeepCopy()
	tmpcfg.Spec.Config.Raw = rawTmpIgnCfg
	serialized, err := json.Marshal(tmpcfg)
	if err != nil {
		return fmt.Errorf("error marshalling MachineConfig: %v", err)
	}
	err = appendFileToIgnition(conf, daemonconsts.MachineConfigEncapsulatedPath, string(serialized))
	if err != nil {
		return fmt.Errorf("error appending file to raw Ignition config: %v", err)
	}
	return nil
}

// appendInitialMachineConfig saves the full serialized MachineConfig that was served
// by the MCS when the node first booted.  This currently is only used as a debugging aid
// in cases where there is unexpected "drift" between the initial bootstrap MC/Ignition and the one
// computed by the cluster.
func appendInitialMachineConfig(conf *igntypes.Config, mc *mcfgv1.MachineConfig) error {
	mcJSON, err := json.MarshalIndent(mc, "", "    ")
	if err != nil {
		return err
	}
	appendFileToIgnition(conf, machineConfigContentPath, string(mcJSON))
	return nil
}

func appendKubeConfig(conf *igntypes.Config, f kubeconfigFunc) error {
	kcData, _, err := f()
	if err != nil {
		return err
	}
	err = appendFileToIgnition(conf, defaultMachineKubeConfPath, string(kcData))
	if err != nil {
		return err
	}
	return nil
}

func appendNodeAnnotations(conf *igntypes.Config, currConf string) error {
	anno, err := getNodeAnnotation(currConf)
	if err != nil {
		return err
	}
	err = appendFileToIgnition(conf, daemonconsts.InitialNodeAnnotationsFilePath, anno)
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

func appendFileToIgnition(conf *igntypes.Config, outPath, contents string) error {
	fileMode := int(420)
	overwrite := true
	source := getEncodedContent(contents)
	file := igntypes.File{
		Node: igntypes.Node{
			Path:      outPath,
			Overwrite: &overwrite,
		},
		FileEmbedded1: igntypes.FileEmbedded1{
			Contents: igntypes.Resource{
				Source: &source,
			},
			Mode: &fileMode,
		},
	}
	if len(conf.Storage.Files) == 0 {
		conf.Storage.Files = make([]igntypes.File, 0)
	}
	conf.Storage.Files = append(conf.Storage.Files, file)
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
