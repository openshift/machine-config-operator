package buildrequest

import (
	"context"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// The Preparer object creates all of the ephemeral build objects required for
// a build to take place. This includes the ConfigMaps which contains the
// rendered MachineConfig and the Containerfile, as well as cloned copies of
// any secrets required for the build to take place. Similarly, the Preparer
// object knows how to destroy all of the objects that it creates. It does so
// by using a specific label query.
type preparerImpl struct {
	mosb       *mcfgv1alpha1.MachineOSBuild
	mosc       *mcfgv1alpha1.MachineOSConfig
	kubeclient clientset.Interface
	mcfgclient mcfgclientset.Interface
}

func NewPreparer(kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) Preparer {
	return &preparerImpl{
		kubeclient: kubeclient,
		mcfgclient: mcfgclient,
		mosb:       mosb.DeepCopy(),
		mosc:       mosc.DeepCopy(),
	}
}

func (p *preparerImpl) Prepare(ctx context.Context) (BuildRequest, error) {
	br, err := newBuildRequestFromAPI(ctx, p.kubeclient, p.mcfgclient, p.mosb, p.mosc)
	if err != nil {
		return nil, fmt.Errorf("could not get imagebuildrequestopts: %w", err)
	}

	if err := p.createEphemeralBuildObjects(ctx, br); err != nil {
		return nil, fmt.Errorf("could not create ephemeral build objects: %w", err)
	}

	return br, nil
}

func (p *preparerImpl) Clean(ctx context.Context) error {
	klog.Infof("Cleaning up ephemeral objects from build %s", p.mosb.Name)

	selector, err := constants.EphemeralBuildObjectSelectorForSpecificBuild(p.mosb, p.mosc)
	if err != nil {
		return fmt.Errorf("could not instantiate selector: %w", err)
	}

	configmaps, err := p.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return fmt.Errorf("could not list ConfigMaps using selector %q: %w", selector.String(), err)
	}

	for _, configmap := range configmaps.Items {
		if err := p.deleteConfigMap(ctx, configmap.Name); err != nil {
			return fmt.Errorf("could not delete ephemeral configmap %s: %w", configmap.Name, err)
		}
	}

	secrets, err := p.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})

	if err != nil {
		return fmt.Errorf("could not list secrets using selector %q: %w", selector.String(), err)
	}

	for _, secret := range secrets.Items {
		if err := p.deleteSecret(ctx, secret.Name); err != nil {
			return fmt.Errorf("could not delete ephemeral configmap %s: %w", secret.Name, err)
		}
	}

	return nil
}

func (p *preparerImpl) createEphemeralBuildObjects(ctx context.Context, br BuildRequest) error {
	configmaps, err := br.ConfigMaps()
	if err != nil {
		return err
	}

	secrets, err := br.Secrets()
	if err != nil {
		return err
	}

	for _, configmap := range configmaps {
		if err := p.createConfigMap(ctx, configmap); err != nil {
			return fmt.Errorf("could not create ephemeral configmap %s for build %s: %w", configmap.Name, p.mosb.Name, err)
		}
	}

	for _, secret := range secrets {
		if err := p.createSecret(ctx, secret); err != nil {
			return fmt.Errorf("could not create ephemeral secret %s for build %s: %w", secret.Name, p.mosb.Name, err)
		}
	}

	klog.Infof("All ephemeral objects for MachineOSBuild %s created", p.mosb.Name)

	return nil
}

func (p *preparerImpl) createSecret(ctx context.Context, s *corev1.Secret) error {
	recorder := events.NewInMemoryRecorder("BuildController-preparer")
	_, _, err := resourceapply.ApplySecret(ctx, p.kubeclient.CoreV1(), recorder, s)
	if err == nil {
		klog.Infof("Created ephemeral secret %s for build %s", s.Name, p.mosb.Name)
	}
	return err
}

func (p *preparerImpl) createConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	recorder := events.NewInMemoryRecorder("BuildController-preparer")
	_, _, err := resourceapply.ApplyConfigMap(ctx, p.kubeclient.CoreV1(), recorder, cm)
	if err == nil {
		klog.Infof("Created ephemeral ConfigMap %s for build %s", cm.Name, p.mosb.Name)
	}
	return err
}

func (p *preparerImpl) deleteConfigMap(ctx context.Context, name string) error {
	err := p.deleteObject(ctx, name, p.kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace))
	if err == nil {
		klog.Infof("Deleted ephemeral ConfigMap %s for build %s", name, p.mosb.Name)
	}
	return err
}

func (p *preparerImpl) deleteSecret(ctx context.Context, name string) error {
	err := p.deleteObject(ctx, name, p.kubeclient.CoreV1().Secrets(ctrlcommon.MCONamespace))
	if err == nil {
		klog.Infof("Deleted ephemeral secret %s for build %s", name, p.mosb.Name)
	}
	return err
}

func (p *preparerImpl) deleteObject(ctx context.Context, name string, deleter interface {
	Delete(context.Context, string, metav1.DeleteOptions) error
}) error {
	err := deleter.Delete(ctx, name, metav1.DeleteOptions{})

	if err == nil {
		return nil
	}

	if k8serrors.IsNotFound(err) {
		klog.Warningf("%s not found: %s", name, err)
		return nil
	}

	return err
}
