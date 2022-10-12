package daemon

import (
	"strings"

	"github.com/ashcrow/osrelease"
)

// OperatingSystem is a wrapper around a subset of the os-release fields
// and also tracks whether ostree is in use.
type OperatingSystem struct {
	// ID is the ID field from the os-release
	ID string
	// VariantID is the VARIANT_ID field from the os-release
	VariantID string
	// VersionID is the VERSION_ID field from the os-release
	VersionID string
}

// IsEL is true if the OS is an Enterprise Linux variant,
// i.e. RHEL CoreOS (RHCOS) or CentOS Stream CoreOS (SCOS)
func (os OperatingSystem) IsEL() bool {
	return os.ID == "rhcos" || os.ID == "scos"
}

// IsEL9 is true if the OS is RHCOS 9 or SCOS 9
func (os OperatingSystem) IsEL9() bool {
	return os.IsEL() && os.VersionID == "9"
}

// IsFCOS is true if the OS is Fedora CoreOS
func (os OperatingSystem) IsFCOS() bool {
	return os.ID == "fedora" && os.VariantID == "coreos"
}

// IsSCOS is true if the OS is SCOS
func (os OperatingSystem) IsSCOS() bool {
	return os.ID == "scos"
}

// IsCoreOSVariant is true if the OS is FCOS or a derivative (ostree+Ignition)
// which includes SCOS and RHCOS.
func (os OperatingSystem) IsCoreOSVariant() bool {
	// In RHCOS8 the variant id is not specified. SCOS (future RHCOS9) and FCOS have VARIANT_ID=coreos.
	return os.VariantID == "coreos" || os.IsEL()
}

// IsLikeTraditionalRHEL7 is true if the OS is traditional RHEL7 or CentOS7:
// yum based + kickstart/cloud-init (not Ignition).
func (os OperatingSystem) IsLikeTraditionalRHEL7() bool {
	// Today nothing else is going to show up with a version ID of 7
	if len(os.VersionID) > 2 {
		return strings.HasPrefix(os.VersionID, "7.")
	}
	return os.VersionID == "7"
}

// ToPrometheusLabel returns a value we historically fed to Prometheus
func (os OperatingSystem) ToPrometheusLabel() string {
	// We historically upper cased this
	return strings.ToUpper(os.ID)
}

// GetHostRunningOS reads os-release to generate the OperatingSystem data.
func GetHostRunningOS() (OperatingSystem, error) {
	libPath := "/usr/lib/os-release"
	etcPath := "/etc/os-release"

	ret := OperatingSystem{}

	or, err := osrelease.NewWithOverrides(etcPath, libPath)
	if err != nil {
		return ret, err
	}

	ret.ID = or.ID
	ret.VariantID = or.VARIANT_ID
	ret.VersionID = or.VERSION_ID

	return ret, nil
}
