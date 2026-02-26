package osimagestream

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/containers/image/v5/types"
	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

var (
	ErrorNoOSImageStreamAvailable = errors.New("unable to retrieve any OSImageStream from the configured sources")
)

// StreamSource represents a source that can provide OS image stream sets.
type StreamSource interface {
	FetchStreams(ctx context.Context) ([]*v1alpha1.OSImageStreamSet, error)
}

// ImageStreamFactory creates OS image streams for different runtime contexts.
type ImageStreamFactory interface {
	// Create builds an OSImageStream from the configured sources and options.
	Create(ctx context.Context, sysCtx *types.SystemContext, opts CreateOptions) (*v1alpha1.OSImageStream, error)
}

// CreateOptions configures how an OSImageStream is built.
// One of ReleaseImage or ReleaseImageStream must be provided.
type CreateOptions struct {
	// ReleaseImage is the release payload image to be pulled over the network.
	// This field is ignored if ReleaseImageStream is given.
	ReleaseImage string
	// ReleaseImageStream is the payload ImageStream provided.
	ReleaseImageStream *imagev1.ImageStream
	// CliImages are OS images provided via CLI flags.
	CliImages *OSImageTuple
	// ConfigMapLister provides access to the osimageurl ConfigMap.
	ConfigMapLister corelisterv1.ConfigMapLister
	// ExistingOSImageStream is the current CR used to load user defined configuration in the spec.
	ExistingOSImageStream *v1alpha1.OSImageStream
}

// DefaultStreamSourceFactory is the production implementation of ImageStreamFactory.
type DefaultStreamSourceFactory struct {
	imagesExtractor  ImageDataExtractor
	inspectorFactory ImagesInspectorFactory
}

// NewDefaultStreamSourceFactory creates a new DefaultStreamSourceFactory with the provided dependencies.
func NewDefaultStreamSourceFactory(inspectorFactory ImagesInspectorFactory) *DefaultStreamSourceFactory {
	return &DefaultStreamSourceFactory{imagesExtractor: NewImageStreamExtractor(), inspectorFactory: inspectorFactory}
}

// Create builds an OSImageStream from the configured sources and options.
func (f *DefaultStreamSourceFactory) Create(ctx context.Context, sysCtx *types.SystemContext, createOptions CreateOptions) (*v1alpha1.OSImageStream, error) {
	var sources []StreamSource
	imagesInspector := f.inspectorFactory.ForContext(sysCtx)
	if createOptions.CliImages != nil {
		sources = append(sources, NewOSImagesURLStreamSource(NewStaticURLProvider(*createOptions.CliImages), f.imagesExtractor, imagesInspector))
	}
	if createOptions.ConfigMapLister != nil {
		sources = append(sources, NewOSImagesURLStreamSource(NewConfigMapURLProviders(createOptions.ConfigMapLister), f.imagesExtractor, imagesInspector))
	}
	var imageStreamProvider ImageStreamProvider
	//nolint:gocritic // I disagree that this would be more readable with a switch case @pablintino
	if createOptions.ReleaseImageStream != nil {
		imageStreamProvider = NewImageStreamProviderResource(createOptions.ReleaseImageStream)
	} else if createOptions.ReleaseImage != "" {
		imageStreamProvider = NewImageStreamProviderNetwork(imagesInspector, createOptions.ReleaseImage)
	} else {
		return nil, errors.New("one of ReleaseImageStream or ReleaseImage must be specified")
	}
	sources = append(sources, NewImageStreamStreamSource(imagesInspector, imageStreamProvider, f.imagesExtractor))

	streams := collect(ctx, sources)
	if len(streams) == 0 {
		return nil, ErrorNoOSImageStreamAvailable
	}

	builtinDefault, err := getBuiltinDefaultStream(ctx, streams, imageStreamProvider)
	if err != nil {
		return nil, fmt.Errorf("could not resolve builtin default stream: %w", err)
	}

	requestedDefault := GetOSImageStreamSpecDefault(createOptions.ExistingOSImageStream)
	defaultStream, err := getDefaultStreamSet(streams, builtinDefault, requestedDefault)
	if err != nil {
		return nil, fmt.Errorf("could not find default OSImageStream in the available streams: %w", err)
	}

	return newOSImageStream(createOptions.ExistingOSImageStream, streams, defaultStream, builtinDefault), nil
}

// newOSImageStream assembles the OSImageStream CR from the resolved streams, default, and existing spec.
func newOSImageStream(existing *v1alpha1.OSImageStream, streams []v1alpha1.OSImageStreamSet, defaultStream, builtinDefault string) *v1alpha1.OSImageStream {
	var spec *v1alpha1.OSImageStreamSpec
	if existing != nil && existing.Spec != nil {
		spec = existing.Spec.DeepCopy()
	} else {
		spec = &v1alpha1.OSImageStreamSpec{}
	}

	return &v1alpha1.OSImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterInstanceNameOSImageStream,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey:  version.Hash,
				ctrlcommon.BuiltinDefaultStreamAnnotationKey: builtinDefault,
			},
		},
		Spec: spec,
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream:    defaultStream,
			AvailableStreams: streams,
		},
	}
}

// getDefaultStreamSet returns the effective default stream: the user override if valid, otherwise the builtin.
func getDefaultStreamSet(streams []v1alpha1.OSImageStreamSet, builtinDefault, requestedDefaultStream string) (string, error) {
	streamNames := GetStreamSetsNames(streams)

	// If the default stream has been provided, and it exists use it.
	if requestedDefaultStream != "" && slices.Contains(streamNames, requestedDefaultStream) {
		return requestedDefaultStream, nil
	} else if requestedDefaultStream != "" {
		// The requested default stream doesn't exist. Cleanly fail.
		return "", fmt.Errorf("could not find the requested %s default stream in the list of OSImageStreams %s. ", requestedDefaultStream, streamNames)
	}

	return builtinDefault, nil
}

// getBuiltinDefaultStream resolves the default stream by matching the default
// ImageStream tag (e.g. "rhel-coreos") against the collected streams' OS images.
func getBuiltinDefaultStream(ctx context.Context, streams []v1alpha1.OSImageStreamSet, imageStreamProvider ImageStreamProvider) (string, error) {
	payloadImageStream, err := imageStreamProvider.ReadImageStream(ctx)
	if err != nil {
		return "", fmt.Errorf("unable to determine the default stream from payload ImageStream: %w", err)
	}

	defaultTag := getDefaultImageStreamTag()
	for _, tag := range payloadImageStream.Spec.Tags {
		if tag.Name != defaultTag || tag.From == nil || tag.From.Kind != "DockerImage" {
			continue
		}

		for _, stream := range streams {
			if string(stream.OSImage) == tag.From.Name {
				return stream.Name, nil
			}
		}
	}

	return "", errors.New("could not find default stream in the list of OSImageStreams")
}

func collect(ctx context.Context, sources []StreamSource) []v1alpha1.OSImageStreamSet {
	result := make(map[string]v1alpha1.OSImageStreamSet)
	for _, source := range sources {
		streams, err := source.FetchStreams(ctx)
		if err != nil {
			// Do not return: Soft failure, best effort
			klog.Warningf("error fetching OSImageStreams from a source: %v", err)
		}

		for _, stream := range streams {
			_, exists := result[stream.Name]
			if exists {
				// Conflicts are allowed and we simply override the previous content
				klog.V(4).Infof("overriding OSImageStream %s with %s %s", stream.Name, stream.OSImage, stream.OSExtensionsImage)
			}
			result[stream.Name] = *stream
		}
	}
	return slices.Collect(maps.Values(result))
}

func getDefaultImageStreamTag() string {
	imageTag := "rhel-coreos"
	if version.IsFCOS() {
		imageTag = "fedora-coreos"
	} else if version.IsSCOS() {
		imageTag = "stream-coreos"
	}
	return imageTag
}
