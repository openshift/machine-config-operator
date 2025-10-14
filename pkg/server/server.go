package server

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/clarketm/json"
	"github.com/coreos/go-semver/semver"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/vincent-petithory/dataurl"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
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
type appenderFunc func(*ign3types.Config, *mcfgv1.MachineConfig) error

// Server defines the interface that is implemented by different
// machine config server implementations.
type Server interface {
	GetConfig(poolRequest) (*runtime.RawExtension, error)
}

func getAppenders(currMachineConfig string, version *semver.Version, f kubeconfigFunc, certs []string, serverDir string) []appenderFunc {
	appenders := []appenderFunc{
		// append machine annotations file.
		func(cfg *ign3types.Config, _ *mcfgv1.MachineConfig) error {
			return appendNodeAnnotations(cfg, currMachineConfig, "")
		},
		// append kubeconfig.
		func(cfg *ign3types.Config, _ *mcfgv1.MachineConfig) error { return appendKubeConfig(cfg, f) },
		// append the machineconfig content
		appendInitialMachineConfig,
		func(cfg *ign3types.Config, _ *mcfgv1.MachineConfig) error { return appendCerts(cfg, certs, serverDir) },
		// This has to come last!!!
		func(cfg *ign3types.Config, mc *mcfgv1.MachineConfig) error {
			return appendEncapsulated(cfg, mc, version)
		},
	}
	return appenders
}

func appendCerts(cfg *ign3types.Config, certs []string, serverDir string) error {
	for _, cert := range certs {
		keyValue := strings.Split(cert, "=")
		if len(keyValue) != 2 {
			return fmt.Errorf("could not use cert, missing key or value %s", cert)
		}
		data, err := os.ReadFile(filepath.Join(serverDir, keyValue[1]))
		if err != nil {
			return fmt.Errorf("could not read cert file %w", err)
		}
		appendFileToIgnition(cfg, filepath.Join("/etc/docker/certs.d", keyValue[0], "ca.crt"), string(data))
	}
	return nil
}

// appendEncapsulated empties out the ignition portion of a MachineConfig and adds
// it to /etc/ignition-machine-config-encapsulated.json.  This is used by
// machine-config-daemon-firstboot.service to process the bits that the main Ignition (that runs in the initramfs)
// didn't handle such as kernel arguments.
func appendEncapsulated(conf *ign3types.Config, mc *mcfgv1.MachineConfig, version *semver.Version) error {
	var rawTmpIgnCfg []byte
	var err error
	// In order to handle old RHCOS versions with the MCD baked in (i.e. before the MCS has
	// https://github.com/openshift/machine-config-operator/pull/1766 )
	// we need to ensure that the Ignition version we're putting in here is compatible.
	// It's kind of silly because there isn't *actually* an Ignition config here,
	// we're just adding a version to make it be a valid MachineConfig which currently
	// requires an empty Ignition version.
	if version == nil || version.Slice()[0] == 3 {
		tmpIgnCfg := ctrlcommon.NewIgnConfig()
		rawTmpIgnCfg, err = json.Marshal(tmpIgnCfg)
		if err != nil {
			return fmt.Errorf("error marshalling Ignition config: %w", err)
		}
	} else {
		tmpIgnCfg := ign2types.Config{
			Ignition: ign2types.Ignition{
				Version: ign2types.MaxVersion.String(),
			},
		}
		rawTmpIgnCfg, err = json.Marshal(tmpIgnCfg)
		if err != nil {
			return fmt.Errorf("error marshalling Ignition config: %w", err)
		}
	}

	tmpcfg := mc.DeepCopy()
	tmpcfg.Spec.Config.Raw = rawTmpIgnCfg

	serialized, err := json.Marshal(tmpcfg)
	if err != nil {
		return fmt.Errorf("error marshalling MachineConfig: %w", err)
	}
	err = appendFileToIgnition(conf, daemonconsts.MachineConfigEncapsulatedPath, string(serialized))
	if err != nil {
		return fmt.Errorf("error appending file to raw Ignition config: %w", err)
	}
	return nil
}

// appendInitialMachineConfig saves the full serialized MachineConfig that was served
// by the MCS when the node first booted.  This currently is only used as a debugging aid
// in cases where there is unexpected "drift" between the initial bootstrap MC/Ignition and the one
// computed by the cluster.
func appendInitialMachineConfig(conf *ign3types.Config, mc *mcfgv1.MachineConfig) error {
	mcJSON, err := json.MarshalIndent(mc, "", "    ")
	if err != nil {
		return err
	}
	appendFileToIgnition(conf, machineConfigContentPath, string(mcJSON))
	return nil
}

func appendKubeConfig(conf *ign3types.Config, f kubeconfigFunc) error {
	if f == nil {
		return nil
	}
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

func appendNodeAnnotations(conf *ign3types.Config, currConf, image string) error {
	anno, err := getNodeAnnotation(currConf, image)
	if err != nil {
		return err
	}
	err = appendFileToIgnition(conf, daemonconsts.InitialNodeAnnotationsFilePath, anno)
	if err != nil {
		return err
	}
	return nil
}

func getNodeAnnotation(conf, image string) (string, error) {
	nodeAnnotations := map[string]string{
		daemonconsts.CurrentMachineConfigAnnotationKey:     conf,
		daemonconsts.DesiredMachineConfigAnnotationKey:     conf,
		daemonconsts.MachineConfigDaemonStateAnnotationKey: daemonconsts.MachineConfigDaemonStateDone,
	}
	// If image is provided, include image annotations
	if image != "" {
		nodeAnnotations[daemonconsts.CurrentImageAnnotationKey] = image
		nodeAnnotations[daemonconsts.DesiredImageAnnotationKey] = image
	}
	contents, err := json.Marshal(nodeAnnotations)
	if err != nil {
		return "", fmt.Errorf("could not marshal node annotations, err: %w", err)
	}
	return string(contents), nil
}

func appendFileToIgnition(conf *ign3types.Config, outPath, contents string) error {
	fileMode := int(420)
	overwrite := true
	source := getEncodedContent(contents)
	file := ign3types.File{
		Node: ign3types.Node{
			Path:      outPath,
			Overwrite: &overwrite,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Contents: ign3types.Resource{
				Source: &source,
			},
			Mode: &fileMode,
		},
	}
	if len(conf.Storage.Files) == 0 {
		conf.Storage.Files = make([]ign3types.File, 0)
	}
	conf.Storage.Files = append(conf.Storage.Files, file)
	return nil
}

func getEncodedContent(inp string) string {
	return (&url.URL{
		Scheme: "data",
		Opaque: "," + dataurl.Escape([]byte(inp)),
	}).String()
}

// MigrateKernelArgsIfNecessary moves the kernel arguments back into MachineConfig when going from a version that supports
// ignition kernel arguments to a version that does not. Without this, we would be unable to serve anything < 3.3 because the
// ignition converter fails to downconvert if unsupported fields are populated. If ShouldNotExist is populated in the
// ignition kernel args, we still have to fail because there is nowhere in MachineConfig for us to put those.
func MigrateKernelArgsIfNecessary(conf *ign3types.Config, mc *mcfgv1.MachineConfig, version *semver.Version) error {
	// If we're downgrading from a version of ignition that
	// support kargs, we need to stuff them back into MachineConfig so they still end up in the encapsulation
	// and they still get applied
	if version != nil && version.LessThan(*semver.New("3.3.0")) {
		// we're converting to a version that doesn't support kargs, so we need to stuff them in machineconfig
		if len(conf.KernelArguments.ShouldNotExist) > 0 {
			return fmt.Errorf("Can't serve version %s with ignition KernelArguments.ShouldNotExist populated", version)
		}

		for _, karg := range conf.KernelArguments.ShouldExist {
			// TODO(jkyros): we probably need to parse/split them because they might be combined
			mc.Spec.KernelArguments = append(mc.Spec.KernelArguments, string(karg))
		}
		// and then we take them out of ignition
		conf.KernelArguments = ign3types.KernelArguments{}
	}
	return nil
}

func addDataAndMaybeAppendToIgnition(path string, data []byte, ignConf *ign3types.Config) {
	exists := false
	for idx, file := range ignConf.Storage.Files {
		if file.Path == path {
			exists = true
			d := getEncodedContent(string(data))
			if len(data) > 0 {
				ignConf.Storage.Files[idx].Contents.Source = &d
			}
			break
		}
	}
	if !exists && len(data) > 0 {
		appendFileToIgnition(ignConf, path, (string(data)))
	}
}
