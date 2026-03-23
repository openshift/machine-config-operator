package build

import (
	"context"

	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// Holds the options needed to create a digestfile ConfigMap.
type DigestConfigMapOpts struct {
	ConfigMapName string
	Digest        string
	Namespace     string
	Labels        map[string]string
}

// Creates a ConfigMap with the requested options. Thie is primarily for
// consumption by the machine-os-builder create-digest-configmap helper.
func CreateDigestConfigMap(ctx context.Context, kubeclient clientset.Interface, opts DigestConfigMapOpts) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.ConfigMapName,
			Namespace: opts.Namespace,
			Labels:    opts.Labels,
		},
		Data: map[string]string{
			imagebuilder.DigestConfigMapKey: opts.Digest,
		},
	}

	_, err := kubeclient.CoreV1().ConfigMaps(opts.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	return err
}
