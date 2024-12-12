package main

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/openshift/machine-config-operator/devex/cmd/onclustertesting/internal/legacycmds"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"golang.org/x/sync/errgroup"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	buildconstants "github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	apierrs "k8s.io/apimachinery/pkg/api/errors"
)

const (
	defaultLayeredPoolName         string = legacycmds.DefaultLayeredPoolName
	createdByOnClusterBuildsHelper string = legacycmds.CreatedByOnClusterBuildsHelper
	globalPullSecretCloneName      string = "global-pull-secret-copy"
)

func hasOurLabel(labels map[string]string) bool {
	if labels == nil {
		return false
	}

	_, ok := labels[createdByOnClusterBuildsHelper]
	return ok
}

func createPool(cs *framework.ClientSet, poolName string) (*mcfgv1.MachineConfigPool, error) { //nolint:unparam // This may eventually be used.
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: poolName,
			Labels: map[string]string{
				createdByOnClusterBuildsHelper: "",
			},
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			MachineConfigSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      mcfgv1.MachineConfigRoleLabelKey,
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"worker", poolName},
					},
				},
			},
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"node-role.kubernetes.io/" + poolName: "",
				},
			},
		},
	}

	klog.Infof("Creating MachineConfigPool %q", pool.Name)

	_, err := cs.MachineConfigPools().Create(context.TODO(), pool, metav1.CreateOptions{})
	switch {
	case apierrs.IsAlreadyExists(err):
		klog.Infof("MachineConfigPool %q already exists, will reuse", poolName)
	case err != nil && !apierrs.IsAlreadyExists(err):
		return nil, err
	}

	klog.Infof("Waiting for MachineConfigPool %s to get a rendered MachineConfig", poolName)

	if _, err := legacycmds.WaitForRenderedConfigs(cs, poolName, "99-worker-ssh"); err != nil {
		return nil, err
	}

	return cs.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
}

func teardownPool(cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) error {
	err := cs.MachineConfigPools().Delete(context.TODO(), mcp.Name, metav1.DeleteOptions{})
	if apierrs.IsNotFound(err) {
		klog.Infof("MachineConfigPool %s not found", mcp.Name)
		return nil
	}

	if err != nil && !apierrs.IsNotFound(err) {
		return err
	}

	klog.Infof("Deleted MachineConfigPool %s", mcp.Name)
	return deleteAllMachineConfigsForPool(cs, mcp)
}

func deleteAllPoolsWithOurLabel(cs *framework.ClientSet) error {
	pools, err := cs.MachineConfigPools().List(context.TODO(), getListOptsForOurLabel())
	if err != nil {
		return err
	}

	eg := errgroup.Group{}

	for _, pool := range pools.Items {
		pool := pool
		eg.Go(func() error {
			return teardownPool(cs, &pool)
		})
	}

	return eg.Wait()
}

func resetAllNodeAnnotations(cs *framework.ClientSet) error {
	workerPool, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	if err != nil {
		return err
	}

	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, node := range nodes.Items {
		if err := resetNodeAnnotationsAndLabels(cs, workerPool, &node); err != nil {
			return err
		}
	}

	return nil
}

func resetNodeAnnotationsAndLabels(cs *framework.ClientSet, originalPool *mcfgv1.MachineConfigPool, node *corev1.Node) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		expectedNodeRoles := map[string]struct{}{
			"node-role.kubernetes.io/worker":        {},
			"node-role.kubernetes.io/master":        {},
			"node-role.kubernetes.io/control-plane": {},
		}

		for label := range node.Labels {
			_, isExpectedNodeRole := expectedNodeRoles[label]
			if strings.HasPrefix(label, "node-role.kubernetes.io") && !isExpectedNodeRole {
				delete(node.Labels, label)
			}
		}

		if _, ok := node.Labels[helpers.MCPNameToRole(originalPool.Name)]; ok {
			node.Annotations[constants.CurrentMachineConfigAnnotationKey] = originalPool.Spec.Configuration.Name
			node.Annotations[constants.DesiredMachineConfigAnnotationKey] = originalPool.Spec.Configuration.Name
			delete(node.Annotations, constants.CurrentImageAnnotationKey)
			delete(node.Annotations, constants.DesiredImageAnnotationKey)
		}

		_, err = cs.CoreV1Interface.Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
		return err
	})
}

func deleteAllMachineConfigsForPool(cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) error {
	machineConfigs, err := cs.MachineConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	eg := errgroup.Group{}

	for _, mc := range machineConfigs.Items {
		mc := mc
		eg.Go(func() error {
			if _, ok := mc.Annotations[helpers.MCPNameToRole(mcp.Name)]; ok && !strings.HasPrefix(mc.Name, "rendered-") {
				if err := cs.MachineConfigs().Delete(context.TODO(), mc.Name, metav1.DeleteOptions{}); err != nil {
					return err
				}
				klog.Infof("Deleted MachineConfig %s, which belonged to MachineConfigPool %s", mc.Name, mcp.Name)
			}

			return nil
		})
	}

	return eg.Wait()
}

func deleteBuildObjects(cs *framework.ClientSet) error {
	deletionSelectors, err := getSelectorsForDeletion()
	if err != nil {
		return err
	}

	eg := errgroup.Group{}

	for _, selector := range deletionSelectors {
		selector := selector
		eg.Go(func() error {
			return deleteBuildObjectsForSelector(cs, selector)
		})
	}

	return eg.Wait()
}

func getSelectorsForDeletion() ([]labels.Selector, error) {
	selectors := []labels.Selector{}

	requirementsLists := [][]string{
		{
			buildconstants.OnClusterLayeringLabelKey,
			buildconstants.EphemeralBuildObjectLabelKey,
		},
		{
			// TODO: Use constant for this.
			"machineconfiguration.openshift.io/used-by-e2e-test",
		},
		{
			createdByOnClusterBuildsHelper,
		},
	}

	for _, requirementsList := range requirementsLists {
		selector := labels.NewSelector()

		for _, requirement := range requirementsList {
			req, err := labels.NewRequirement(requirement, selection.Exists, []string{})
			if err != nil {
				return nil, fmt.Errorf("could not add requirement %q to selector: %w", requirement, err)
			}
			selector = selector.Add(*req)
		}

		selectors = append(selectors, selector)
	}

	return selectors, nil
}

func deleteBuildObjectsForSelector(cs *framework.ClientSet, selector labels.Selector) error {
	eg := errgroup.Group{}

	klog.Infof("Deleting build objects for selector %q:", selector.String())

	eg.Go(func() error {
		return cleanupConfigMaps(cs, selector)
	})

	eg.Go(func() error {
		return cleanupPods(cs, selector)
	})

	eg.Go(func() error {
		return cleanupSecrets(cs, selector)
	})

	eg.Go(func() error {
		return cleanupImagestreams(cs, selector)
	})

	eg.Go(func() error {
		return cleanupNamespaces(cs, selector)
	})

	return eg.Wait()
}

func cleanupPods(cs *framework.ClientSet, selector labels.Selector) error {
	eg := errgroup.Group{}

	pods, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return err
	}

	for _, pod := range pods.Items {
		pod := pod
		eg.Go(func() error {
			return deleteObjectAndIgnoreIfNotFound(&pod, cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace))
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	if len(pods.Items) > 0 {
		klog.Infof("Cleaned up all Pods for selector %s", selector.String())
	}

	return nil
}

func cleanupConfigMaps(cs *framework.ClientSet, selector labels.Selector) error {
	eg := errgroup.Group{}

	configMaps, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return err
	}

	for _, configMap := range configMaps.Items {
		configMap := configMap
		eg.Go(func() error {
			return deleteObjectAndIgnoreIfNotFound(&configMap, cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace))
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	if len(configMaps.Items) > 0 {
		klog.Infof("Cleaned up all ConfigMaps for selector %s", selector.String())
	}

	return nil
}

func cleanupSecrets(cs *framework.ClientSet, selector labels.Selector) error {
	eg := errgroup.Group{}

	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return err
	}

	for _, secret := range secrets.Items {
		secret := secret
		eg.Go(func() error {
			return deleteObjectAndIgnoreIfNotFound(&secret, cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace))
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	if len(secrets.Items) > 0 {
		klog.Infof("Cleaned up all Secrets for selector %s", selector.String())
	}

	return nil
}

func cleanupImagestreams(cs *framework.ClientSet, selector labels.Selector) error {
	eg := errgroup.Group{}

	isList, err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return err
	}

	for _, is := range isList.Items {
		is := is
		eg.Go(func() error {
			return deleteObjectAndIgnoreIfNotFound(&is, cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace))
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	if len(isList.Items) > 0 {
		klog.Infof("Cleaned up all Imagestreams for selector %s", selector.String())
	}

	return nil
}

func cleanupNamespaces(cs *framework.ClientSet, selector labels.Selector) error {
	eg := errgroup.Group{}

	nsList, err := cs.CoreV1Interface.Namespaces().List(context.TODO(), metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return err
	}

	for _, ns := range nsList.Items {
		ns := ns
		eg.Go(func() error {
			return deleteObjectAndIgnoreIfNotFound(&ns, cs.CoreV1Interface.Namespaces())
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	if len(nsList.Items) > 0 {
		klog.Infof("Cleaned up all Namespaces for selector %s", selector.String())
	}

	return nil
}

type deleter interface {
	Delete(context.Context, string, metav1.DeleteOptions) error
}

func deleteObjectAndIgnoreIfNotFound(obj metav1.Object, deleter deleter) error {
	err := deleter.Delete(context.TODO(), obj.GetName(), metav1.DeleteOptions{})
	if err == nil {
		klog.Infof("Deleted %T %s", obj, obj.GetName())
		return nil
	}

	if apierrs.IsNotFound(err) {
		klog.Infof("%T %s was not found, skipping deletion", obj, obj.GetName())
		return nil
	}

	return fmt.Errorf("could not delete %T %s: %w", obj, obj.GetName(), err)
}

func errIfNotSet(in, name string) error {
	if isEmpty(in) {
		if !strings.HasPrefix(name, "--") {
			name = "--" + name
		}
		return fmt.Errorf("required flag %q not set", name)
	}

	return nil
}

func isNoneSet(in1, in2 string) bool {
	return isEmpty(in1) && isEmpty(in2)
}

func isOnlyOneSet(in1, in2 string) bool {
	if !isEmpty(in1) && !isEmpty(in2) {
		return false
	}

	return true
}

func isEmpty(in string) bool {
	return in == ""
}

func getListOptsForOurLabel() metav1.ListOptions {
	req, err := labels.NewRequirement(createdByOnClusterBuildsHelper, selection.Exists, []string{})
	if err != nil {
		klog.Fatalln(err)
	}

	return metav1.ListOptions{
		LabelSelector: req.String(),
	}
}

func ignoreIsNotFound(err error) error {
	if err == nil {
		return nil
	}

	if apierrs.IsNotFound(err) {
		return nil
	}

	return err
}
