package internalreleaseimage

import (
	"context"
	"fmt"
	"strings"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// generateHtpasswdEntry generates an htpasswd-formatted line for the given username
// and password using bcrypt hashing.
func generateHtpasswdEntry(username, password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to generate bcrypt hash: %w", err)
	}
	return fmt.Sprintf("%s:%s", username, string(hash)), nil
}

// HtpasswdMatchesPassword reports whether the given htpasswd line matches
// the provided username and password.
func HtpasswdMatchesPassword(htpasswd, username, password string) bool {
	prefix := username + ":"
	if !strings.HasPrefix(htpasswd, prefix) {
		return false
	}
	hash := []byte(strings.TrimPrefix(htpasswd, prefix))
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

// reconcileHtpasswd ensures the htpasswd field in the IRI auth secret is in
// sync with the password field. If the password has changed (or htpasswd is
// missing), it generates a new bcrypt hash and updates the secret. This is the
// trigger for single-phase credential rotation: the updated htpasswd causes the
// MachineConfig to be re-rendered, which MCDs roll out to nodes. Brief registry
// downtime during the rollout is accepted.
func reconcileHtpasswd(kubeClient clientset.Interface, authSecret *corev1.Secret) (*corev1.Secret, error) {
	password := string(authSecret.Data["password"])
	if password == "" {
		return nil, fmt.Errorf("IRI auth secret %s/%s missing or empty \"password\" field", authSecret.Namespace, authSecret.Name)
	}
	htpasswd := string(authSecret.Data["htpasswd"])

	if HtpasswdMatchesPassword(htpasswd, ctrlcommon.IRIRegistryUsername, password) {
		return authSecret, nil
	}

	klog.V(4).Infof("IRI auth secret htpasswd is out of sync with password, regenerating")

	newHtpasswd, err := generateHtpasswdEntry(ctrlcommon.IRIRegistryUsername, password)
	if err != nil {
		return nil, fmt.Errorf("failed to generate htpasswd: %w", err)
	}

	updated := authSecret.DeepCopy()
	updated.Data["htpasswd"] = []byte(newHtpasswd)

	result, err := kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(
		context.TODO(), updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update IRI auth secret: %w", err)
	}

	klog.Infof("Regenerated IRI auth secret htpasswd for credential rotation (secret %s/%s)", authSecret.Namespace, authSecret.Name)
	return result, nil
}
