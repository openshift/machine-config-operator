package osimagestream

import (
	"context"
	"errors"
	"testing"

	"github.com/containers/image/v5/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamDiscoverer_Discover(t *testing.T) {
	tests := []struct {
		name                string
		images              []string
		inspectData         map[string]*types.ImageInspectInfo
		inspectorErr        error
		expectedStreamNames []string
		expectError         bool
	}{
		{
			name:   "discovers OS and extensions pair",
			images: []string{testStreamDefRHEL9.osImage, testStreamDefRHEL9.extImage},
			inspectData: newTestInspectData(testStreamDefRHEL9),
			expectedStreamNames: []string{"rhel-9"},
		},
		{
			name:                "empty images list returns empty",
			images:              []string{},
			expectedStreamNames: []string{},
		},
		{
			name:         "propagates inspector global error",
			images:       []string{"quay.io/some/image@sha256:aaa"},
			inspectorErr: errors.New("connection refused"),
			expectError:  true,
		},
		{
			name:   "skips images with per-image inspection errors",
			images: []string{"bad-image", testStreamDefRHEL9.osImage, testStreamDefRHEL9.extImage},
			inspectData: newTestInspectData(testStreamDefRHEL9),
			expectedStreamNames: []string{"rhel-9"},
		},
		{
			name:   "skips images without stream labels",
			images: []string{"quay.io/some/app@sha256:aaa", testStreamDefRHEL9.osImage, testStreamDefRHEL9.extImage},
			inspectData: func() map[string]*types.ImageInspectInfo {
				data := newTestInspectData(testStreamDefRHEL9)
				data["quay.io/some/app@sha256:aaa"] = &types.ImageInspectInfo{
					Labels: map[string]string{"unrelated": "label"},
				}
				return data
			}(),
			expectedStreamNames: []string{"rhel-9"},
		},
		{
			name: "discovers multiple streams",
			images: []string{
				testStreamDefRHEL9.osImage, testStreamDefRHEL9.extImage,
				testStreamDefRHEL10.osImage, testStreamDefRHEL10.extImage,
			},
			inspectData:         newTestInspectData(testStreamDefRHEL9, testStreamDefRHEL10),
			expectedStreamNames: []string{"rhel-9", "rhel-10"},
		},
		{
			name:                "incomplete stream pair is discarded",
			images:              []string{testStreamDefRHEL9.osImage},
			inspectData:         newTestInspectData(testStreamDefRHEL9),
			expectedStreamNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			discoverer := NewStreamDiscoverer(
				&mockImagesInspector{inspectData: tt.inspectData, err: tt.inspectorErr},
				NewImageStreamExtractor(),
			)

			streams, err := discoverer.Discover(context.Background(), tt.images)

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, streams)
				return
			}

			require.NoError(t, err)
			require.Len(t, streams, len(tt.expectedStreamNames))

			actualNames := make([]string, 0, len(streams))
			for _, s := range streams {
				actualNames = append(actualNames, s.Name)
			}
			assert.ElementsMatch(t, tt.expectedStreamNames, actualNames)
		})
	}
}
