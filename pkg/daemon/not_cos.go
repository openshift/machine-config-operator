package daemon

import (
	"io/ioutil"
	"strings"

	"github.com/pkg/errors"
)

// NotCoreOSClient is a wrapper around RpmOstreeClient that implements NodeUpdaterClient
// safely on unsupported and legacy Operating systems.
type notCoreOSClient struct {
	r *RpmOstreeClient
}

// NewNodeUpdaterClientNotCoreOS returns a NodeUpdaterClient for legacy and non-CoreOS
// operating systems like RHEL 7.
func NewNodeUpdaterClientNotCoreOS() NodeUpdaterClient {
	return &notCoreOSClient{
		r: &RpmOstreeClient{},
	}
}

var (
	// notCoreOSClient is a NodeUpdaterClient.
	_ NodeUpdaterClient = &notCoreOSClient{}

	// ErrNotCoreosVariant is a generic error for un-supported tasks on legacy systems.
	ErrNotCoreosVariant = errors.New("operating system is not a CoreOS Variant")
)

// GetBootedDeployment returns the booted demployment.
func (noCos *notCoreOSClient) GetBootedDeployment() (*RpmOstreeDeployment, error) {
	return noCos.r.GetBootedDeployment()
}

// Rebase calls rpm-ostree status
func (noCos *notCoreOSClient) Rebase(a, b string) (bool, error) {
	return noCos.r.Rebase(a, b)
}

// GetStatus returns the rpm-ostree status
func (noCos *notCoreOSClient) GetStatus() (string, error) {
	return noCos.r.GetStatus()
}

// GetBootedOSImageURL returns the rpmOstree Booted OS Image
func (noCos *notCoreOSClient) GetBootedOSImageURL() (string, string, error) {
	return noCos.r.GetBootedOSImageURL()
}

// GetKernelArgs returns the real kernel arguments since we can't use rpmOstree
func (noCos *notCoreOSClient) GetKernelArgs() ([]string, error) {
	content, err := ioutil.ReadFile(CmdLineFile)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(content), " "), nil
}
