package daemon

import (
	"fmt"

	"github.com/ashcrow/osrelease"
)

const (
	// MachineConfigDaemonOSRHCOS denotes RHEL CoreOS
	MachineConfigDaemonOSRHCOS = "RHCOS"
	// machineConfigDaemonOSRHEL denotes RHEL
	machineConfigDaemonOSRHEL = "RHEL"
	// machineConfigDaemonOSCENTOS denotes CentOS
	machineConfigDaemonOSCENTOS = "CENTOS"
	// MachineConfigDaemonOSFCOS denotes Fedora CoreOS
	MachineConfigDaemonOSFCOS = "FCOS"
)

// GetHostRunningOS reads os-release from the rootFs prefix to return what
// OS variant the daemon is running on. If we are unable to read the
// os-release file OR the information doesn't match MCD supported OS's
// an error is returned.
func GetHostRunningOS() (string, error) {
	libPath := "/usr/lib/os-release"
	etcPath := "/etc/os-release"

	or, err := osrelease.NewWithOverrides(etcPath, libPath)
	if err != nil {
		return "", err
	}

	if or.ID == "fedora" && or.VARIANT_ID == "coreos" {
		return MachineConfigDaemonOSFCOS, nil
	}

	// See https://github.com/openshift/redhat-release-coreos/blob/master/redhat-release-coreos.spec
	switch or.ID {
	case "rhcos":
		return MachineConfigDaemonOSRHCOS, nil
	case "rhel":
		return machineConfigDaemonOSRHEL, nil
	case "centos":
		return machineConfigDaemonOSCENTOS, nil
	default:
		// default to unknown OS
		return "", fmt.Errorf("an unsupported OS is being used: %s:%s", or.ID, or.VARIANT_ID)
	}
}
