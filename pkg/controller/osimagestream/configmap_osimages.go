package osimagestream

import (
	"context"
	"fmt"

	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	corelisterv1 "k8s.io/client-go/listers/core/v1"
)

type ConfigMapSource interface {
	GetOSImagesConfigMap(ctx context.Context) (*corev1.ConfigMap, error)
}

type OSImageURLConfigMapEtcdSource struct {
	cmLister corelisterv1.ConfigMapLister
}

func NewOSImageURLConfigMapEtcdSource(cmLister corelisterv1.ConfigMapLister) *OSImageURLConfigMapEtcdSource {
	return &OSImageURLConfigMapEtcdSource{cmLister: cmLister}
}

func (s *OSImageURLConfigMapEtcdSource) GetOSImagesConfigMap(ctx context.Context) (*corev1.ConfigMap, error) {
	cm, err := s.cmLister.ConfigMaps(common.MCONamespace).Get(common.MachineConfigOSImageURLConfigMapName)
	if err != nil {
		return nil, fmt.Errorf("could not get ConfigMap %s: %w", common.MachineConfigOSImageURLConfigMapName, err)
	}
	return cm, nil
}

type ConfigMapParser struct {
	source ConfigMapSource
}

func NewConfigMapParser(source ConfigMapSource) *ConfigMapParser {
	return &ConfigMapParser{source: source}
}

func (e *ConfigMapParser) FetchStreams(ctx context.Context) ([]*v1alpha1.OSImageStreamURLSet, error) {
	configMap, err := e.source.GetOSImagesConfigMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OS Image URLs ConfigMap: %w", err)
	}
	osUrls, err := common.ParseOSImageURLConfigMap(configMap)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OS Image URLs ConfigMap: %w", err)
	}
	return []*v1alpha1.OSImageStreamURLSet{
		{
			Name:                 GetDefaultStreamName(),
			OSExtensionsImageUrl: osUrls.BaseOSExtensionsContainerImage,
			OSImageUrl:           osUrls.BaseOSContainerImage,
		},
	}, nil
}
