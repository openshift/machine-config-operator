package common

import (
	"context"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	routefake "github.com/openshift/client-go/route/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetInternalRegistryHostnames(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name              string
		services          []runtime.Object
		routes            []runtime.Object
		expectedHostnames []string
		errExpected       bool
	}{
		{
			name: "Registry with service and route",
			services: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "image-registry",
						Namespace: "openshift-image-registry",
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{Port: 5000},
						},
					},
				},
			},
			routes: []runtime.Object{
				&routev1.Route{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-route",
						Namespace: "openshift-image-registry",
					},
					Spec: routev1.RouteSpec{
						Host: "default-route-openshift-image-registry.apps.example.com",
					},
				},
			},
			expectedHostnames: []string{
				"image-registry.openshift-image-registry.svc:5000",
				"default-route-openshift-image-registry.apps.example.com",
			},
		},
		{
			name: "Registry with service without port",
			services: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "image-registry",
						Namespace: "openshift-image-registry",
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{},
					},
				},
			},
			routes: []runtime.Object{},
			expectedHostnames: []string{
				"image-registry.openshift-image-registry.svc",
			},
		},
		{
			name:              "Registry not deployed (empty namespace)",
			services:          []runtime.Object{},
			routes:            []runtime.Object{},
			expectedHostnames: []string{},
		},
		{
			name: "Multiple services and routes",
			services: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "image-registry",
						Namespace: "openshift-image-registry",
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{Port: 5000},
						},
					},
				},
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "image-registry-internal",
						Namespace: "openshift-image-registry",
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{Port: 5001},
						},
					},
				},
			},
			routes: []runtime.Object{
				&routev1.Route{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "external-route",
						Namespace: "openshift-image-registry",
					},
					Spec: routev1.RouteSpec{
						Host: "external-route.apps.example.com",
					},
				},
			},
			expectedHostnames: []string{
				"image-registry.openshift-image-registry.svc:5000",
				"image-registry-internal.openshift-image-registry.svc:5001",
				"external-route.apps.example.com",
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			kubeclient := fake.NewSimpleClientset(testCase.services...)
			routeclient := routefake.NewSimpleClientset(testCase.routes...)

			hostnames, err := GetInternalRegistryHostnames(context.TODO(), kubeclient, routeclient)

			if testCase.errExpected {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.ElementsMatch(t, testCase.expectedHostnames, hostnames)
		})
	}
}

func TestIsOpenShiftRegistry(t *testing.T) {
	t.Parallel()

	// Set up a standard registry environment
	services := []runtime.Object{
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "image-registry",
				Namespace: "openshift-image-registry",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{Port: 5000},
				},
			},
		},
	}

	routes := []runtime.Object{
		&routev1.Route{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "default-route",
				Namespace: "openshift-image-registry",
			},
			Spec: routev1.RouteSpec{
				Host: "default-route-openshift-image-registry.apps.example.com",
			},
		},
	}

	testCases := []struct {
		name        string
		imageRef    string
		isInternal  bool
		errExpected bool
	}{
		{
			name:       "Internal registry via service hostname",
			imageRef:   "image-registry.openshift-image-registry.svc:5000/openshift/custom-image:latest",
			isInternal: true,
		},
		{
			name:       "Internal registry via route hostname",
			imageRef:   "default-route-openshift-image-registry.apps.example.com/openshift/custom-image:latest",
			isInternal: true,
		},
		{
			name:       "External registry (Quay)",
			imageRef:   "quay.io/openshift/custom-image:latest",
			isInternal: false,
		},
		{
			name:       "External registry (Docker Hub)",
			imageRef:   "docker.io/library/nginx:latest",
			isInternal: false,
		},
		{
			name:       "External private registry",
			imageRef:   "my-private-registry.example.com:5000/app/image:v1",
			isInternal: false,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			kubeclient := fake.NewSimpleClientset(services...)
			routeclient := routefake.NewSimpleClientset(routes...)

			isInternal, err := IsOpenShiftRegistry(context.TODO(), testCase.imageRef, kubeclient, routeclient)

			if testCase.errExpected {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.isInternal, isInternal)
		})
	}
}

func TestIsOpenShiftRegistryWhenRegistryNotDeployed(t *testing.T) {
	t.Parallel()

	// Empty clients - simulating registry not deployed
	kubeclient := fake.NewSimpleClientset()
	routeclient := routefake.NewSimpleClientset()

	testCases := []struct {
		name     string
		imageRef string
	}{
		{
			name:     "Image that would be internal if registry existed",
			imageRef: "image-registry.openshift-image-registry.svc:5000/openshift/custom-image:latest",
		},
		{
			name:     "External registry image",
			imageRef: "quay.io/openshift/custom-image:latest",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			isInternal, err := IsOpenShiftRegistry(context.TODO(), testCase.imageRef, kubeclient, routeclient)

			// Should not error when registry is not deployed
			require.NoError(t, err)

			// Should return false (not internal) when registry not deployed
			assert.False(t, isInternal, "Should treat as external registry when OpenShift registry is not deployed")
		})
	}
}
