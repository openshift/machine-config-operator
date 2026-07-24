package osimagestream

import (
	"context"
	"errors"
	"testing"

	"github.com/containers/image/v5/types"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noopSysCtxFactory() (*imageutils.SysContext, error) {
	return &imageutils.SysContext{}, nil
}

func TestInspectStreamClass(t *testing.T) {
	const testImage = "quay.io/openshift/rhcos@sha256:abc123"

	tests := []struct {
		name      string
		inspector ImagesInspector
		wantClass string
		wantErr   bool
	}{
		{
			name: "RHEL 10",
			inspector: &mockImagesInspector{
				inspectData: map[string]*types.ImageInspectInfo{
					testImage: {Labels: map[string]string{
						"io.openshift.os.streamclass": "rhel-10",
						"containers.bootc":            "1",
					}},
				},
			},
			wantClass: "rhel-10",
		},
		{
			name: "RHEL 9",
			inspector: &mockImagesInspector{
				inspectData: map[string]*types.ImageInspectInfo{
					testImage: {Labels: map[string]string{
						"io.openshift.os.streamclass": "rhel-9",
						"containers.bootc":            "true",
					}},
				},
			},
			wantClass: "rhel-9",
		},
		{
			name: "CentOS 10",
			inspector: &mockImagesInspector{
				inspectData: map[string]*types.ImageInspectInfo{
					testImage: {Labels: map[string]string{
						"io.openshift.os.streamclass": "centos-10",
						"containers.bootc":            "1",
					}},
				},
			},
			wantClass: "centos-10",
		},
		{
			name: "no stream class label",
			inspector: &mockImagesInspector{
				inspectData: map[string]*types.ImageInspectInfo{
					testImage: {Labels: map[string]string{
						"containers.bootc": "true",
					}},
				},
			},
			wantClass: "",
		},
		{
			name: "empty labels",
			inspector: &mockImagesInspector{
				inspectData: map[string]*types.ImageInspectInfo{
					testImage: {Labels: map[string]string{}},
				},
			},
			wantClass: "",
		},
		{
			name: "nil InspectInfo",
			inspector: &mockImagesInspector{
				results: []imageutils.BulkInspectResult{
					{Image: testImage, InspectInfo: nil},
				},
			},
			wantClass: "",
		},
		{
			name:      "inspector returns error",
			inspector: &mockImagesInspector{err: errors.New("connection refused")},
			wantErr:   true,
		},
		{
			name: "result contains error",
			inspector: &mockImagesInspector{
				results: []imageutils.BulkInspectResult{
					{Image: testImage, Error: errors.New("manifest unknown")},
				},
			},
			wantErr: true,
		},
		{
			name: "empty results",
			inspector: &mockImagesInspector{
				results: []imageutils.BulkInspectResult{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factory := &mockImagesInspectorFactory{inspector: tt.inspector}
			sci := NewStreamClassInspector(factory, noopSysCtxFactory)
			sc, err := sci.InspectStreamClass(context.Background(), testImage)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantClass, sc)
		})
	}
}
