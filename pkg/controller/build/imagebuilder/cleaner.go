package imagebuilder

import (
	"context"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	tektonclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"k8s.io/klog/v2"
)

// Holds an implementation of the Cleaner interface that solely cleans up the
// ephemeral build objects that were created.
type cleanerImpl struct {
	*baseImageBuilder
}

// Constructs an instance of the cleaner from the MachineOSBuild and
// MachineOSConfig objects. It is possible that the MachineOSConfig can be nil,
// which this tolerates.
func newCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) Cleaner {
	return &cleanerImpl{
		baseImageBuilder: newBaseImageBuilder(kubeclient, mcfgclient, tektonclient, mosb, mosc, nil),
	}
}

// Constructs an instance of the cleaner using a Builder object. This will
// refer to fields on the Builder object to delete ephemeral build objects
// instead of a MachineOSConfig or MachineOSBuild.
func newCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, tektonclient tektonclientset.Interface, builder buildrequest.Builder) Cleaner {
	return &cleanerImpl{
		baseImageBuilder: newBaseImageBuilder(kubeclient, mcfgclient, tektonclient, nil, nil, builder),
	}
}

// Removes all of the ephemeral build objects that were created for the build.
func (c *cleanerImpl) Clean(ctx context.Context) error {
	mosbName, err := c.getMachineOSBuildName()
	if err != nil {
		return err
	}

	selector, err := c.getSelectorForDeletion()
	if err != nil {
		return fmt.Errorf("could not instantiate selector: %w", err)
	}

	klog.Infof("Cleaning up ephemeral objects from build %q using selector %q", mosbName, selector.String())

	configmaps, err := c.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return fmt.Errorf("could not list configmaps: %w", err)
	}

	for _, configmap := range configmaps.Items {
		if err := c.deleteConfigMap(ctx, configmap.Name, mosbName); err != nil {
			return fmt.Errorf("could not delete ephemeral configmap %s: %w", configmap.Name, err)
		}
	}

	secrets, err := c.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return fmt.Errorf("could not list secrets: %w", err)
	}

	for _, secret := range secrets.Items {
		if err := c.deleteSecret(ctx, secret.Name, mosbName); err != nil {
			return fmt.Errorf("could not delete ephemeral configmap %s: %w", secret.Name, err)
		}
	}

	return nil
}

// Deletes a given ConfigMap and tolerates that it was not found so that if
// this is called more than once, it will not error.
func (c *cleanerImpl) deleteConfigMap(ctx context.Context, cmName, mosbName string) error {
	err := c.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(ctx, cmName, metav1.DeleteOptions{})
	if err == nil {
		klog.Infof("Deleted ephemeral ConfigMap %q for build %q", cmName, mosbName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return nil
	}

	return err
}

// Deletes a given Secret and tolerates that it was not found so that if
// this is called more than once, it will not error.
func (c *cleanerImpl) deleteSecret(ctx context.Context, secretName, mosbName string) error {
	err := c.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).Delete(ctx, secretName, metav1.DeleteOptions{})
	if err == nil {
		klog.Infof("Deleted ephemeral secret %q for build %q", secretName, mosbName)
		return nil
	}

	if k8serrors.IsNotFound(err) {
		return nil
	}

	return err
}

// Instantiates the selector to use for looking up ConfigMaps and Secrets to
// delete. It will either use the MachineOSBuild / MachineOSConfig objects or
// it will get those fields from the Builder object.
func (c *cleanerImpl) getSelectorForDeletion() (labels.Selector, error) {
	if c.mosb != nil {
		// This function can tolerate having MachineOSConfig be nil.
		return utils.EphemeralBuildObjectSelectorForSpecificBuild(c.mosb, c.mosc)
	}

	return ephemeralBuildObjectSelectorForBuilder(c.builder)
}

// Resolve circular import issues.
func ephemeralBuildObjectSelectorForBuilder(builder buildrequest.Builder) (labels.Selector, error) {
	mcpName, err := builder.MachineConfigPool()
	if err != nil {
		return nil, fmt.Errorf("could not get selector, missing MachineConfigPool: %w", err)
	}

	renderedMC, err := builder.RenderedMachineConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get selector, missing rendered MachineConfig: %w", err)
	}

	moscName, err := builder.MachineOSConfig()
	if err != nil {
		return nil, fmt.Errorf("could not get selector, missing MachineOSConfig: %w", err)
	}

	return labels.SelectorFromSet(map[string]string{
		constants.TargetMachineConfigPoolLabelKey: mcpName,
		constants.RenderedMachineConfigLabelKey:   renderedMC,
		constants.MachineOSConfigNameLabelKey:     moscName,
	}), nil
}
