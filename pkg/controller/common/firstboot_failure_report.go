package common

// FirstbootFailureReport represents a failure report from a node during firstboot.
// Used by the daemon (sender) and the MCS (receiver).
type FirstbootFailureReport struct {
	Pool         string `json:"pool"`
	NodeID       string `json:"nodeID"`
	Stage        string `json:"stage"`
	ImageURL     string `json:"imageURL"`
	ErrorMessage string `json:"errorMessage"`
}

// MCSConnectionInfo carries what a node needs to reach the MCS before it has
// joined the cluster. It is written to disk by the MCS (sender) during ignition
// provisioning and read back by the daemon (receiver) on the firstboot failure
// path.
type MCSConnectionInfo struct {
	// URL is the base URL this cluster's MCS is reached at.
	URL string `json:"url"`
	// TLSMinVersion is the minimum TLS version required by the cluster's
	// security profile, in the Kubernetes flag form (e.g. "VersionTLS12"), so
	// the client does not undercut the profile the MCS itself serves with. An
	// empty value means the Kubernetes default of TLS 1.2.
	TLSMinVersion string `json:"tlsMinVersion,omitempty"`
}
