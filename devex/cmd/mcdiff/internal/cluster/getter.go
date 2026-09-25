package cluster

import (
	"context"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Getter loads MachineConfigPools and MachineConfigs. A kube client implements
// this; a must-gather loader can later implement the same methods without a
// live cluster.
type Getter interface {
	GetMachineConfigPool(ctx context.Context, name string) (*mcfgv1.MachineConfigPool, error)
	GetMachineConfig(ctx context.Context, name string) (*mcfgv1.MachineConfig, error)
	ListMachineConfigPools(ctx context.Context) ([]*mcfgv1.MachineConfigPool, error)
}

type kubeGetter struct {
	client mcfgclientset.Interface
}

// NewKubeGetter returns a Getter backed by the machineconfiguration clientset.
func NewKubeGetter(client mcfgclientset.Interface) Getter {
	return &kubeGetter{client: client}
}

func (k *kubeGetter) GetMachineConfigPool(ctx context.Context, name string) (*mcfgv1.MachineConfigPool, error) {
	pool, err := k.client.MachineconfigurationV1().MachineConfigPools().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, wrapPoolNotFound(name, err)
		}
		return nil, err
	}
	return pool, nil
}

func (k *kubeGetter) GetMachineConfig(ctx context.Context, name string) (*mcfgv1.MachineConfig, error) {
	return k.client.MachineconfigurationV1().MachineConfigs().Get(ctx, name, metav1.GetOptions{})
}

func (k *kubeGetter) ListMachineConfigPools(ctx context.Context) ([]*mcfgv1.MachineConfigPool, error) {
	list, err := k.client.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]*mcfgv1.MachineConfigPool, 0, len(list.Items))
	for i := range list.Items {
		p := list.Items[i]
		out = append(out, &p)
	}
	return out, nil
}
