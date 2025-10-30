package osimagestream

import (
	"context"
	"fmt"
	"maps"
	"slices"

	imagev1 "github.com/openshift/api/image/v1"
	v1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/image"
	"github.com/openshift/machine-config-operator/pkg/version"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

type StreamSource interface {
	FetchStreams(ctx context.Context) ([]*v1alpha1.OSImageStreamURLSet, error)
}

func BuildOsImageStreamBootstrap(
	ctx context.Context,
	secret *corev1.Secret,
	temporalCC *v1.ControllerConfig,
	imageStream *imagev1.ImageStream,
	cliParser *CliOSImageStreamParser,
) (*v1alpha1.OSImageStream, error) {
	var sources []StreamSource
	if cliParser != nil {
		sources = append(sources, cliParser)
	}
	if imageStream != nil {
		sysCfgProvider := image.NewSysContextControllerConfigProvider(secret, temporalCC)
		sysCtx, err := sysCfgProvider.BuildSystemContext()
		if err != nil {
			return nil, fmt.Errorf("could not prepare for image inspection: %w", err)
		}

		defer func() {
			if err := sysCfgProvider.Cleanup(sysCtx); err != nil {
				klog.Warningf("Unable to clean resources after OSImageStream inspection: %s", err)
			}
		}()
		sources = append(sources, NewOSImageStreamParser(sysCtx, NewImageStreamProviderResource(imageStream)))
	}
	return BuildOSImageStreamFromSources(ctx, sources)
}

func BuildOsImageStreamRuntime(
	ctx context.Context,
	secret *corev1.Secret,
	controllerConfig *v1.ControllerConfig,
	cmLister corelisterv1.ConfigMapLister,
	releaseImage string,
) (*v1alpha1.OSImageStream, error) {

	sysCfgProvider := image.NewSysContextControllerConfigProvider(secret, controllerConfig)
	sysCtx, err := sysCfgProvider.BuildSystemContext()
	if err != nil {
		return nil, fmt.Errorf("could not prepare for image inspection: %w", err)
	}

	defer func() {
		if err := sysCfgProvider.Cleanup(sysCtx); err != nil {
			klog.Warningf("Unable to clean resources after OSImageStream inspection: %s", err)
		}
	}()

	// TODO @pablintino
	// To avoid breaking changes we firstly load the streams from the configmap
	// After that's done we fetch the ImageStream of the current version (likely this code is code at the first
	// boot of the controller after an MCO update) and get the available images from that ImageStream
	return BuildOSImageStreamFromSources(ctx,
		[]StreamSource{
			NewConfigMapParser(NewOSImageURLConfigMapEtcdSource(cmLister)),
			NewOSImageStreamParser(sysCtx, NewImageStreamProviderNetwork(sysCtx, releaseImage)),
		})
}

func BuildOSImageStreamFromSources(ctx context.Context, sources []StreamSource) (*v1alpha1.OSImageStream, error) {
	streams := collect(ctx, sources)
	if len(streams) == 0 {
		return nil, fmt.Errorf("could not find any OS stream")
	}

	defaultStream := getDefaultStream(streams)
	if defaultStream == nil {
		return nil, fmt.Errorf("could not find default stream %s in the list of OSImageStreams", GetDefaultStreamName())
	}
	return &v1alpha1.OSImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: &v1alpha1.OSImageStreamSpec{},
		Status: &v1alpha1.OSImageStreamStatus{
			DefaultStream:    defaultStream.Name,
			AvailableStreams: streams,
		},
	}, nil
}

func getDefaultStream(streams []v1alpha1.OSImageStreamURLSet) *v1alpha1.OSImageStreamURLSet {
	defaultStream := GetDefaultStreamName()
	for _, stream := range streams {
		if stream.Name == defaultStream {
			return &stream
		}
	}
	return nil
}

func GetDefaultOSImageStream(osImageStream *v1alpha1.OSImageStream) v1alpha1.OSImageStreamURLSet {
	// OSImageStream warranties that the default stream is always available
	if osImageStream == nil || osImageStream.Status == nil {
		return v1alpha1.OSImageStreamURLSet{}
	}
	defaultStream := getDefaultStream(osImageStream.Status.AvailableStreams)
	if defaultStream == nil {
		return v1alpha1.OSImageStreamURLSet{}
	}
	return *defaultStream
}

func collect(ctx context.Context, sources []StreamSource) []v1alpha1.OSImageStreamURLSet {
	result := make(map[string]v1alpha1.OSImageStreamURLSet)
	for _, source := range sources {
		streams, err := source.FetchStreams(ctx)
		if err != nil {
			// Do not return: Soft failure, best effort
			// todo: log
		}

		for _, stream := range streams {
			_, exists := result[stream.Name]
			if exists {
				// Conflict: TODO DEBUG log and override
				// This is expected. For example, the cli args
				// may define the base stream, but an imagestream is
				// passed and it overrides the values
			}
			result[stream.Name] = *stream
		}
	}
	return slices.Collect(maps.Values(result))
}

func GetDefaultStreamName() string {
	if version.IsFCOS() {
		return "fedora-coreos"
	} else if version.IsSCOS() {
		return "stream-coreos"
	}
	return "rhel-coreos"
}
