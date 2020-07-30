package operator

// Images contain data derived from what github.com/openshift/installer's
// bootkube.sh provides.  If you want to add a new image, you need
// to "ratchet" the change as follows:
//
// Add the image here and also a CLI option with a default value
// Change the installer to pass that arg with the image from the CVO
// (some time later) Change the option to required and drop the default
type Images struct {
	ReleaseVersion string `json:"releaseVersion,omitempty"`
	RenderConfigImages
	ControllerConfigImages
}

// RenderConfigImages are image names used to render templates under ./manifests/
type RenderConfigImages struct {
	MachineConfigOperator string `json:"machineConfigOperator"`
	MachineOSContent      string `json:"machineOSContent"`
	// These have to be named differently from the ones in ControllerConfigImages
	// or we get errors about ambiguous selectors because both structs are
	// combined in the Images struct.
	KeepalivedBootstrap          string `json:"keepalived"`
	CorednsBootstrap             string `json:"coredns"`
	BaremetalRuntimeCfgBootstrap string `json:"baremetalRuntimeCfg"`
	OauthProxy                   string `json:"oauthProxy"`
}

// ControllerConfigImages are image names used to render templates under ./templates/
type ControllerConfigImages struct {
	InfraImage          string `json:"infraImage"`
	Keepalived          string `json:"keepalivedImage"`
	Coredns             string `json:"corednsImage"`
	MdnsPublisher       string `json:"mdnsPublisherImage"`
	Haproxy             string `json:"haproxyImage"`
	BaremetalRuntimeCfg string `json:"baremetalRuntimeCfgImage"`
}
