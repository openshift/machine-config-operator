package runtimeassets

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"sigs.k8s.io/yaml"
)

var _ RuntimeAsset = &revertService{}

const (
	RevertServiceName              string = "machine-config-daemon-revert.service"
	RevertServiceMachineConfigFile string = "/etc/mco/machineconfig-revert.json"
)

//go:embed machine-config-daemon-revert.service.yaml
var mcdRevertServiceIgnYAML string

type revertService struct {
	// The MCO image pullspec that should be used.
	MCOImage string
	// Whether the proxy file exists and should be considered.
	Proxy bool
}

// Constructs a revertService instance from a ControllerConfig. Returns an
// error if the provided ControllerConfig cannot be used.
func NewRevertService(ctrlcfg *mcfgv1.ControllerConfig) (RuntimeAsset, error) {
	mcoImage, ok := ctrlcfg.Spec.Images["machineConfigOperator"]
	if !ok {
		return nil, fmt.Errorf("controllerconfig Images does not have machineConfigOperator image")
	}

	if mcoImage == "" {
		return nil, fmt.Errorf("controllerconfig Images has machineConfigOperator but it is empty")
	}

	hasProxy := false
	if ctrlcfg.Spec.Proxy != nil {
		hasProxy = true
	}

	return &revertService{
		MCOImage: mcoImage,
		Proxy:    hasProxy,
	}, nil
}

// Returns an Ignition config containing the
// machine-config-daemon-revert.service systemd unit.
func (r *revertService) Ignition() (*ign3types.Config, error) {
	rendered, err := r.render()
	if err != nil {
		return nil, err
	}

	out := &ign3types.Unit{}

	if err := yaml.Unmarshal(rendered, out); err != nil {
		return nil, err
	}

	ignConfig := ctrlcommon.NewIgnConfig()
	ignConfig.Systemd = ign3types.Systemd{
		Units: []ign3types.Unit{
			*out,
		},
	}

	return &ignConfig, nil
}

// Renders the embedded template with the provided values.
func (r *revertService) render() ([]byte, error) {
	if r.MCOImage == "" {
		return nil, fmt.Errorf("MCOImage field must be provided")
	}

	tmpl := template.New(RevertServiceName)

	tmpl, err := tmpl.Parse(mcdRevertServiceIgnYAML)
	if err != nil {
		return nil, err
	}

	// Golang templates must be rendered using exported fields. However, we want
	// to manage the exported field for this service, so we create an anonymous
	// struct which embeds RevertService and the ServiceName before we render
	// the template.
	data := struct {
		ServiceName                    string
		RevertServiceMachineConfigFile string
		revertService
	}{
		ServiceName:                    RevertServiceName,
		RevertServiceMachineConfigFile: RevertServiceMachineConfigFile,
		revertService:                  *r,
	}

	buf := bytes.NewBuffer([]byte{})
	if err := tmpl.Execute(buf, data); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
