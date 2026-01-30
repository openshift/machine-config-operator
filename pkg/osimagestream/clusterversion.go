package osimagestream

import (
	"errors"
	"fmt"

	configlisters "github.com/openshift/client-go/config/listers/config/v1"
)

// clusterVersionSingletonName is the well-known name of the cluster-scoped ClusterVersion singleton resource.
const clusterVersionSingletonName = "version"

// GetReleasePayloadImage retrieves the release payload image from the ClusterVersion resource.
func GetReleasePayloadImage(lister configlisters.ClusterVersionLister) (string, error) {
	clusterVersion, err := lister.Get(clusterVersionSingletonName)
	if err != nil {
		return "", fmt.Errorf("failed to get ClusterVersion: %w", err)
	}
	if clusterVersion == nil || clusterVersion.Status.Desired.Image == "" {
		return "", errors.New("ClusterVersion desired image is not yet available")
	}
	// Got it, store the variable and exit
	return clusterVersion.Status.Desired.Image, nil
}
