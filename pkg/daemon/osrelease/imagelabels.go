package osrelease

import (
	"fmt"
	"strings"
)

// Infers the OS release version given the image labels from a given OS image.
func InferFromOSImageLabels(imageLabels map[string]string) (OperatingSystem, error) {
	return newOperatingSystemFromImageLabels(imageLabels)
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

	// Like SCOS, RHCOS has the version number in the middle position with the
	// prefix of the OCP / OKD version ID (e.g., 413.92.202302081904-0, becomes
	// 92.202302081904-0; which is CentOS Stream CoreOS 9.2 though we don't care
	// about the missing decimal here).
	os.version = getRHCOSversion(os.version)

	// If we've made it this far and the first character is either 8 or 9, we
	// most likely have an RHCOS image.
	if os.version[0:1] == "8" || os.version[0:1] == "9" {
		os.id = rhcos
		return os, nil
	}

	return os, fmt.Errorf("unable to infer OS version from image labels: %v", imageLabels)
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
	case SCOS:
		os.id = scos
		os.version = getSCOSversion(os.version)
	case FCOS:
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
	if os.id == scos && os.version[0:1] != "9" {
		return os, fmt.Errorf("unknown SCOS version %q", os.version)
	}

	// We've been able to infer the necessary fields for FCOS and SCOS.
	return os, nil
}

// Infers the OS version by stripping the OCP / OKD version number from the
// version field.
func getRHCOSversion(version string) string {
	// Get the OCP / OKD version which is in the first section of the version
	// For example: 413.9.202302130811-0, gives us 413.
	ocpVersion := strings.Split(version, ".")[0] + "."

	// Next, we strip the OCP / OKD version from the rest of the version string.
	return strings.TrimPrefix(version, ocpVersion)
}

// Aliases getRHCOSversion for readability.
func getSCOSversion(version string) string {
	return getRHCOSversion(version)
}
