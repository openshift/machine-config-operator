package build

// This code is copy/pasted from
// https://raw.githubusercontent.com/openshift/client-go/master/build/clientset/versioned/fake/clientset_generated.go.
// All of this is necessary because the Instantiate() method on the official
// generated FakeClient panics because of an incorrect type assertion :P.

import (
	"context"
	"fmt"

	"github.com/openshift/client-go/build/clientset/versioned/typed/build/v1/fake"

	fakebuildclient "github.com/openshift/client-go/build/clientset/versioned/fake"

	v1 "github.com/openshift/client-go/build/clientset/versioned/typed/build/v1"

	buildv1 "github.com/openshift/api/build/v1"
	applyconfigurationsbuildv1 "github.com/openshift/client-go/build/applyconfigurations/build/v1"
	clientset "github.com/openshift/client-go/build/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	rest "k8s.io/client-go/rest"
	"k8s.io/client-go/testing"
)

var _ v1.BuildV1Interface = &wrappedFakeBuildClient{}
var _ v1.BuildConfigInterface = &wrappedFakeBuildConfigs{}
var _ clientset.Interface = &wrappedFakeBuildClientset{}

type wrappedFakeBuildClientset struct {
	cs            clientset.Interface
	buildv1Client *wrappedFakeBuildClient
}

func newWrappedFakeBuildClientset(obj ...runtime.Object) *wrappedFakeBuildClientset {
	return &wrappedFakeBuildClientset{
		cs:            fakebuildclient.NewSimpleClientset(obj...),
		buildv1Client: newWrappedFakeBuildClient(obj...),
	}
}

func (c *wrappedFakeBuildClientset) Discovery() discovery.DiscoveryInterface {
	return c.cs.Discovery()
}

func (c *wrappedFakeBuildClientset) BuildV1() v1.BuildV1Interface {
	return c.buildv1Client
}

type wrappedFakeBuildClient struct {
	client v1.BuildV1Interface
}

func newWrappedFakeBuildClient(obj ...runtime.Object) *wrappedFakeBuildClient {
	return &wrappedFakeBuildClient{
		client: fakebuildclient.NewSimpleClientset(obj...).BuildV1(),
	}
}

func (w *wrappedFakeBuildClient) Builds(namespace string) v1.BuildInterface {
	return w.client.Builds(namespace)
}

func (w *wrappedFakeBuildClient) BuildConfigs(namespace string) v1.BuildConfigInterface {
	return newWrappedFakeBuildConfigs(w.client, namespace)
}

func (w *wrappedFakeBuildClient) RESTClient() rest.Interface {
	return w.client.RESTClient()
}

type wrappedFakeBuildConfigs struct {
	parentClient v1.BuildV1Interface
	client       *fake.FakeBuildConfigs
	namespace    string
}

func newWrappedFakeBuildConfigs(client v1.BuildV1Interface, namespace string) *wrappedFakeBuildConfigs {
	return &wrappedFakeBuildConfigs{
		parentClient: client,
		client:       client.BuildConfigs(namespace).(*fake.FakeBuildConfigs),
		namespace:    namespace,
	}
}

func (c *wrappedFakeBuildConfigs) Get(ctx context.Context, name string, options metav1.GetOptions) (result *buildv1.BuildConfig, err error) {
	return c.client.Get(ctx, name, options)
}

func (c *wrappedFakeBuildConfigs) List(ctx context.Context, opts metav1.ListOptions) (result *buildv1.BuildConfigList, err error) {
	return c.client.List(ctx, opts)
}

func (c *wrappedFakeBuildConfigs) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.client.Watch(ctx, opts)
}

func (c *wrappedFakeBuildConfigs) Create(ctx context.Context, buildConfig *buildv1.BuildConfig, opts metav1.CreateOptions) (result *buildv1.BuildConfig, err error) {
	return c.client.Create(ctx, buildConfig, opts)
}

func (c *wrappedFakeBuildConfigs) Update(ctx context.Context, buildConfig *buildv1.BuildConfig, opts metav1.UpdateOptions) (result *buildv1.BuildConfig, err error) {
	return c.client.Update(ctx, buildConfig, opts)
}

func (c *wrappedFakeBuildConfigs) UpdateStatus(ctx context.Context, buildConfig *buildv1.BuildConfig, opts metav1.UpdateOptions) (*buildv1.BuildConfig, error) {
	return c.client.UpdateStatus(ctx, buildConfig, opts)
}

func (c *wrappedFakeBuildConfigs) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return c.client.Delete(ctx, name, opts)
}

func (c *wrappedFakeBuildConfigs) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listopts metav1.ListOptions) error {
	return c.client.DeleteCollection(ctx, opts, listopts)
}

func (c *wrappedFakeBuildConfigs) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *buildv1.BuildConfig, err error) {
	return c.client.Patch(ctx, name, pt, data, opts, subresources...)
}

func (c *wrappedFakeBuildConfigs) Apply(ctx context.Context, buildConfig *applyconfigurationsbuildv1.BuildConfigApplyConfiguration, opts metav1.ApplyOptions) (result *buildv1.BuildConfig, err error) {
	return c.client.Apply(ctx, buildConfig, opts)
}

func (c *wrappedFakeBuildConfigs) ApplyStatus(ctx context.Context, buildConfig *applyconfigurationsbuildv1.BuildConfigApplyConfiguration, opts metav1.ApplyOptions) (result *buildv1.BuildConfig, err error) {
	return c.client.ApplyStatus(ctx, buildConfig, opts)
}

// All of this is necessary because this method doesn't work as it should in the officially-generated FakeClient :P
func (c *wrappedFakeBuildConfigs) Instantiate(ctx context.Context, buildConfigName string, buildRequest *buildv1.BuildRequest, opts metav1.CreateOptions) (result *buildv1.Build, err error) {
	buildconfigsResource := schema.GroupVersionResource{Group: "build.openshift.io", Version: "v1", Resource: "buildconfigs"}

	obj, err := c.client.Fake.
		Invokes(testing.NewCreateSubresourceAction(buildconfigsResource, buildConfigName, "instantiate", c.namespace, buildRequest), &buildv1.Build{})

	if obj == nil {
		return nil, err
	}

	buildConfig, err := c.Get(ctx, buildConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	namespace := buildConfig.Namespace
	if namespace == "" {
		namespace = c.namespace
	}

	// We're looking for builds that belong to this buildconfig, so craft a filter.
	ourBuildReq, err := labels.NewRequirement("buildconfig", selection.In, []string{buildConfig.Name})
	if err != nil {
		return nil, err
	}

	builds, err := c.parentClient.Builds(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.NewSelector().Add(*ourBuildReq).String(),
	})

	if err != nil {
		return nil, err
	}

	b := &buildv1.Build{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("%s-%d", buildConfig.Name, len(builds.Items)+1),
			Labels: map[string]string{
				"buildconfig": buildConfigName,
			},
		},
		Spec: buildv1.BuildSpec{
			CommonSpec:  buildConfig.Spec.CommonSpec,
			TriggeredBy: buildRequest.TriggeredBy,
		},
	}

	return c.parentClient.Builds(namespace).Create(ctx, b, opts)
}
