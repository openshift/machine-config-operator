package osimagestream

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/machine-config-operator/pkg/imageutils"
)

// StreamClassInspector inspects a container image URL and returns its OS Image
// Stream name.
type StreamClassInspector interface {
	InspectStreamClass(ctx context.Context, imageURL string) (string, error)
}

type streamClassInspector struct {
	inspectorFactory ImagesInspectorFactory
	sysCtxFactory    imageutils.SysContextFactory
}

// NewStreamClassInspector creates a StreamClassInspector that resolves the OS
// Image Stream name by inspecting image labels via the given factory.
func NewStreamClassInspector(inspectorFactory ImagesInspectorFactory, sysCtxFactory imageutils.SysContextFactory) StreamClassInspector {
	return &streamClassInspector{
		inspectorFactory: inspectorFactory,
		sysCtxFactory:    sysCtxFactory,
	}
}

func (s *streamClassInspector) InspectStreamClass(ctx context.Context, imageURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	inspector := s.inspectorFactory.ForContext(s.sysCtxFactory)
	return inspectStreamClass(ctx, inspector, imageURL)
}

func inspectStreamClass(ctx context.Context, inspector ImagesInspector, imageURL string) (string, error) {
	results, err := inspector.Inspect(ctx, imageURL)
	if err != nil {
		return "", fmt.Errorf("failed to inspect OS image: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no inspection result for OS image")
	}
	if results[0].Error != nil {
		return "", fmt.Errorf("failed to inspect OS image: %w", results[0].Error)
	}
	if results[0].InspectInfo == nil {
		return "", nil
	}

	extractor := NewImageStreamExtractor()
	imageData := extractor.GetImageData(imageURL, results[0].InspectInfo.Labels)
	if imageData == nil {
		return "", nil
	}
	return imageData.Stream, nil
}
