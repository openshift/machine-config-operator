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
	"k8s.io/klog/v2"
)

type cleanerImpl struct {
	mosb       *mcfgv1alpha1.MachineOSBuild
	mosc       *mcfgv1alpha1.MachineOSConfig
	kubeclient clientset.Interface
	mcfgclient mcfgclientset.Interface
	builder    buildrequest.Builder
}

func NewCleaner(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) Cleaner {
	c := &cleanerImpl{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		mosb:       mosb.DeepCopy(),
	}

	if mosc != nil {
		c.mosc = mosc.DeepCopy()
	}

	return c
}

func NewCleanerFromBuilder(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, builder buildrequest.Builder) Cleaner {
	return &cleanerImpl{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		builder:    builder,
	}
}

func (c *cleanerImpl) getSelectorForDeletion() (labels.Selector, error) {
	if c.mosb != nil {
		return utils.EphemeralBuildObjectSelectorForSpecificBuild(c.mosb, c.mosc)
	}

	return ephemeralBuildObjectSelectorForBuilder(c.builder)
}

func (c *cleanerImpl) getMachineOSBuildName() (string, error) {
	if c.mosb != nil {
		return c.mosb.Name, nil
	}

	return c.builder.MachineOSBuild()
}

func (c *cleanerImpl) Clean(ctx context.Context) error {
	mosbName, err := c.getMachineOSBuildName()
	if err != nil {
		return err
	}

	klog.Infof("Cleaning up ephemeral objects from build %q", mosbName)

	selector, err := c.getSelectorForDeletion()
	if err != nil {
		return fmt.Errorf("could not instantiate selector: %w", err)
	}

	configmaps, err := c.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	for _, configmap := range configmaps.Items {
		if err := c.deleteConfigMap(ctx, configmap.Name, mosbName); err != nil {
			return fmt.Errorf("could not delete ephemeral configmap %s: %w", configmap.Name, err)
		}
	}

	secrets, err := c.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	for _, secret := range secrets.Items {
		if err := c.deleteSecret(ctx, secret.Name, mosbName); err != nil {
			return fmt.Errorf("could not delete ephemeral configmap %s: %w", secret.Name, err)
		}
	}

	return nil
}

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
