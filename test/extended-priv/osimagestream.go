package extended

import (
	"fmt"
	"strings"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// OSImageStream struct handles OSImageStream resource in OCP
type OSImageStream struct {
	Resource
}

// NewOSImageStream creates a new OSImageStream struct (singleton resource named "cluster")
func NewOSImageStream(oc *exutil.CLI) *OSImageStream {
	return &OSImageStream{Resource: *NewResource(oc, "osimagestream", "cluster")}
}

// GetDefaultStream returns the default stream name
func (osis *OSImageStream) GetDefaultStream() (string, error) {
	return osis.Get(`{.status.defaultStream}`)
}

// GetAvailableStreamNames returns a list of all available stream names
func (osis *OSImageStream) GetAvailableStreamNames() ([]string, error) {
	namesString, err := osis.Get(`{.status.availableStreams[*].name}`)
	if err != nil {
		return nil, err
	}

	if namesString == "" {
		return []string{}, nil
	}

	names := strings.Fields(namesString)
	return names, nil
}

// GetOsImageByName returns the osImage for the given stream name
func (osis *OSImageStream) GetOsImageByName(streamName string) (string, error) {
	osImage, err := osis.Get(fmt.Sprintf(`{.status.availableStreams[?(@.name=="%s")].osImage}`, streamName))
	if err != nil {
		return "", err
	}

	if osImage == "" {
		return "", fmt.Errorf("stream '%s' not found in available streams", streamName)
	}

	return osImage, nil
}

// GetOsExtensionsImageByName returns the osExtensionsImage for the given stream name
func (osis *OSImageStream) GetOsExtensionsImageByName(streamName string) (string, error) {
	osExtImage, err := osis.Get(fmt.Sprintf(`{.status.availableStreams[?(@.name=="%s")].osExtensionsImage}`, streamName))
	if err != nil {
		return "", err
	}

	if osExtImage == "" {
		return "", fmt.Errorf("stream '%s' not found in available streams", streamName)
	}

	return osExtImage, nil
}

// LogStreamInfo logs information about all available streams
func (osis *OSImageStream) LogStreamInfo() {
	defaultStream, err := osis.GetDefaultStream()
	if err != nil {
		logger.Errorf("Error getting default stream: %s", err)
		return
	}

	names, err := osis.GetAvailableStreamNames()
	if err != nil {
		logger.Errorf("Error getting available stream names: %s", err)
		return
	}

	logger.Infof("OSImageStream default stream: %s", defaultStream)
	logger.Infof("OSImageStream available streams: %v", names)

	for _, name := range names {
		osImage, _ := osis.GetOsImageByName(name)
		osExtImage, _ := osis.GetOsExtensionsImageByName(name)
		logger.Infof("Stream '%s':", name)
		logger.Infof("  osImage: %s", osImage)
		logger.Infof("  osExtensionsImage: %s", osExtImage)
	}
}

// GetDefaultOSImageStream returns the default OS image stream based on the cluster version.
// OCP 4.x clusters default to "rhel-9", OCP 5+ default to "rhel-10".
func GetDefaultOSImageStream(oc *exutil.CLI) string {
	clusterVersion, _, err := exutil.GetClusterVersion(oc)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting cluster version")
	if clusterVersion[:1] == "4" {
		return OSImageStreamRHEL9
	}
	return OSImageStreamRHEL10
}

// GetInitialAndTargetStreams determines initial and target osImageStreams based on cluster version.
// Returns (initialStream, targetStream) where:
// - initialStream: cluster default stream (rhel-9 in 4.23, rhel-10 in 5.0)
// - targetStream: alternate stream (rhel-10 in 4.23, rhel-9 in 5.0)
// This ensures tests always trigger a stream change and work consistently across versions.
func GetInitialAndTargetStreams(oc *exutil.CLI) (initialStream, targetStream string) {
	defaultStream := GetDefaultOSImageStream(oc)

	if defaultStream == OSImageStreamRHEL10 {
		// 5.0 cluster: default is rhel-10, alternate is rhel-9
		initialStream = OSImageStreamRHEL10
		targetStream = OSImageStreamRHEL9
	} else {
		// 4.23 cluster: default is rhel-9, alternate is rhel-10
		initialStream = OSImageStreamRHEL9
		targetStream = OSImageStreamRHEL10
	}

	logger.Infof("Cluster streams - initial: %s, target: %s", initialStream, targetStream)
	return initialStream, targetStream
}
