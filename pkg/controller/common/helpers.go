package common

import (
	igntypes "github.com/coreos/ignition/config/v2_2/types"
	"github.com/golang/glog"
	"io/ioutil"
)

// NewIgnConfig returns an empty ignition config with version set as latest version
func NewIgnConfig() igntypes.Config {
	return igntypes.Config{
		Ignition: igntypes.Ignition{
			Version: igntypes.MaxVersion.String(),
		},
	}
}

// WriteTerminationError writes to the Kubernetes termination log.
func WriteTerminationError(err error) {
	msg := err.Error()
	ioutil.WriteFile("/dev/termination-log", []byte(msg), 0644)
	glog.Fatal(msg)
}
