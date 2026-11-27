package node

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var _ Getter = (*kubeNodeGetter)(nil)

// Getter loads a Node object. Used to detect the node's MachineConfigPool
// from labels when --pool is not set.
type Getter interface {
	GetNode(ctx context.Context, name string) (*corev1.Node, error)
}

type kubeNodeGetter struct {
	kube kubernetes.Interface
}

// NewKubeNodeGetter returns a Getter backed by the kubernetes clientset.
func NewKubeNodeGetter(kube kubernetes.Interface) Getter {
	return &kubeNodeGetter{kube: kube}
}

func (g *kubeNodeGetter) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	if g == nil || g.kube == nil {
		return nil, fmt.Errorf("node getter is not configured")
	}
	if name == "" {
		return nil, fmt.Errorf("node name must not be empty")
	}
	n, err := g.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, wrapNodeNotFound(name, err)
		}
		return nil, fmt.Errorf("failed to get node %q: %w", name, err)
	}
	return n, nil
}
