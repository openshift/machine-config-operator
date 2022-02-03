// Code generated by client-gen. DO NOT EDIT.

package v1

import (
	"context"
	"time"

	v1 "github.com/openshift/api/config/v1"
	scheme "github.com/openshift/client-go/config/clientset/versioned/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"
)

// ConfigNodesGetter has a method to return a ConfigNodeInterface.
// A group's client should implement this interface.
type ConfigNodesGetter interface {
	ConfigNodes() ConfigNodeInterface
}

// ConfigNodeInterface has methods to work with ConfigNode resources.
type ConfigNodeInterface interface {
	Create(ctx context.Context, configNode *v1.ConfigNode, opts metav1.CreateOptions) (*v1.ConfigNode, error)
	Update(ctx context.Context, configNode *v1.ConfigNode, opts metav1.UpdateOptions) (*v1.ConfigNode, error)
	UpdateStatus(ctx context.Context, configNode *v1.ConfigNode, opts metav1.UpdateOptions) (*v1.ConfigNode, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*v1.ConfigNode, error)
	List(ctx context.Context, opts metav1.ListOptions) (*v1.ConfigNodeList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *v1.ConfigNode, err error)
	ConfigNodeExpansion
}

// configNodes implements ConfigNodeInterface
type configNodes struct {
	client rest.Interface
}

// newConfigNodes returns a ConfigNodes
func newConfigNodes(c *ConfigV1Client) *configNodes {
	return &configNodes{
		client: c.RESTClient(),
	}
}

// Get takes name of the configNode, and returns the corresponding configNode object, and an error if there is any.
func (c *configNodes) Get(ctx context.Context, name string, options metav1.GetOptions) (result *v1.ConfigNode, err error) {
	result = &v1.ConfigNode{}
	err = c.client.Get().
		Resource("confignodes").
		Name(name).
		VersionedParams(&options, scheme.ParameterCodec).
		Do(ctx).
		Into(result)
	return
}

// List takes label and field selectors, and returns the list of ConfigNodes that match those selectors.
func (c *configNodes) List(ctx context.Context, opts metav1.ListOptions) (result *v1.ConfigNodeList, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = &v1.ConfigNodeList{}
	err = c.client.Get().
		Resource("confignodes").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Do(ctx).
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested configNodes.
func (c *configNodes) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		Resource("confignodes").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Watch(ctx)
}

// Create takes the representation of a configNode and creates it.  Returns the server's representation of the configNode, and an error, if there is any.
func (c *configNodes) Create(ctx context.Context, configNode *v1.ConfigNode, opts metav1.CreateOptions) (result *v1.ConfigNode, err error) {
	result = &v1.ConfigNode{}
	err = c.client.Post().
		Resource("confignodes").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(configNode).
		Do(ctx).
		Into(result)
	return
}

// Update takes the representation of a configNode and updates it. Returns the server's representation of the configNode, and an error, if there is any.
func (c *configNodes) Update(ctx context.Context, configNode *v1.ConfigNode, opts metav1.UpdateOptions) (result *v1.ConfigNode, err error) {
	result = &v1.ConfigNode{}
	err = c.client.Put().
		Resource("confignodes").
		Name(configNode.Name).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(configNode).
		Do(ctx).
		Into(result)
	return
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *configNodes) UpdateStatus(ctx context.Context, configNode *v1.ConfigNode, opts metav1.UpdateOptions) (result *v1.ConfigNode, err error) {
	result = &v1.ConfigNode{}
	err = c.client.Put().
		Resource("confignodes").
		Name(configNode.Name).
		SubResource("status").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(configNode).
		Do(ctx).
		Into(result)
	return
}

// Delete takes name of the configNode and deletes it. Returns an error if one occurs.
func (c *configNodes) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return c.client.Delete().
		Resource("confignodes").
		Name(name).
		Body(&opts).
		Do(ctx).
		Error()
}

// DeleteCollection deletes a collection of objects.
func (c *configNodes) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	var timeout time.Duration
	if listOpts.TimeoutSeconds != nil {
		timeout = time.Duration(*listOpts.TimeoutSeconds) * time.Second
	}
	return c.client.Delete().
		Resource("confignodes").
		VersionedParams(&listOpts, scheme.ParameterCodec).
		Timeout(timeout).
		Body(&opts).
		Do(ctx).
		Error()
}

// Patch applies the patch and returns the patched configNode.
func (c *configNodes) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *v1.ConfigNode, err error) {
	result = &v1.ConfigNode{}
	err = c.client.Patch(pt).
		Resource("confignodes").
		Name(name).
		SubResource(subresources...).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return
}
