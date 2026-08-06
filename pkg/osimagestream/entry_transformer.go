package osimagestream

import (
	"bytes"
	"fmt"
	"strings"

	imagev1 "github.com/openshift/api/image/v1"
	"github.com/openshift/client-go/image/clientset/versioned/scheme"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
)

// NewCacheEntryFilter returns a filter that selects cache entries relevant
// for OS stream discovery: those carrying at least one OS, extensions, bootc,
// or release label.
func NewCacheEntryFilter() imageutils.CacheEntryFilter {
	return imageutils.NewCacheEntryFilter(
		coreOSLabelStreamClass,
		coreOSLabelBootc,
		coreOSLabelExtension,
		releasePayloadLabel,
	)
}

// NewCacheEntryTransformer returns a transformer that strips unneeded tags
// from image-references files in cache entries.
func NewCacheEntryTransformer() imageutils.CacheEntryTransformer {
	return NewImageStreamFileTransformer()
}

// NewImageStreamFileTransformer returns an EntryTransformer that strips
// unneeded tags from cached image-references files. Only tags that match
// the OS/extensions pattern or carry the OpenShift OS build annotation are
// kept. This reduces a typical ~30-40 KB manifest to ~2-3 KB.
func NewImageStreamFileTransformer() imageutils.CacheEntryTransformer {
	return imageutils.NewCacheFileTransformer(releaseImageStreamLocation, filterImageStreamTags)
}

func filterImageStreamTags(data []byte) ([]byte, error) {
	obj, err := runtime.Decode(scheme.Codecs.UniversalDecoder(imagev1.SchemeGroupVersion), data)
	if err != nil {
		return data, nil
	}

	is, ok := obj.(*imagev1.ImageStream)
	if !ok {
		return data, nil
	}

	filtered := make([]imagev1.TagReference, 0, len(is.Spec.Tags))
	for _, tag := range is.Spec.Tags {
		if shouldKeepTag(tag) {
			filtered = append(filtered, tag)
		}
	}
	is.Spec.Tags = filtered

	serializer := json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme,
		json.SerializerOptions{Yaml: false, Pretty: false})
	var buf bytes.Buffer
	if err := serializer.Encode(is, &buf); err != nil {
		return nil, fmt.Errorf("encoding filtered ImageStream: %w", err)
	}
	return buf.Bytes(), nil
}

func shouldKeepTag(tag imagev1.TagReference) bool {
	if tag.From == nil || tag.From.Kind != "DockerImage" {
		return false
	}
	if tag.Annotations != nil {
		if source, ok := tag.Annotations[osSourceAnnotation]; ok && strings.Contains(source, osSourceRepo) {
			return true
		}
	}
	return imageTagRegxpr.MatchString(tag.Name)
}
