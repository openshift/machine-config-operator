package daemon

import (
	"io/ioutil"

	"github.com/golang/glog"
)

// NotCoreOSClient is a wrapper around RpmOstreeClient that implements NodeUpdaterClient
// safely on unsupported and legacy Operating systems.
type notCoreOSClient struct{}

// NewNodeUpdaterClientNotCoreOS returns a NodeUpdaterClient for traditional RHEL.
func NewNodeUpdaterClientNotCoreOS() NodeUpdaterClient {
	glog.Warning("Operating System is not a CoreOS Variant. Update functionality is disabled.")
	return &notCoreOSClient{}
}

// notCoreOSClient is a NodeUpdateClient
var _ NodeUpdaterClient = &notCoreOSClient{}

// GetBootedDeployment returns the booted demployment.
func (noCos *notCoreOSClient) GetBootedDeployment() (*RpmOstreeDeployment, error) {
	return &RpmOstreeDeployment{}, nil
}

// Rebase calls rpm-ostree status
func (noCos *notCoreOSClient) Rebase(a, b string) (bool, error) {
	glog.Info("Rebase is not supported on this system.")
	return false, nil
}

// GetStatus returns the rpm-ostree status
func (noCos *notCoreOSClient) GetStatus() (string, error) {
	return "", nil
}

// GetBootedOSImageURL returns the rpmOstree Booted OS Image
func (noCos *notCoreOSClient) GetBootedOSImageURL() (string, string, error) {
	return "", "", nil
}

// GetKernelArgs returns the real kernel arguments since we can't use rpmOstree
func (noCos *notCoreOSClient) GetKernelArgs() ([]string, error) {
	content, err := ioutil.ReadFile(CmdLineFile)
	if err != nil {
		return nil, err
	}
	return quoteSpaceSplit(string(content)), nil
}

// SetKernelArgs sets the kernel arguments
func (noCos *notCoreOSClient) SetKernelArgs([]KernelArgument) (string, error) {
	return "unsupported", nil
}

// RemovePendingDeployment is not supported on non-CoreOS machines.
func (noCos *notCoreOSClient) RemovePendingDeployment() error {
	return nil
}

func (noCos *notCoreOSClient) RunRpmOstree(noun string, args ...string) ([]byte, error) {
	return nil, nil
}
