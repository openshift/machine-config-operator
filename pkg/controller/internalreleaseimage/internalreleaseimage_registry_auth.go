package internalreleaseimage

import (
	"context"
	"fmt"
	"strings"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

// htpasswdUpdateTimeout bounds the get/update retry loop that syncs the
// htpasswd field, so a wedged API server cannot block the sync loop.
const htpasswdUpdateTimeout = 30 * time.Second

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
//
// The get/update retry loop runs under a htpasswdUpdateTimeout-bounded child of
// ctx, so it is capped even when the caller's context has no deadline and it
// still unblocks promptly when the controller shuts down.
func reconcileHtpasswd(ctx context.Context, kubeClient clientset.Interface, authSecret *corev1.Secret) (*corev1.Secret, error) {
	password := string(authSecret.Data["password"])
	if password == "" {
		return nil, fmt.Errorf("IRI auth secret %s/%s missing or empty \"password\" field", authSecret.Namespace, authSecret.Name)
	}
	htpasswd := string(authSecret.Data["htpasswd"])

	if HtpasswdMatchesPassword(htpasswd, ctrlcommon.IRIRegistryUsername, password) {
		return authSecret, nil
	}

	klog.V(4).Infof("IRI auth secret htpasswd is out of sync with password, regenerating")

	ctx, cancel := context.WithTimeout(ctx, htpasswdUpdateTimeout)
	defer cancel()

	var result *corev1.Secret
	if err := retry.RetryOnConflict(updateBackoff, func() error {
		// Re-read the secret so the htpasswd is applied on top of the latest
		// resourceVersion. The password may have been rotated again since the
		// lister copy was taken, so re-derive everything from what we just read.
		latest, err := kubeClient.CoreV1().Secrets(authSecret.Namespace).Get(ctx, authSecret.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		latestPassword := string(latest.Data["password"])
		if latestPassword == "" {
			return fmt.Errorf("IRI auth secret %s/%s missing or empty \"password\" field", latest.Namespace, latest.Name)
		}
		if HtpasswdMatchesPassword(string(latest.Data["htpasswd"]), ctrlcommon.IRIRegistryUsername, latestPassword) {
			// Another writer already synced it.
			result = latest
			return nil
		}

		newHtpasswd, err := generateHtpasswdEntry(ctrlcommon.IRIRegistryUsername, latestPassword)
		if err != nil {
			return fmt.Errorf("failed to generate htpasswd: %w", err)
		}
		if latest.Data == nil {
			latest.Data = map[string][]byte{}
		}
		latest.Data["htpasswd"] = []byte(newHtpasswd)

		result, err = kubeClient.CoreV1().Secrets(latest.Namespace).Update(ctx, latest, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Regenerated IRI auth secret htpasswd for credential rotation (secret %s/%s)", latest.Namespace, latest.Name)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to update IRI auth secret: %w", err)
	}

	return result, nil
}
