package osimagestream

import (
	"errors"
	"fmt"
	"slices"

	configv1 "github.com/openshift/api/config/v1"
	k8sversion "k8s.io/apimachinery/pkg/util/version"

	configlisters "github.com/openshift/client-go/config/listers/config/v1"
)

// clusterVersionSingletonName is the well-known name of the cluster-scoped ClusterVersion singleton resource.
const clusterVersionSingletonName = "version"

// GetClusterVersion retrieves the current ClusterVersion resource.
func GetClusterVersion(lister configlisters.ClusterVersionLister) (*configv1.ClusterVersion, error) {
	clusterVersion, err := lister.Get(clusterVersionSingletonName)
	if err != nil {
		return nil, fmt.Errorf("failed to get ClusterVersion: %w", err)
	}
	return clusterVersion, nil
}

// GetReleasePayloadImage retrieves the release payload image from the given ClusterVersion resource.
func GetReleasePayloadImage(clusterVersion *configv1.ClusterVersion) (string, error) {
	if clusterVersion == nil || clusterVersion.Status.Desired.Image == "" {
		return "", errors.New("ClusterVersion desired image is not yet available")
	}
	// Got it, store the variable and exit
	return clusterVersion.Status.Desired.Image, nil
}

// GetInstallVersion returns the first known version from the given ClusterVersion history.
func GetInstallVersion(clusterVersion *configv1.ClusterVersion) (*k8sversion.Version, error) {
	if clusterVersion == nil {
		return nil, errors.New("ClusterVersion cannot be nil")
	}
	completed := make([]configv1.UpdateHistory, 0, len(clusterVersion.Status.History))
	for _, entry := range clusterVersion.Status.History {
		if entry.CompletionTime != nil && entry.State == configv1.CompletedUpdate {
			completed = append(completed, entry)
		}
	}
	if len(completed) == 0 {
		return nil, errors.New("ClusterVersion has no completed updates in history")
	}

	slices.SortFunc(completed, func(a, b configv1.UpdateHistory) int {
		return a.CompletionTime.Time.Compare(b.CompletionTime.Time)
	})

	v, err := k8sversion.ParseGeneric(completed[0].Version)
	if err != nil {
		return nil, fmt.Errorf("failed to parse install version %q: %w", completed[0].Version, err)
	}
	return v, nil
}
