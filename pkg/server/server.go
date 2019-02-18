package server

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/url"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	daemonconsts "github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/vincent-petithory/dataurl"
)

const (
	// defaultMachineKubeConfPath defines the default location
	// of the KubeConfig file on the machine.
	defaultMachineKubeConfPath = "/etc/kubernetes/kubeconfig"

	// From https://github.com/openshift/pivot/pull/25/commits/c77788a35d7ee4058d1410e89e6c7937bca89f6c#diff-04c6e90faac2675aa89e2176d2eec7d8R44
	pivotRebootNeeded = "/run/pivot/reboot-needed"

	// defaultFileSystem defines the default file system to be
	// used for writing the ignition files created by the
	// server.
	defaultFileSystem = "root"
)

// kubeconfigFunc fetches the kubeconfig that needs to be served.
type kubeconfigFunc func() (kubeconfigData []byte, rootCAData []byte, err error)

// appenderFunc appends Config.
type appenderFunc func(*ignv2_2types.Config) error

// Server defines the interface that is implemented by different
// machine config server implementations.
type Server interface {
	GetConfig(poolRequest) (*ignv2_2types.Config, error)
}

func getAppenders(cr poolRequest, currMachineConfig string, f kubeconfigFunc, osimageurl string) []appenderFunc {
	appenders := []appenderFunc{
		// append machine annotations file.
		func(config *ignv2_2types.Config) error { return appendNodeAnnotations(config, currMachineConfig) },
		// append pivot
		func(config *ignv2_2types.Config) error { return appendInitialPivot(config, osimageurl) },
		// append kubeconfig.
		func(config *ignv2_2types.Config) error { return appendKubeConfig(config, f) },
	}
	return appenders
}

// Golang :cry:
func boolToPtr(b bool) *bool {
	return &b
}

func appendInitialPivot(conf *ignv2_2types.Config, osimageurl string) error {
	if osimageurl == "" {
		return nil
	}

	// Tell pivot.service to pivot early
	appendFileToIgnition(conf, daemonconsts.EtcPivotFile, osimageurl + "\n")
	// Awful hack to create a file in /run
	// https://github.com/openshift/machine-config-operator/pull/363#issuecomment-463397373
	// "So one gotcha here is that Ignition will actually write `/run/pivot/image-pullspec` to the filesystem rather than the `/run` tmpfs"
	if len(conf.Systemd.Units) == 0 {
		conf.Systemd.Units = make([]ignv2_2types.Unit, 0)
	}
	unit := ignv2_2types.Unit {
		Name: "mcd-write-pivot-reboot.service",
		Enabled: boolToPtr(true),
		Contents: `[Unit]
Before=pivot.service
ConditionFirstBoot=true
[Service]
ExecStart=/bin/sh -c 'mkdir /run/pivot && touch /run/pivot/reboot-needed'
[Install]
WantedBy=multi-user.target
`,}
	conf.Systemd.Units = append(conf.Systemd.Units, unit)
	return nil
}

func appendKubeConfig(conf *ignv2_2types.Config, f kubeconfigFunc) error {
	kcData, _, err := f()
	if err != nil {
		return err
	}
	appendFileToIgnition(conf, defaultMachineKubeConfPath, string(kcData))
	return nil
}

func appendNodeAnnotations(conf *ignv2_2types.Config, currConf string) error {
	anno, err := getNodeAnnotation(currConf)
	if err != nil {
		return err
	}
	appendFileToIgnition(conf, daemonconsts.InitialNodeAnnotationsFilePath, string(anno))
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

func copyFileToIgnition(conf *ignv2_2types.Config, outPath, srcPath string) error {
	contents, err := ioutil.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("could not read file from: %s, err: %v", srcPath, err)
	}
	appendFileToIgnition(conf, outPath, string(contents))
	return nil
}

func appendFileToIgnition(conf *ignv2_2types.Config, outPath, contents string) {
	fileMode := int(420)
	file := ignv2_2types.File{
		Node: ignv2_2types.Node{
			Filesystem: defaultFileSystem,
			Path:       outPath,
		},
		FileEmbedded1: ignv2_2types.FileEmbedded1{
			Contents: ignv2_2types.FileContents{
				Source: getEncodedContent(contents),
			},
			Mode: &fileMode,
		},
	}
	if len(conf.Storage.Files) == 0 {
		conf.Storage.Files = make([]ignv2_2types.File, 0)
	}
	conf.Storage.Files = append(conf.Storage.Files, file)
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
