package build

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
)

// DigestConfigMapOpts holds the options needed to create a digestfile ConfigMap from a file.
type DigestConfigMapOpts struct {
	ConfigMapName string
	Namespace     string
	DigestFile    string
	Labels        string
}

// applyDigestConfigMap creates or updates a ConfigMap.
// This function is idempotent: it creates the ConfigMap if it doesn't exist, or updates
// the Data and Labels if it already exists.
func applyDigestConfigMap(ctx context.Context, kubeclient clientset.Interface, cm *corev1.ConfigMap) error {
	_, err := kubeclient.CoreV1().ConfigMaps(cm.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		return nil
	}

	// It is highly unlikely that the ConfigMap already exists. But in the event
	// that it does, we should fetch it and update its data and labels to keep
	// parity with what oc apply does.
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := kubeclient.CoreV1().ConfigMaps(cm.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}

		// Update Data and Labels
		existing.Data = cm.Data
		existing.Labels = cm.Labels

		_, updateErr := kubeclient.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
		return updateErr
	}

	return err
}

// ApplyDigestConfigMapFromFile reads a digest from a file, parses labels, and creates or updates
// a ConfigMap with the digest. This function combines file I/O and ConfigMap operations for
// convenience in the machine-os-builder CLI.
func ApplyDigestConfigMapFromFile(ctx context.Context, kubeclient clientset.Interface, opts DigestConfigMapOpts) error {
	// Read the digest file
	digestBytes, err := os.ReadFile(opts.DigestFile)
	if err != nil {
		return fmt.Errorf("read digestfile: %w", err)
	}

	trimmedDigest := strings.TrimSpace(string(digestBytes))
	if trimmedDigest == "" {
		return fmt.Errorf("digestfile %q is empty", opts.DigestFile)
	}

	// Parse labels from string into map
	configmapLabels, err := labels.ConvertSelectorToLabelsMap(opts.Labels)
	if err != nil {
		return fmt.Errorf("parse labels: %w", err)
	}

	// Default to the MCO namespace if no namespace value is supplied.
	if opts.Namespace == "" {
		opts.Namespace = ctrlcommon.MCONamespace
	}

	// Create the ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.ConfigMapName,
			Namespace: opts.Namespace,
			Labels:    configmapLabels,
		},
		Data: map[string]string{
			imagebuilder.DigestConfigMapKey: trimmedDigest,
		},
	}

	return applyDigestConfigMap(ctx, kubeclient, cm)
}
