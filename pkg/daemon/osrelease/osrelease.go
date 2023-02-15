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

// OS IDs
const (
	coreos string = "coreos"
	fedora string = "fedora"
	rhcos  string = "rhcos"
	scos   string = "scos"
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

func newOperatingSystemFromImageLabels(imageLabels map[string]string) (OperatingSystem, error) {
	if err := hasRequiredLabels(imageLabels); err != nil {
		return OperatingSystem{}, err
	}

	os := OperatingSystem{
		values: imageLabels,
		source: ImageLabelInfoSource,
		// If we've made it this far, we know we have a CoreOS variant.
		variantID: coreos,
		version:   imageLabels["version"],
	}

	// Only FCOS and SCOS set this label, which is why it's not required.
	if osName, osNameOK := imageLabels["io.openshift.build.version-display-names"]; osNameOK {
		return inferNonRHCOS(os, osName)
	}

	// Like SCOS, RHCOS has the version number in the middle position of the OCP
	// / OKD version ID (e.g., 413.92.202302081904-0, becomes 92; which is 9.2
	// though we don't care about the missing decimal here)
	os.version = strings.Split(os.version, ".")[1]

	// If we've made it this far and the first character is either 8 or 9, we
	// most likely have an RHCOS image.
	if os.version[0:1] == "8" || os.version[0:1] == "9" {
		os.id = rhcos
		return os, nil
	}

	return os, fmt.Errorf("unable to infer OS version from image labels: %v", imageLabels)
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

// Infers the OS release version given the image labels from a given OS image.
func InferFromOSImageLabels(imageLabels map[string]string) (OperatingSystem, error) {
	return newOperatingSystemFromImageLabels(imageLabels)
}

// Determines if an OS image has the labels that are required to infer what OS
// it contains.
func hasRequiredLabels(imageLabels map[string]string) error {
	requiredLabels := []string{
		"coreos-assembler.image-input-checksum",
		"coreos-assembler.image-config-checksum",
		"org.opencontainers.image.revision",
		"org.opencontainers.image.source",
		"version",
	}

	for _, reqLabel := range requiredLabels {
		if _, ok := imageLabels[reqLabel]; !ok {
			return fmt.Errorf("labels %v missing required key %q", imageLabels, reqLabel)
		}
	}

	return nil
}

// Infers that a given oeprating system is either FCOS or SCOS.
func inferNonRHCOS(os OperatingSystem, osName string) (OperatingSystem, error) {
	osName = strings.ReplaceAll(osName, "machine-os=", "")

	switch osName {
	case "CentOS Stream CoreOS":
		os.id = scos
		// Grab the middle value from the version number (e.g.,
		// 413.9.202302130811-0 becomes 9)
		os.version = strings.Split(os.version, ".")[1]
	case "Fedora CoreOS":
		// FCOS doesn't have the OCP / OKD version number encoded in it (e.g.,
		// 37.20230211.20.0) so we don't need to mutate it or inspect it.
		os.id = fedora
	default:
		// Catch-all if we have an unknown OS name.
		return os, fmt.Errorf("unknown OS %q", osName)
	}

	// Currently, SCOS is at major version 9. This will probably change in the
	// distant future. Additionally, this provides a guard in the event that
	// the version number schema changes.
	if os.id == scos && os.version != "9" {
		return os, fmt.Errorf("unknown SCOS version %q", os.version)
	}

	// We've been able to infer the necessary fields for FCOS and SCOS.
	return os, nil
}

// Determines the OS version based upon the contents of the RHEL_VERSION, VERSION or VERSION_ID fields.
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
