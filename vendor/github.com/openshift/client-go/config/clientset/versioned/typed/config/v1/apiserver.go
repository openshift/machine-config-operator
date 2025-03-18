// Code generated by client-gen. DO NOT EDIT.

package v1

import (
	context "context"

	configv1 "github.com/openshift/api/config/v1"
	applyconfigurationsconfigv1 "github.com/openshift/client-go/config/applyconfigurations/config/v1"
	scheme "github.com/openshift/client-go/config/clientset/versioned/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"
)

// APIServersGetter has a method to return a APIServerInterface.
// A group's client should implement this interface.
type APIServersGetter interface {
	APIServers() APIServerInterface
}

// APIServerInterface has methods to work with APIServer resources.
type APIServerInterface interface {
	Create(ctx context.Context, aPIServer *configv1.APIServer, opts metav1.CreateOptions) (*configv1.APIServer, error)
	Update(ctx context.Context, aPIServer *configv1.APIServer, opts metav1.UpdateOptions) (*configv1.APIServer, error)
	// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
	UpdateStatus(ctx context.Context, aPIServer *configv1.APIServer, opts metav1.UpdateOptions) (*configv1.APIServer, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*configv1.APIServer, error)
	List(ctx context.Context, opts metav1.ListOptions) (*configv1.APIServerList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *configv1.APIServer, err error)
	Apply(ctx context.Context, aPIServer *applyconfigurationsconfigv1.APIServerApplyConfiguration, opts metav1.ApplyOptions) (result *configv1.APIServer, err error)
	// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
	ApplyStatus(ctx context.Context, aPIServer *applyconfigurationsconfigv1.APIServerApplyConfiguration, opts metav1.ApplyOptions) (result *configv1.APIServer, err error)
	APIServerExpansion
}

// aPIServers implements APIServerInterface
type aPIServers struct {
	*gentype.ClientWithListAndApply[*configv1.APIServer, *configv1.APIServerList, *applyconfigurationsconfigv1.APIServerApplyConfiguration]
}

// newAPIServers returns a APIServers
func newAPIServers(c *ConfigV1Client) *aPIServers {
	return &aPIServers{
		gentype.NewClientWithListAndApply[*configv1.APIServer, *configv1.APIServerList, *applyconfigurationsconfigv1.APIServerApplyConfiguration](
			"apiservers",
			c.RESTClient(),
			scheme.ParameterCodec,
			"",
			func() *configv1.APIServer { return &configv1.APIServer{} },
			func() *configv1.APIServerList { return &configv1.APIServerList{} },
		),
	}
}
