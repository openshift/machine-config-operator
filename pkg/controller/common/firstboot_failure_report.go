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
