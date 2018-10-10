package operator

// Images allows build systems to inject images for MCO components.
type Images struct {
	MachineConfigController string `json:"machineConfigController"`
	MachineConfigDaemon     string `json:"machineConfigDaemon"`
	MachineConfigServer     string `json:"machineConfigServer"`
}

// DefaultImages returns default set of images for operator.
func DefaultImages() Images {
	return Images{
		MachineConfigController: "docker.io/openshift/origin-machine-config-controller:v4.0.0",
		MachineConfigDaemon:     "docker.io/openshift/origin-machine-config-daemon:v4.0.0",
		MachineConfigServer:     "docker.io/openshift/origin-machine-config-server:v4.0.0",
	}
}
