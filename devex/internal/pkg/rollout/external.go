package rollout

import (
	"context"
	"fmt"
	"os/exec"

	routeClient "github.com/openshift/client-go/route/clientset/versioned"
	"github.com/openshift/machine-config-operator/devex/internal/pkg/errors"
	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
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

func ExposeClusterImageRegistry(cs *framework.ClientSet) (string, error) {
	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		return "", err
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return "", err
	}

	rc := routeClient.NewForConfigOrDie(config)

	_, err = rc.RouteV1().Routes(imageRegistryNamespace).Get(context.TODO(), imageRegistryObject, metav1.GetOptions{})
	if err != nil && !apierrs.IsNotFound(err) {
		return "", err
	}

	if apierrs.IsNotFound(err) {
		cmd := exec.Command("oc", "expose", "-n", imageRegistryNamespace, fmt.Sprintf("svc/%s", imageRegistryObject))
		cmd.Env = utils.ToEnvVars(map[string]string{
			"KUBECONFIG": kubeconfig,
		})
		klog.Infof("Running %s", cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", errors.NewExecError(cmd, out, err)
		}
	}

	// Ensure that the route was created.
	_, err = rc.RouteV1().Routes(imageRegistryNamespace).Get(context.TODO(), imageRegistryObject, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	registryPatchSpec := []byte(`{"spec": {"tls": {"insecureEdgeTerminationPolicy": "Redirect", "termination": "reencrypt"}}}`)

	_, err = rc.RouteV1().Routes(imageRegistryNamespace).Patch(context.TODO(), imageRegistryObject, k8stypes.MergePatchType, registryPatchSpec, metav1.PatchOptions{})
	if err != nil {
		return "", fmt.Errorf("could not patch image-registry: %w", err)
	}
	klog.Infof("Patched %s", imageRegistryObject)

	cmd := exec.Command("oc", "-n", commonconsts.MCONamespace, "policy", "add-role-to-group", "registry-viewer", "system:anonymous")
	cmd.Env = utils.ToEnvVars(map[string]string{
		"KUBECONFIG": kubeconfig,
	})
	klog.Infof("Running %s", cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", errors.NewExecError(cmd, out, err)
	}
	klog.Infof("Policies added")

	imgRegistryRoute, err := rc.RouteV1().Routes(imageRegistryNamespace).Get(context.TODO(), imageRegistryObject, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	extHostname := imgRegistryRoute.Spec.Host
	klog.Infof("Cluster image registry exposed using external hostname %s", extHostname)
	return extHostname, err
}

func UnexposeClusterImageRegistry(cs *framework.ClientSet) error {
	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		return err
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}

	rc := routeClient.NewForConfigOrDie(config)

	if err := rc.RouteV1().Routes(imageRegistryNamespace).Delete(context.TODO(), imageRegistryObject, metav1.DeleteOptions{}); err != nil && !apierrs.IsNotFound(err) {
		return err
	}
	klog.Infof("Route for %s deleted", imageRegistryObject)

	if err := cs.Services(imageRegistryNamespace).Delete(context.TODO(), imageRegistryObject, metav1.DeleteOptions{}); err != nil && !apierrs.IsNotFound(err) {
		return err
	}
	klog.Infof("Service for %s deleted", imageRegistryObject)

	cmd := exec.Command("oc", "-n", commonconsts.MCONamespace, "policy", "remove-role-from-group", "registry-viewer", "system:anonymous")
	cmd.Env = utils.ToEnvVars(map[string]string{
		"KUBECONFIG": kubeconfig,
	})

	klog.Infof("Running %s", cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.NewExecError(cmd, out, err)
	}
	klog.Infof("Policies removed")

	klog.Infof("Cluster image registry is no longer exposed")

	return nil
}
