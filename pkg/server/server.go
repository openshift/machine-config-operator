package server

import (
	"encoding/json"
	"fmt"
	"net/url"

	igntypes "github.com/coreos/ignition/v2/config/v3_0/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/vincent-petithory/dataurl"
)

const (
	// defaultMachineKubeConfPath defines the default location
	// of the KubeConfig file on the machine.
	defaultMachineKubeConfPath = "/etc/kubernetes/kubeconfig"

	// defaultFileSystem defines the default file system to be
	// used for writing the ignition files created by the
	// server.
	defaultFileSystem = "root"
)

// kubeconfigFunc fetches the kubeconfig that needs to be served.
type kubeconfigFunc func() (kubeconfigData []byte, rootCAData []byte, err error)

// appenderFunc appends Config.
type appenderFunc func(*igntypes.Config) error

// Server defines the interface that is implemented by different
// machine config server implementations.
type Server interface {
	GetConfig(poolRequest) (*igntypes.Config, error)
}

func getAppenders(currMachineConfig string, f kubeconfigFunc, osimageurl string) []appenderFunc {
	appenders := []appenderFunc{
		// append machine annotations file.
		func(config *igntypes.Config) error { return appendNodeAnnotations(config, currMachineConfig) },
		// append pivot
		func(config *igntypes.Config) error { return appendInitialPivot(config, osimageurl) },
		// append kubeconfig.
		func(config *igntypes.Config) error { return appendKubeConfig(config, f) },
	}
	return appenders
}

// machineConfigToIgnition converts a MachineConfig object into Ignition.
func machineConfigToIgnition(mccfg *mcfgv1.MachineConfig) *igntypes.Config {
	tmpcfg := mccfg.DeepCopy()
	tmpcfg.Spec.Config = ctrlcommon.NewIgnConfig()
	serialized, err := json.Marshal(tmpcfg)
	serializedString := string(serialized)
	if err != nil {
		panic(err.Error())
	}
	appendFileToIgnition(&mccfg.Spec.Config, daemonconsts.MachineConfigEncapsulatedPath, &serializedString)
	return &mccfg.Spec.Config
}

// Golang :cry:
func boolToPtr(b bool) *bool {
	return &b
}

func appendInitialPivot(conf *igntypes.Config, osimageurl string) error {
	if osimageurl == "" {
		return nil
	}

	// Tell pivot.service to pivot early
	localosimageurl := osimageurl + "\n"
	appendFileToIgnition(conf, daemonconsts.EtcPivotFile, &localosimageurl)
	// Awful hack to create a file in /run
	// https://github.com/openshift/machine-config-operator/pull/363#issuecomment-463397373
	// "So one gotcha here is that Ignition will actually write `/run/pivot/image-pullspec` to the filesystem rather than the `/run` tmpfs"
	if len(conf.Systemd.Units) == 0 {
		conf.Systemd.Units = make([]igntypes.Unit, 0)
	}
	pivotUnit := `[Unit]
Before=pivot.service
ConditionFirstBoot=true
[Service]
ExecStart=/bin/sh -c 'mkdir /run/pivot && touch /run/pivot/reboot-needed'
[Install]
WantedBy=multi-user.target
`
	unit := igntypes.Unit{
		Name:     "mcd-write-pivot-reboot.service",
		Enabled:  boolToPtr(true),
		Contents: &pivotUnit}
	conf.Systemd.Units = append(conf.Systemd.Units, unit)
	return nil
}

func appendKubeConfig(conf *igntypes.Config, f kubeconfigFunc) error {
	kcData, _, err := f()
	if err != nil {
		return err
	}
	kcDataString := string(kcData)
	appendFileToIgnition(conf, defaultMachineKubeConfPath, &kcDataString)
	return nil
}

func appendNodeAnnotations(conf *igntypes.Config, currConf string) error {
	anno, err := getNodeAnnotation(currConf)
	if err != nil {
		return err
	}
	appendFileToIgnition(conf, daemonconsts.InitialNodeAnnotationsFilePath, &anno)
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

func appendFileToIgnition(conf *igntypes.Config, outPath string, contents *string) {
	fileMode := int(420)
	overwrite := true
	source := getEncodedContent(contents)
	file := igntypes.File{
		Node: igntypes.Node{
			Path:      outPath,
			Overwrite: &overwrite,
		},
		FileEmbedded1: igntypes.FileEmbedded1{
			Contents: igntypes.FileContents{
				Source: &source,
			},
			Mode: &fileMode,
		},
	}
	if len(conf.Storage.Files) == 0 {
		conf.Storage.Files = make([]igntypes.File, 0)
	}
	conf.Storage.Files = append(conf.Storage.Files, file)
}

func getDecodedContent(inp *string) (string, error) {
	d, err := dataurl.DecodeString(*inp)
	if err != nil {
		return "", err
	}

	return string(d.Data), nil
}

func getEncodedContent(inp *string) string {
	return (&url.URL{
		Scheme: "data",
		Opaque: "," + dataurl.Escape([]byte(*inp)),
	}).String()
}
