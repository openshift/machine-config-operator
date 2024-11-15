package imagebuilder

import (
	"context"
	"fmt"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/buildrequest"
	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
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

func (p *preparerImpl) Prepare(ctx context.Context) (buildrequest.BuildRequest, error) {
	br, err := buildrequest.NewBuildRequestFromAPI(ctx, p.kubeclient, p.mcfgclient, p.mosb, p.mosc)
	if err != nil {
		return nil, fmt.Errorf("could not get imagebuildrequestopts: %w", err)
	}

	if err := p.createEphemeralBuildObjects(ctx, br); err != nil {
		return nil, fmt.Errorf("could not create ephemeral build objects: %w", err)
	}

	return br, nil
}

func (p *preparerImpl) createEphemeralBuildObjects(ctx context.Context, br buildrequest.BuildRequest) error {
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
	_, err := p.kubeclient.CoreV1().Secrets(commonconsts.MCONamespace).Create(ctx, s, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Created ephemeral secret %q for build %q", s.Name, p.mosb.Name)
		return nil
	}

	if k8serrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}

func (p *preparerImpl) createConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	_, err := p.kubeclient.CoreV1().ConfigMaps(commonconsts.MCONamespace).Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Created ephemeral ConfigMap %q for build %q", cm.Name, p.mosb.Name)
		return nil
	}

	if k8serrors.IsAlreadyExists(err) {
		return nil
	}

	return err
}
