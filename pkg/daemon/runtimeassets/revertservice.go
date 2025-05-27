package runtimeassets

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"sigs.k8s.io/yaml"
)

var _ RuntimeAsset = &revertService{}

const (
	RevertServiceName              string = "machine-config-daemon-revert.service"
	RevertServiceMachineConfigFile string = "/etc/mco/machineconfig-revert.json"
	RevertServiceProxyFile         string = "/etc/mco/proxy.env.backup"
)

//go:embed machine-config-daemon-revert.service.yaml
var mcdRevertServiceIgnYAML string

type revertService struct {
	// The MCO image pullspec that should be used.
	MCOImage string
	// Whether the proxy file exists and should be considered.
	Proxy bool
	// The current MachineConfig to write to disk.
	mc      *mcfgv1.MachineConfig
	ctrlcfg *mcfgv1.ControllerConfig
}

// Constructs a revertService instance from a ControllerConfig and
// MachineConfig. Returns an error if the provided ControllerConfig or
// MachineConfig cannot be used.
func NewRevertService(ctrlcfg *mcfgv1.ControllerConfig, mc *mcfgv1.MachineConfig) (RuntimeAsset, error) {
	mcoImage, ok := ctrlcfg.Spec.Images["machineConfigOperator"]
	if !ok {
		return nil, fmt.Errorf("controllerconfig Images does not have machineConfigOperator image")
	}

	if mcoImage == "" {
		return nil, fmt.Errorf("controllerconfig Images has machineConfigOperator but it is empty")
	}

	return &revertService{
		MCOImage: mcoImage,
		Proxy:    ctrlcfg.Spec.Proxy != nil,
		ctrlcfg:  ctrlcfg,
		mc:       mc,
	}, nil
}

// Returns an Ignition config containing the
// machine-config-daemon-revert.service systemd unit, the proxy config file,
// and the on-disk MachineConfig needed by the systemd unit.
func (r *revertService) Ignition() (*ign3types.Config, error) {
	unit, err := r.renderServiceTemplate()
	if err != nil {
		return nil, err
	}

	mcOnDisk, err := r.getMachineConfigJSONFile()
	if err != nil {
		return nil, fmt.Errorf("could not create Ignition file %q for MachineConfig %q: %w", RevertServiceMachineConfigFile, r.mc.Name, err)
	}

	ignConfig := ctrlcommon.NewIgnConfig()
	ignConfig.Storage.Files = []ign3types.File{*mcOnDisk}
	ignConfig.Systemd = ign3types.Systemd{
		Units: []ign3types.Unit{*unit},
	}

	if r.Proxy {
		// TODO: Should we fall back to the ControllerConfig if this file is not
		// present?
		proxyfile, err := findFileInMachineConfig(r.mc, "/etc/mco/proxy.env")
		if err != nil {
			return nil, err
		}

		proxyfile.Path = RevertServiceProxyFile
		ignConfig.Storage.Files = append(ignConfig.Storage.Files, *proxyfile)
	}

	return &ignConfig, nil
}

// Converts a MachineConfig to a JSON representation and returns an Ignition
// file containing the appropriate path for the file.
func (r *revertService) getMachineConfigJSONFile() (*ign3types.File, error) {
	outBytes, err := json.Marshal(r.mc)
	if err != nil {
		return nil, fmt.Errorf("could not marshal MachineConfig %q to JSON: %w", r.mc.Name, err)
	}

	ignFile := ctrlcommon.NewIgnFileBytes(RevertServiceMachineConfigFile, outBytes)
	return &ignFile, nil
}

// Renders the embedded service template with the provided values.
func (r *revertService) renderServiceTemplate() (*ign3types.Unit, error) {
	if r.MCOImage == "" {
		return nil, fmt.Errorf("MCOImage field must be provided")
	}

	// Golang templates must be rendered using exported fields. However, we want
	// to manage the exported field for this service, so we create an anonymous
	// struct which embeds RevertService and the ServiceName before we render
	// the template.
	data := struct {
		ServiceName                    string
		RevertServiceMachineConfigFile string
		ProxyFile                      string
		revertService
	}{
		ServiceName:                    RevertServiceName,
		RevertServiceMachineConfigFile: RevertServiceMachineConfigFile,
		ProxyFile:                      RevertServiceProxyFile,
		revertService:                  *r,
	}

	out := &ign3types.Unit{}

	if err := renderTemplate(RevertServiceName, mcdRevertServiceIgnYAML, data, out); err != nil {
		return nil, err
	}

	return out, nil
}

// Finds a given file by path in a given MachineConfig. Returns an error if the
// file cannot be found.
func findFileInMachineConfig(mc *mcfgv1.MachineConfig, path string) (*ign3types.File, error) {
	ignConfig, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, err
	}

	ignFile := findFileInIgnitionConfig(&ignConfig, path)
	if ignFile == nil {
		return nil, fmt.Errorf("file %q not found in MachineConfig %q", path, mc.Name)
	}

	return ignFile, nil
}

// Finds a given file by path in a given Ignition config. Returns nil if the
// file is not found.
func findFileInIgnitionConfig(ignConfig *ign3types.Config, path string) *ign3types.File {
	for _, file := range ignConfig.Storage.Files {
		if file.Path == path {
			out := file
			return &out
		}
	}

	return nil
}

// Renders the given data into the given YAML template source. Then attempts to
// decode it into the given struct instance. Returns any errors encountered.
func renderTemplate(name, src string, data, out interface{}) error {
	tmpl := template.New(name)

	tmpl, err := tmpl.Parse(src)
	if err != nil {
		return err
	}

	buf := bytes.NewBuffer([]byte{})
	if err := tmpl.Execute(buf, data); err != nil {
		return err
	}

	if err := yaml.Unmarshal(buf.Bytes(), out); err != nil {
		return err
	}

	return nil
}
