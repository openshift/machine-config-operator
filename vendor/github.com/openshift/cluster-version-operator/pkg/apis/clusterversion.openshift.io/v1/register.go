package v1

import (
	"github.com/openshift/cluster-version-operator/pkg/apis"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// SchemeGroupVersion is group version used to register these objects.
var SchemeGroupVersion = schema.GroupVersion{Group: apis.ClusterVersionGroupName, Version: "v1"}

// Resource takes an unqualified resource and returns a Group qualified
// GroupResource.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

var (
	// SchemeBuilder is the scheme builder for ClusterVersionOperator's types.
	SchemeBuilder runtime.SchemeBuilder
	// localSchemeBuilder and AddToScheme will stay in k8s.io/kubernetes.
	localSchemeBuilder = &SchemeBuilder
	// AddToScheme is the function alias for AddtoScheme.
	AddToScheme = localSchemeBuilder.AddToScheme
)

func init() {
	// We only register manually written functions here. The registration of
	// the generated functions takes place in the generated files. The
	// separation makes the code compile even when the generated files are
	// missing.
	localSchemeBuilder.Register(addKnownTypes)
}

// Adds the list of known types to api.Scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&CVOConfig{},
		&CVOStatus{},
	)

	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
