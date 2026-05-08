package helpers

import (
	"context"
	"fmt"

	routeClient "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/machine-config-operator/test/framework"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const (
	imageRegistryNamespace string = "openshift-image-registry"
	imageRegistryObject    string = "image-registry"
)

// ExposeClusterImageRegistry configures the cluster to expose the internal
// image registry via the clusters' configured external hostname. Returns the
// hostname if succsesful.
func ExposeClusterImageRegistry(ctx context.Context, cs *framework.ClientSet) (string, error) {
	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		return "", err
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return "", err
	}

	rc := routeClient.NewForConfigOrDie(config)

	_, err = rc.RouteV1().Routes(imageRegistryNamespace).Get(ctx, imageRegistryObject, metav1.GetOptions{})
	if err != nil && !apierrs.IsNotFound(err) {
		return "", err
	}

	if apierrs.IsNotFound(err) {
		cmd, err := NewOcCommandWithOutput(cs, "expose", "-n", imageRegistryNamespace, fmt.Sprintf("svc/%s", imageRegistryObject))
		if err != nil {
			return "", fmt.Errorf("could not get oc command: %w", err)
		}
		klog.Infof("Running %s", cmd)
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("could not run %s: %w", cmd, err)
		}
	}

	// Ensure that the route was created.
	_, err = rc.RouteV1().Routes(imageRegistryNamespace).Get(ctx, imageRegistryObject, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	registryPatchSpec := []byte(`{"spec": {"tls": {"insecureEdgeTerminationPolicy": "Redirect", "termination": "reencrypt"}}}`)

	_, err = rc.RouteV1().Routes(imageRegistryNamespace).Patch(ctx, imageRegistryObject, k8stypes.MergePatchType, registryPatchSpec, metav1.PatchOptions{})
	if err != nil {
		return "", fmt.Errorf("could not patch image-registry: %w", err)
	}
	klog.Infof("Patched %s", imageRegistryObject)

	cmd, err := NewOcCommandWithOutput(cs, "-n", mcoNamespace, "policy", "add-role-to-group", "registry-viewer", "system:anonymous")
	if err != nil {
		return "", fmt.Errorf("could not get oc command: %w", err)
	}
	klog.Infof("Running %s", cmd)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("could not run %s: %w", cmd, err)
	}
	klog.Infof("Policies added")

	imgRegistryRoute, err := rc.RouteV1().Routes(imageRegistryNamespace).Get(ctx, imageRegistryObject, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	extHostname := imgRegistryRoute.Spec.Host
	klog.Infof("Cluster image registry exposed using external hostname %s", extHostname)
	return extHostname, err
}

// UnexposeClusterImageRegistry undoes exposing the internal image registry operation. Returns nil if successful.
func UnexposeClusterImageRegistry(ctx context.Context, cs *framework.ClientSet) error {
	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		return err
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}

	rc := routeClient.NewForConfigOrDie(config)

	if err := rc.RouteV1().Routes(imageRegistryNamespace).Delete(ctx, imageRegistryObject, metav1.DeleteOptions{}); err != nil && !apierrs.IsNotFound(err) {
		return err
	}
	klog.Infof("Route for %s deleted", imageRegistryObject)

	if err := cs.Services(imageRegistryNamespace).Delete(ctx, imageRegistryObject, metav1.DeleteOptions{}); err != nil && !apierrs.IsNotFound(err) {
		return err
	}
	klog.Infof("Service for %s deleted", imageRegistryObject)

	cmd, err := NewOcCommandWithOutput(cs, "-n", mcoNamespace, "policy", "remove-role-from-group", "registry-viewer", "system:anonymous")
	if err != nil {
		return fmt.Errorf("could not get oc command: %w", err)
	}
	klog.Infof("Running %s", cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not run oc command %s: %w", cmd, err)
	}
	klog.Infof("Policies removed")

	klog.Infof("Cluster image registry is no longer exposed")

	return nil
}
