package osrelease

import (
	"strings"

	"github.com/ashcrow/osrelease"
)

// OS Release Paths
const (
	EtcOSReleasePath string = "/etc/os-release"
	LibOSReleasePath string = "/usr/lib/os-release"
)

// OS IDs
const (
	coreos string = "coreos"
	fcos   string = "fcos"
	fedora string = "fedora"
	rhcos  string = "rhcos"
	scos   string = "scos"
)

// OperatingSystem is a wrapper around a subset of the os-release fields
// and also tracks whether ostree is in use.
type OperatingSystem struct {
	// id is the ID field from the os-release
	id string
	// variantID is the VARIANT_ID field from the os-release
	variantID string
	// version is the VERSION, RHEL_VERSION, or VERSION_ID field from the os-release
	version string
}

// IsEL is true if the OS is an Enterprise Linux variant,
// i.e. RHEL CoreOS (RHCOS) or CentOS Stream CoreOS (SCOS)
func (os OperatingSystem) IsEL() bool {
	return os.id == rhcos || os.id == scos
}

// IsEL9 is true if the OS is RHCOS 9 or SCOS 9
func (os OperatingSystem) IsEL9() bool {
	return os.IsEL() && strings.HasPrefix(os.version, "9.") || os.version == "9"
}

// IsFCOS is true if the OS is Fedora CoreOS
func (os OperatingSystem) IsFCOS() bool {
	return os.id == fedora && os.variantID == coreos
}

// IsSCOS is true if the OS is SCOS
func (os OperatingSystem) IsSCOS() bool {
	return os.id == scos
}

// IsCoreOSVariant is true if the OS is FCOS or a derivative (ostree+Ignition)
// which includes SCOS and RHCOS.
func (os OperatingSystem) IsCoreOSVariant() bool {
	// In RHCOS8 the variant id is not specified. SCOS (future RHCOS9) and FCOS have VARIANT_ID=coreos.
	return os.variantID == coreos || os.IsEL()
}

// IsLikeTraditionalRHEL7 is true if the OS is traditional RHEL7 or CentOS7:
// yum based + kickstart/cloud-init (not Ignition).
func (os OperatingSystem) IsLikeTraditionalRHEL7() bool {
	// Today nothing else is going to show up with a version ID of 7
	if len(os.version) > 2 {
		return strings.HasPrefix(os.version, "7.")
	}
	return os.version == "7"
}

// ToPrometheusLabel returns a value we historically fed to Prometheus
func (os OperatingSystem) ToPrometheusLabel() string {
	// We historically upper cased this
	return strings.ToUpper(os.id)
}

// GetHostRunningOS reads os-release to generate the OperatingSystem data.
func GetHostRunningOS() (OperatingSystem, error) {
	return GetNodeRunningOS(EtcOSReleasePath, LibOSReleasePath)
}

func GetNodeRunningOS(etcPath, libPath string) (OperatingSystem, error) {
	ret := OperatingSystem{}

	or, err := osrelease.NewWithOverrides(etcPath, libPath)
	if err != nil {
		return ret, err
	}

	ret.id = or.ID
	ret.variantID = or.VARIANT_ID
	ret.version = getOSVersion(or)

	return ret, nil
}

func getOSVersion(or osrelease.OSRelease) string {
	// If we have the RHEL_VERSION field, we should use that value instead.
	if rhelVersion, ok := or.ADDITIONAL_FIELDS["RHEL_VERSION"]; ok {
		return rhelVersion
	}

	// If we have the OPENSHIFT_VERSION field, we can compute the OS version.
	if openshiftVersion, ok := or.ADDITIONAL_FIELDS["OPENSHIFT_VERSION"]; ok {
		// Move the "." from the middle of the OpenShift version to the end; e.g., 4.12 becomes 412.
		openshiftVersion := strings.ReplaceAll(openshiftVersion, ".", "") + "."
		if strings.HasPrefix(or.VERSION, openshiftVersion) {
			// Strip the OpenShift Version prefix from the VERSION field, if it is found.
			return strings.ReplaceAll(or.VERSION, openshiftVersion, "")
		}
	}

	// Fallback to the VERSION_ID field
	return or.VERSION_ID
}
