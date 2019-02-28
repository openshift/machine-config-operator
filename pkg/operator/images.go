package operator

// This data is derived from what github.com/openshift/installer's
// bootkube.sh provides.  If you want to add a new image, you need
// to "ratchet" the change as follows:
//
// Add the image here and also a CLI option with a default value
// Change the installer to pass that arg with the image from the CVO
// (some time later) Change the option to required and drop the default
type Images struct {
	MachineConfigController string `json:"machineConfigController"`
	MachineConfigDaemon     string `json:"machineConfigDaemon"`
	MachineConfigServer     string `json:"machineConfigServer"`
	MachineOSContent        string `json:"machineOSContent"`
	Etcd                    string `json:"etcd"`
	SetupEtcdEnv            string `json:"setupEtcdEnv"`
	InfraImage              string `json:"infraImage"`
}
