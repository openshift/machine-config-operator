package osimagestream

import (
	"github.com/openshift/machine-config-operator/pkg/imageutils"
)

// NewCacheEntryFilter returns a filter that selects cache entries relevant
// for OS stream discovery: those carrying at least one OS, extensions, bootc,
// or release label.
func NewCacheEntryFilter() imageutils.CacheEntryFilter {
	return imageutils.NewCacheEntryFilter(
		coreOSLabelStreamClass,
		coreOSLabelBootc,
		coreOSLabelExtension,
		releasePayloadLabel,
	)
}

