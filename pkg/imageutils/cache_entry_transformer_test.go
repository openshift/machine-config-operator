package imageutils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEntryFilter_KeepsEntryWithMatchingLabel(t *testing.T) {
	filter := NewCacheEntryFilter("io.openshift.os.streamclass", "io.openshift.release")

	entry := &InspectionCacheEntry{
		Labels: map[string]string{
			"io.openshift.os.streamclass": "rhel-9",
			"vendor":                      "Red Hat",
		},
	}

	assert.True(t, filter("sha256:aaa", entry))
}

func TestEntryFilter_ExcludesEntryWithoutMatchingLabels(t *testing.T) {
	filter := NewCacheEntryFilter("io.openshift.os.streamclass", "io.openshift.release")

	entry := &InspectionCacheEntry{
		Labels: map[string]string{
			"vendor":  "Red Hat",
			"version": "9.4",
		},
	}

	assert.False(t, filter("sha256:aaa", entry))
}

func TestEntryFilter_EmptyLabels(t *testing.T) {
	filter := NewCacheEntryFilter("io.openshift.os.streamclass")

	assert.False(t, filter("sha256:aaa", &InspectionCacheEntry{Labels: nil}))
	assert.False(t, filter("sha256:aaa", &InspectionCacheEntry{Labels: map[string]string{}}))
}
