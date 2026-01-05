package osimagestream

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/containers/image/v5/types"
	imagev1 "github.com/openshift/api/image/v1"
	v1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/version"
	corev1 "k8s.io/api/core/v1"
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
	// CreateRuntimeSources builds an OSImageStream for runtime operation.
	CreateRuntimeSources(ctx context.Context, releaseImage string, sysCtx *types.SystemContext) (*v1alpha1.OSImageStream, error)
	// CreateBootstrapSources builds an OSImageStream for bootstrap operation.
	CreateBootstrapSources(ctx context.Context, imageStream *imagev1.ImageStream, cliImages *OSImageTuple, sysCtx *types.SystemContext) (*v1alpha1.OSImageStream, error)
}

// DefaultStreamSourceFactory is the production implementation of ImageStreamFactory.
type DefaultStreamSourceFactory struct {
	imagesExtractor  ImageDataExtractor
	cmLister         corelisterv1.ConfigMapLister
	inspectorFactory ImagesInspectorFactory
}

// NewDefaultStreamSourceFactory creates a new DefaultStreamSourceFactory with the provided dependencies.
func NewDefaultStreamSourceFactory(cmLister corelisterv1.ConfigMapLister, inspectorFactory ImagesInspectorFactory) *DefaultStreamSourceFactory {
	return &DefaultStreamSourceFactory{imagesExtractor: NewImageStreamExtractor(), cmLister: cmLister, inspectorFactory: inspectorFactory}
}

// CreateRuntimeSources builds an OSImageStream for runtime operation.
func (f *DefaultStreamSourceFactory) CreateRuntimeSources(ctx context.Context, releaseImage string, sysCtx *types.SystemContext) (*v1alpha1.OSImageStream, error) {
	imagesInspector := f.inspectorFactory.ForContext(sysCtx)
	sources := []StreamSource{
		NewOSImagesURLStreamSource(NewConfigMapURLProviders(f.cmLister), f.imagesExtractor, imagesInspector),
		NewImageStreamStreamSource(imagesInspector, NewImageStreamProviderNetwork(imagesInspector, releaseImage), f.imagesExtractor),
	}
	return BuildOSImageStreamFromSources(ctx, sources)
}

// CreateBootstrapSources builds an OSImageStream for bootstrap operation.
func (f *DefaultStreamSourceFactory) CreateBootstrapSources(ctx context.Context, imageStream *imagev1.ImageStream, cliImages *OSImageTuple, sysCtx *types.SystemContext) (*v1alpha1.OSImageStream, error) {
	var sources []StreamSource
	imagesInspector := f.inspectorFactory.ForContext(sysCtx)
	if cliImages != nil {
		sources = append(sources, NewOSImagesURLStreamSource(NewStaticURLProvider(*cliImages), f.imagesExtractor, imagesInspector))
	}
	if imageStream != nil {
		sources = append(sources, NewImageStreamStreamSource(imagesInspector, NewImageStreamProviderResource(imageStream), f.imagesExtractor))
	}
	return BuildOSImageStreamFromSources(ctx, sources)
}

// BuildOsImageStreamBootstrap builds an OSImageStream for bootstrap using the provided factory.
func BuildOsImageStreamBootstrap(
	ctx context.Context,
	secret *corev1.Secret,
	controllerConfig *v1.ControllerConfig,
	imageStream *imagev1.ImageStream,
	cliImages *OSImageTuple,
	factory ImageStreamFactory,
) (*v1alpha1.OSImageStream, error) {
	sysCtx, err := imageutils.NewSysContextFromControllerConfig(secret, controllerConfig)
	if err != nil {
		return nil, fmt.Errorf("could not prepare for image inspection: %w", err)
	}
	defer func() {
		if err := sysCtx.Cleanup(); err != nil {
			klog.Warningf("Unable to clean resources after OSImageStream inspection: %s", err)
		}
	}()
	return factory.CreateBootstrapSources(ctx, imageStream, cliImages, sysCtx.SysContext)
}

// BuildOsImageStreamRuntime builds an OSImageStream for runtime using the provided factory.
func BuildOsImageStreamRuntime(
	ctx context.Context,
	secret *corev1.Secret,
	controllerConfig *v1.ControllerConfig,
	releaseImage string,
	factory ImageStreamFactory,
) (*v1alpha1.OSImageStream, error) {
	sysCtx, err := imageutils.NewSysContextFromControllerConfig(secret, controllerConfig)
	if err != nil {
		return nil, fmt.Errorf("could not prepare for image inspection: %w", err)
	}
	defer func() {
		if err := sysCtx.Cleanup(); err != nil {
			klog.Warningf("Unable to clean resources after OSImageStream inspection: %s", err)
		}
	}()
	return factory.CreateRuntimeSources(ctx, releaseImage, sysCtx.SysContext)
}

// BuildOSImageStreamFromSources aggregates streams from multiple sources into a single OSImageStream.
func BuildOSImageStreamFromSources(ctx context.Context, sources []StreamSource) (*v1alpha1.OSImageStream, error) {
	streams := collect(ctx, sources)
	if len(streams) == 0 {
		return nil, ErrorNoOSImageStreamAvailable
	}

	// TODO: This logic is temporal till we make an agreement on
	// how to propagate the default stream value (ie, injected by
	// the installer)
	defaultStream, err := getDefaultStreamSet(streams)
	if err != nil {
		return nil, fmt.Errorf("could not find default OSImageStream in the available streams: %w", err)
	}
	return &v1alpha1.OSImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.ClusterInstanceNameOSImageStream,
			Annotations: map[string]string{
				ctrlcommon.ReleaseImageVersionAnnotationKey: version.Hash,
			},
		},
		Spec: &v1alpha1.OSImageStreamSpec{},
		Status: v1alpha1.OSImageStreamStatus{
			DefaultStream:    defaultStream,
			AvailableStreams: streams,
		},
	}, nil
}

func getDefaultStreamSet(streams []v1alpha1.OSImageStreamSet) (string, error) {
	// TODO This logic is temporal. For now, try to locate the RHEL 9 one in best effort
	// Make a copy to avoid modifying the input slice
	streamNames := GetStreamSetsNames(streams)

	// Sort by name length (shortest first) to prefer simpler names
	slices.SortFunc(streamNames, func(a, b string) int {
		return len(a) - len(b)
	})

	for _, stream := range streamNames {
		if (strings.Contains(stream, "coreos-9") || strings.Contains(stream, "9-coreos") || strings.Contains(stream, "9")) && !strings.Contains(stream, "10") {
			return stream, nil
		}
	}
	return "", errors.New("could not find default stream in the list of OSImageStreams. No stream points to RHEL9")
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

// GetDefaultAndAvailableStreamNames retrieves the default and available stream names from the
// OSImageStream CR. It returns an error if the CR doesn't exist or has no default or available set.
// Assisted by: Cursor
func GetDefaultAndAvailableStreamNames(lister mcfglistersv1alpha1.OSImageStreamLister) (defaultStreamName string, availableStreamNames []string, err error) {
	// Grab the cluster's OSImageStream
	osImageStream, err := lister.Get(ctrlcommon.ClusterInstanceNameOSImageStream)
	if err != nil {
		klog.Warningf("Failed to retrieve OSImageStream CR: %v.", err)
		return defaultStreamName, availableStreamNames, err
	}

	// Grab the name of the default OSImageStream
	if osImageStream.Status.DefaultStream == "" {
		klog.V(4).Infof("OSImageStream CR has no default stream set.")
		return defaultStreamName, availableStreamNames, err
	}
	defaultStreamName = osImageStream.Status.DefaultStream

	// Grab the names of the available OSImageStreams
	if len(osImageStream.Status.AvailableStreams) == 0 {
		klog.V(4).Infof("OSImageStream CR has no available streams set.")
		return defaultStreamName, availableStreamNames, err
	}
	availableStreamNames = GetStreamSetsNames(osImageStream.Status.AvailableStreams)

	return defaultStreamName, availableStreamNames, nil
}
