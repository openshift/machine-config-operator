// Code generated by lister-gen. DO NOT EDIT.

package v1

import (
	imagev1 "github.com/openshift/api/image/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	listers "k8s.io/client-go/listers"
	cache "k8s.io/client-go/tools/cache"
)

// ImageStreamLister helps list ImageStreams.
// All objects returned here must be treated as read-only.
type ImageStreamLister interface {
	// List lists all ImageStreams in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*imagev1.ImageStream, err error)
	// ImageStreams returns an object that can list and get ImageStreams.
	ImageStreams(namespace string) ImageStreamNamespaceLister
	ImageStreamListerExpansion
}

// imageStreamLister implements the ImageStreamLister interface.
type imageStreamLister struct {
	listers.ResourceIndexer[*imagev1.ImageStream]
}

// NewImageStreamLister returns a new ImageStreamLister.
func NewImageStreamLister(indexer cache.Indexer) ImageStreamLister {
	return &imageStreamLister{listers.New[*imagev1.ImageStream](indexer, imagev1.Resource("imagestream"))}
}

// ImageStreams returns an object that can list and get ImageStreams.
func (s *imageStreamLister) ImageStreams(namespace string) ImageStreamNamespaceLister {
	return imageStreamNamespaceLister{listers.NewNamespaced[*imagev1.ImageStream](s.ResourceIndexer, namespace)}
}

// ImageStreamNamespaceLister helps list and get ImageStreams.
// All objects returned here must be treated as read-only.
type ImageStreamNamespaceLister interface {
	// List lists all ImageStreams in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*imagev1.ImageStream, err error)
	// Get retrieves the ImageStream from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*imagev1.ImageStream, error)
	ImageStreamNamespaceListerExpansion
}

// imageStreamNamespaceLister implements the ImageStreamNamespaceLister
// interface.
type imageStreamNamespaceLister struct {
	listers.ResourceIndexer[*imagev1.ImageStream]
}
