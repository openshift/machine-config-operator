package common

import (
	"context"
	"fmt"
	"strings"

	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// GetInternalRegistryHostnames discovers OpenShift internal registry hostnames
// by querying Services and Routes in the openshift-image-registry namespace.
func GetInternalRegistryHostnames(ctx context.Context, kubeclient clientset.Interface, routeclient routeclientset.Interface) ([]string, error) {
	var hostnames []string

	// Get the list of services in the openshift-image-registry namespace (cluster-local)
	services, err := kubeclient.CoreV1().Services("openshift-image-registry").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, svc := range services.Items {
		clusterHostname := fmt.Sprintf("%s.%s.svc", svc.Name, svc.Namespace)
		if len(svc.Spec.Ports) > 0 {
			port := svc.Spec.Ports[0].Port
			hostnames = append(hostnames, fmt.Sprintf("%s:%d", clusterHostname, port))
		} else {
			hostnames = append(hostnames, clusterHostname)
		}
	}

	// Get the list of routes in the openshift-image-registry namespace (external access)
	routes, err := routeclient.RouteV1().Routes("openshift-image-registry").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, route := range routes.Items {
		if route.Spec.Host != "" {
			hostnames = append(hostnames, route.Spec.Host)
		}
	}

	return hostnames, nil
}

// IsOpenShiftRegistry checks if the imageRef points to one of the known internal registry hostnames
func IsOpenShiftRegistry(ctx context.Context, imageRef string, kubeclient clientset.Interface, routeclient routeclientset.Interface) (bool, error) {
	registryHosts, err := GetInternalRegistryHostnames(ctx, kubeclient, routeclient)
	if err != nil {
		return false, err
	}

	for _, host := range registryHosts {
		if strings.HasPrefix(imageRef, host) {
			return true, nil
		}
	}

	return false, nil
}
