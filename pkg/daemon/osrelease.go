package daemon

import (
	"fmt"

	"github.com/ashcrow/osrelease"
)

const (
	// machineConfigDaemonOSRHCOS denotes RHCOS
	machineConfigDaemonOSRHCOS = "RHCOS"
	// machineConfigDaemonOSRHEL denotes RHEL
	machineConfigDaemonOSRHEL = "RHEL"
	// machineConfigDaemonOSCENTOS denotes CENTOS
	machineConfigDaemonOSCENTOS = "CENTOS"
	// machineConfigDaemonOSFCOS denotes FCOS
	machineConfigDaemonOSFCOS = "FCOS"
)

// getHostRunningOS reads os-release from the rootFs prefix to return what
// OS variant the daemon is running on. If we are unable to read the
// os-release file OR the information doesn't match MCD supported OS's
// an error is returned.
func getHostRunningOS() (string, error) {
	libPath := "/usr/lib/os-release"
	etcPath := "/etc/os-release"

	or, err := osrelease.NewWithOverrides(etcPath, libPath)
	if err != nil {
		return "", err
	}

	if or.ID == "fedora" && or.VARIANT_ID == "coreos" {
		return machineConfigDaemonOSFCOS, nil
	}

	// See https://github.com/openshift/redhat-release-coreos/blob/master/redhat-release-coreos.spec
	switch or.ID {
	case "rhcos":
		return machineConfigDaemonOSRHCOS, nil
	case "rhel":
		return machineConfigDaemonOSRHEL, nil
	case "centos":
		return machineConfigDaemonOSCENTOS, nil
	default:
		// default to unknown OS
		return "", fmt.Errorf("an unsupported OS is being used: %s:%s", or.ID, or.VARIANT_ID)
	}
}
