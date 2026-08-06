package osimagestream

import (
	"testing"

	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/stretchr/testify/assert"
)

func TestCacheEntryFilter(t *testing.T) {
	filter := NewCacheEntryFilter()

	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{name: "accepts streamclass label", labels: map[string]string{"io.openshift.os.streamclass": "coreos"}, want: true},
		{name: "accepts bootc label", labels: map[string]string{"containers.bootc": "1"}, want: true},
		{name: "accepts extensions label", labels: map[string]string{"io.openshift.os.extensions": "true"}, want: true},
		{name: "accepts release label", labels: map[string]string{"io.openshift.release": "5.0.0"}, want: true},
		{name: "rejects unrelated labels", labels: map[string]string{"io.openshift.build.commit.id": "abc123"}, want: false},
		{name: "rejects empty labels", labels: map[string]string{}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &imageutils.InspectionCacheEntry{Labels: tt.labels}
			assert.Equal(t, tt.want, filter("sha256:aaa", entry))
		})
	}
}
