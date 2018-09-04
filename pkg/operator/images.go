package operator

// images allows build systems to inject images for MCO components.
type images struct {
	MachineConfigController string `json:"machineConfigController"`
	MachineConfigDaemon     string `json:"machineConfigDaemon"`
	MachineConfigServer     string `json:"machineConfigServer"`
}
