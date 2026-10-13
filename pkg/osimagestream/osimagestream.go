package osimagestream

import (
	"cmp"
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
	k8sversion "k8s.io/apimachinery/pkg/util/version"
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
	// InstallVersion is the OCP version the cluster was originally installed with.
	// Optional: used as a fallback when the ImageStream name cannot be parsed as a version.
	InstallVersion *k8sversion.Version
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

	defaultStream, err := getDefaultStreamSet(streams, &createOptions)
	if err != nil {
		return nil, fmt.Errorf("could not find default OSImageStream in the available streams: %w", err)
	}

	return newOSImageStream(createOptions.ExistingOSImageStream, streams, defaultStream), nil
}

// newOSImageStream assembles the OSImageStream CR from the resolved streams, default, and existing spec.
func newOSImageStream(existing *v1alpha1.OSImageStream, streams []v1alpha1.OSImageStreamSet, defaultStream string) *v1alpha1.OSImageStream {
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
				ctrlcommon.ReleaseImageVersionAnnotationKey: version.Hash,
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
func getDefaultStreamSet(streams []v1alpha1.OSImageStreamSet, createOptions *CreateOptions) (string, error) {
	streamNames := GetStreamSetsNames(streams)

	existingOSImageStream := createOptions.ExistingOSImageStream
	requestedDefaultStream := GetOSImageStreamSpecDefault(existingOSImageStream)
	if requestedDefaultStream == "" && existingOSImageStream != nil && existingOSImageStream.Status.DefaultStream != "" {
		// If the spec is empty but the CR is already populated pick whatever existed
		requestedDefaultStream = existingOSImageStream.Status.DefaultStream
	}
	if requestedDefaultStream == "" {
		// No user preference: if there's only one stream just use it,
		// otherwise fall back to the builtin default.
		if len(streams) == 1 {
			return streams[0].Name, nil
		}
		builtinDefault, err := GetBuiltinDefaultStreamName(createOptions.InstallVersion)
		if err != nil {
			return "", fmt.Errorf("could not determine the builtin default stream: %w", err)
		}
		requestedDefaultStream = builtinDefault
	}

	if slices.Contains(streamNames, requestedDefaultStream) {
		return requestedDefaultStream, nil
	}

	return "", fmt.Errorf("could not find the requested %s default stream in the list of OSImageStreams %s", requestedDefaultStream, streamNames)
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
	// Sort by name for consistent ordering across reconciliations
	streams := slices.Collect(maps.Values(result))
	slices.SortFunc(streams, func(a, b v1alpha1.OSImageStreamSet) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return streams
}
