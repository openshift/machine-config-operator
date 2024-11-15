package utils

import (
	"context"
	"fmt"

	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	apierrs "k8s.io/apimachinery/pkg/api/errors"

	"github.com/openshift/machine-config-operator/test/framework"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	ClonedObjectLabelKey      string = "machineconfiguration.openshift.io/cloned-by-mco-helpers"
	RecreatableSecretLabelKey string = "machineconfiguration.openshift.io/recreatable-secret"
)

type SecretRef struct {
	Name      string
	Namespace string
}

func (s *SecretRef) String() string {
	return fmt.Sprintf("%s/%s", s.Namespace, s.Name)
}

func CloneSecret(cs *framework.ClientSet, src, dst SecretRef) error {
	originalSecret, err := cs.CoreV1Interface.Secrets(src.Namespace).Get(context.TODO(), src.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get secret %s: %w", src, err)
	}

	return createSecret(cs, prepareSecret(originalSecret, dst, nil))
}

func CloneSecretWithLabels(cs *framework.ClientSet, src, dst SecretRef, addlLabels map[string]string) error {
	originalSecret, err := cs.CoreV1Interface.Secrets(src.Namespace).Get(context.TODO(), src.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get secret %s: %w", src, err)
	}

	return createSecret(cs, prepareSecret(originalSecret, dst, addlLabels))
}

func prepareSecret(originalSecret *corev1.Secret, dstRef SecretRef, addlLabels map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dstRef.Name,
			Namespace: dstRef.Namespace,
			Labels:    getLabelsForClonedObject(addlLabels),
		},
		Data: originalSecret.Data,
		Type: originalSecret.Type,
	}
}

func getLabelsForClonedObject(addlLabels map[string]string) map[string]string {
	out := map[string]string{
		ClonedObjectLabelKey:      "",
		RecreatableSecretLabelKey: "",
	}

	if addlLabels == nil {
		return out
	}

	for k, v := range addlLabels {
		out[k] = v
	}

	return out
}

func createSecret(cs *framework.ClientSet, s *corev1.Secret) error {
	_, err := cs.CoreV1Interface.Secrets(commonconsts.MCONamespace).Create(context.TODO(), s, metav1.CreateOptions{})
	if err == nil {
		klog.Infof("Created secret %q in namespace %q", s.Name, commonconsts.MCONamespace)
		return nil
	}

	if err != nil && !apierrs.IsAlreadyExists(err) {
		return err
	}

	secret, err := cs.CoreV1Interface.Secrets(commonconsts.MCONamespace).Get(context.TODO(), s.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if _, ok := secret.Labels[RecreatableSecretLabelKey]; ok {
		if err := cs.CoreV1Interface.Secrets(commonconsts.MCONamespace).Delete(context.TODO(), s.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}

		return createSecret(cs, s)
	}

	return fmt.Errorf("unmanaged preexisting secret %s already exists, missing label %q", s.Name, RecreatableSecretLabelKey)
}

func CreateOrRecreateSecret(cs *framework.ClientSet, s *corev1.Secret) error {
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}

	s.Labels[RecreatableSecretLabelKey] = ""

	return createSecret(cs, s)
}
