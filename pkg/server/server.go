package server

import (
	"encoding/json"
	"fmt"
	"net/url"

	ign "github.com/coreos/ignition/config/v2_2"
	igntypes "github.com/coreos/ignition/config/v2_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/vincent-petithory/dataurl"
	"k8s.io/apimachinery/pkg/runtime"
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

	// defaultFileSystem defines the default file system to be
	// used for writing the ignition files created by the
	// server.
	defaultFileSystem = "root"
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

func getAppenders(currMachineConfig string, f kubeconfigFunc, osimageurl string) []appenderFunc {
	appenders := []appenderFunc{
		// append machine annotations file.
		func(mc *mcfgv1.MachineConfig) error { return appendNodeAnnotations(&mc.Spec.Config, currMachineConfig) },
		// append pivot
		func(mc *mcfgv1.MachineConfig) error { return appendInitialPivot(&mc.Spec.Config, osimageurl) },
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

// Golang :cry:
func boolToPtr(b bool) *bool {
	return &b
}

func appendInitialPivot(rawExt *runtime.RawExtension, osimageurl string) error {
	if osimageurl == "" {
		return nil
	}

	// Tell pivot.service to pivot early
	err := appendFileToRawIgnition(rawExt, daemonconsts.EtcPivotFile, osimageurl+"\n")
	if err != nil {
		return err
	}
	conf, report, err := ign.Parse(rawExt.Raw)
	if err != nil {
		return fmt.Errorf("failed to append initial pivot. Parsing Ignition config failed with error: %v\nReport: %v", err, report)
	}
	// Awful hack to create a file in /run
	// https://github.com/openshift/machine-config-operator/pull/363#issuecomment-463397373
	// "So one gotcha here is that Ignition will actually write `/run/pivot/image-pullspec` to the filesystem rather than the `/run` tmpfs"
	if len(conf.Systemd.Units) == 0 {
		conf.Systemd.Units = make([]igntypes.Unit, 0)
	}
	unit := igntypes.Unit{
		Name:    "mcd-write-pivot-reboot.service",
		Enabled: boolToPtr(true),
		Contents: `[Unit]
Before=pivot.service
ConditionFirstBoot=true
[Service]
ExecStart=/bin/sh -c 'mkdir /run/pivot && touch /run/pivot/reboot-needed'
[Install]
WantedBy=multi-user.target
`}
	conf.Systemd.Units = append(conf.Systemd.Units, unit)
	rawExt.Raw, err = json.Marshal(conf)
	if err != nil {
		return err
	}
	return nil
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
	conf, report, err := ign.Parse(rawExt.Raw)
	if err != nil {
		return fmt.Errorf("failed to append file. Parsing Ignition config failed with error: %v\nReport: %v", err, report)
	}
	fileMode := int(420)
	file := igntypes.File{
		Node: igntypes.Node{
			Filesystem: defaultFileSystem,
			Path:       outPath,
		},
		FileEmbedded1: igntypes.FileEmbedded1{
			Contents: igntypes.FileContents{
				Source: getEncodedContent(contents),
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
