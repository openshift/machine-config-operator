package osimagestream

import "regexp"

const (
	// osSourceAnnotation is the ImageStream tag annotation key that identifies
	// tags built from the openshift/os repository.
	osSourceAnnotation = "io.openshift.build.source-location"

	// osSourceRepo is the substring matched against the osSourceAnnotation value
	// to identify OS-related ImageStream tags.
	osSourceRepo = "github.com/openshift/os"

	// releaseImageStreamPath is the path inside a release payload image
	// where the image-references ImageStream manifest is stored.
	releaseImageStreamPath = "/release-manifests/image-references"
)

var (
	// imageTagRegxpr matches ImageStream tag names that are considered OS or extensions images.
	// Matches patterns like "rhel-coreos", "stream-coreos", "rhel-coreos-extensions", etc.
	imageTagRegxpr = regexp.MustCompile(`^(rhel|stream)[a-zA-Z0-9.-]*-coreos[a-zA-Z0-9.-]*(-extensions[a-zA-Z0-9.-]*)?$`)
)
