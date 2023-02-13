package osrelease

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashcrow/osrelease"
)

// Source of the OS release information
type InfoSource string

const (
	// From the /etc/os-release / /usr/lib/os-release files.
	OSReleaseInfoSource InfoSource = "OS Release"
	// From the OS image labels.
	ImageLabelInfoSource InfoSource = "OS Image Label"
)

// OS Release Paths
const (
	EtcOSReleasePath string = "/etc/os-release"
	LibOSReleasePath string = "/usr/lib/os-release"
)

// OS IDs - only used internally
const (
	coreos string = "coreos"
	fedora string = "fedora"
	rhcos  string = "rhcos"
	scos   string = "scos"
)

// Full OS names
const (
	FCOS  string = "Fedora CoreOS"
	RHCOS string = "Red Hat Enterprise Linux CoreOS"
	SCOS  string = "CentOS Stream CoreOS"
)

// OperatingSystem is a wrapper around a subset of the os-release fields
// and also tracks whether ostree is in use.
type OperatingSystem struct {
	// id is the ID field from the os-release or inferred from the OS image
	// label.
	id string
	// variantID is the VARIANT_ID field from the os-release or inferred from the
	// OS image label.
	variantID string
	// version is the VERSION, RHEL_VERSION, or VERSION_ID field from the
	// os-release or image label.
	version string
	// values is a map of all the values we uncovered either via the
	// /etc/os-release / /usr/lib/os-release files *or* the labels attached
	// to an OS image.
	values map[string]string
	// source identifies whether this came from the OSRelease file or from image labels.
	source InfoSource
}

func newOperatingSystemFromOSRelease(etcPath, libPath string) (OperatingSystem, error) {
	ret := OperatingSystem{}

	or, err := osrelease.NewWithOverrides(etcPath, libPath)
	if err != nil {
		return ret, err
	}

	ret.id = or.ID
	ret.variantID = or.VARIANT_ID

	ret.version = getOSVersion(or)

	// Store all of the values identified by the osrelease library.
	ret.values = or.ADDITIONAL_FIELDS
	ret.values["NAME"] = or.NAME
	ret.values["VERSION"] = or.VERSION
	ret.values["ID"] = or.ID
	ret.values["ID_LIKE"] = or.ID_LIKE
	ret.values["VERSION_ID"] = or.VERSION_ID
	ret.values["VERSION_CODENAME"] = or.VERSION_CODENAME
	ret.values["PRETTY_NAME"] = or.PRETTY_NAME
	ret.values["ANSI_COLOR"] = or.ANSI_COLOR
	ret.values["CPE_NAME"] = or.CPE_NAME
	ret.values["HOME_URL"] = or.HOME_URL
	ret.values["BUG_REPORT_URL"] = or.BUG_REPORT_URL
	ret.values["PRIVACY_POLICY_URL"] = or.PRIVACY_POLICY_URL
	ret.values["VARIANT"] = or.VARIANT
	ret.values["VARIANT_ID"] = or.VARIANT_ID

	ret.source = OSReleaseInfoSource

	if ret.id == rhcos && ret.version[0:1] != "8" && ret.version[0:1] != "9" {
		return ret, fmt.Errorf("unknown RHCOS version: %q, got: %v", ret.version, ret.values)
	}

	if ret.id == scos && ret.version[0:1] != "9" {
		return ret, fmt.Errorf("unknown SCOS version: %q, got: %v", ret.version, ret.values)
	}

	return ret, nil
}

// Returns the source of where this info came from.
func (os OperatingSystem) Source() InfoSource {
	return os.source
}

// Returns the values map if cdditional ontext is needed.
func (os OperatingSystem) Values() map[string]string {
	return os.values
}

// IsEL is true if the OS is an Enterprise Linux variant,
// i.e. RHEL CoreOS (RHCOS) or CentOS Stream CoreOS (SCOS)
func (os OperatingSystem) IsEL() bool {
	return os.id == rhcos || os.id == scos
}

// IsEL9 is true if the OS is RHCOS 9 or SCOS 9
func (os OperatingSystem) IsEL9() bool {
	return os.IsEL() && strings.HasPrefix(os.version, "9") || os.version == "9"
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
	return newOperatingSystemFromOSRelease(EtcOSReleasePath, LibOSReleasePath)
}

// Generates the OperatingSystem data from strings which contain the desired
// content. Mostly useful for testing purposes.
func LoadOSRelease(etcOSReleaseContent, libOSReleaseContent string) (OperatingSystem, error) {
	tempDir, err := os.MkdirTemp("", "")
	if err != nil {
		return OperatingSystem{}, err
	}

	defer os.RemoveAll(tempDir)

	etcOSReleasePath := filepath.Join(tempDir, "etc-os-release")
	libOSReleasePath := filepath.Join(tempDir, "lib-os-release")

	if err := os.WriteFile(etcOSReleasePath, []byte(etcOSReleaseContent), 0o644); err != nil {
		return OperatingSystem{}, err
	}

	if err := os.WriteFile(libOSReleasePath, []byte(libOSReleaseContent), 0o644); err != nil {
		return OperatingSystem{}, err
	}

	return newOperatingSystemFromOSRelease(etcOSReleasePath, libOSReleasePath)
}

// Determines the OS version based upon the contents of the RHEL_VERSION, VERSION or VERSION_ID fields.
func getOSVersion(or osrelease.OSRelease) string {
	// If we have the RHEL_VERSION field, we should use that value instead.
	if rhelVersion, ok := or.ADDITIONAL_FIELDS["RHEL_VERSION"]; ok {
		return rhelVersion
	}

	// If we have the OPENSHIFT_VERSION field, we can compute the OS version.
	if openshiftVersion, ok := or.ADDITIONAL_FIELDS["OPENSHIFT_VERSION"]; ok {
		// Strip the "." from the middle of the OpenShift version; e.g., 4.12 becomes 412.
		stripped := strings.ReplaceAll(openshiftVersion, ".", "")
		if strings.HasPrefix(or.VERSION, stripped) {
			return getSCOSversion(or.VERSION)
		}
	}

	// Fallback to the VERSION_ID field
	return or.VERSION_ID
}
