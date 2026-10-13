package osimagestream

import (
	"fmt"

	"github.com/openshift/machine-config-operator/pkg/version"
	k8sversion "k8s.io/apimachinery/pkg/util/version"
)

const (
	// StreamNameRHEL9 is the stream name for RHEL 9 based CoreOS images.
	StreamNameRHEL9 = "rhel-9"
	// StreamNameRHEL10 is the stream name for RHEL 10 based CoreOS images.
	StreamNameRHEL10 = "rhel-10"
	// StreamNameCentOS10 is the stream name for SCOS 10 based CoreOS images.
	StreamNameCentOS10 = "centos-10"
)

// GetBuiltinDefaultStreamName returns the default stream name for the current build.
// For OKD/SCOS builds it always returns "centos-10" as OKD only ships a single stream.
// For OCP builds it returns the default stream based on the OCP version:
// OCP 4.x clusters default to "rhel-9", OCP 5+ default to "rhel-10".
// If installVersion is provided that value is used directly. Otherwise, the build's
// release version (version.ReleaseVersion) is used as fallback.
func GetBuiltinDefaultStreamName(installVersion *k8sversion.Version) (string, error) {
	if version.IsSCOS() {
		return StreamNameCentOS10, nil
	}

	var releaseVersion *k8sversion.Version
	if installVersion != nil {
		releaseVersion = installVersion
	} else {
		var err error
		releaseVersion, err = k8sversion.ParseGeneric(version.ReleaseVersion)
		if err != nil {
			return "", fmt.Errorf("unable to parse the build release version %q as a version: %w", version.ReleaseVersion, err)
		}
	}

	if releaseVersion.Major() == 4 {
		return StreamNameRHEL9, nil
	}
	return StreamNameRHEL10, nil
}
