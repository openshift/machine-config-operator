package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/klog/v2"
)

func writeBuilderSecretToTempDir(cs *framework.ClientSet, hostname string) (string, error) {
	secrets, err := cs.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "")
	if err != nil {
		return "", err
	}

	var foundDockerCfg *corev1.Secret
	names := []string{}
	for _, secret := range secrets.Items {
		secret := secret
		names = append(names, secret.Name)
		if strings.HasPrefix(secret.Name, "builder-dockercfg") {
			foundDockerCfg = &secret
			break
		}
	}

	if foundDockerCfg == nil {
		return "", fmt.Errorf("did not find a matching secret, foundDockerCfg: %v", names)
	}

	converted, _, err := canonicalizePullSecretBytes(foundDockerCfg.Data[corev1.DockerConfigKey], hostname)
	if err != nil {
		return "", err
	}

	secretPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(secretPath, converted, 0o755); err != nil {
		return "", err
	}

	klog.Infof("Secret %q has been written to %s", foundDockerCfg.Name, secretPath)

	return secretPath, nil
}

// Converts a legacy Docker pull secret into a more modern representation.
// Essentially, it converts {"registry.hostname.com": {"username": "user"...}}
// into {"auths": {"registry.hostname.com": {"username": "user"...}}}. If it
// encounters a pull secret already in this configuration, it will return the
// input secret as-is. Returns either the supplied data or the newly-configured
// representation of said data, a boolean to indicate whether it was converted,
// and any errors resulting from the conversion process. Additionally, this
// function will add an additional entry for the external cluster image
// registry hostname.
func canonicalizePullSecretBytes(secretBytes []byte, extHostname string) ([]byte, bool, error) {
	type newStyleAuth struct {
		Auths map[string]interface{} `json:"auths,omitempty"`
	}

	// Try marshaling the new-style secret first:
	newStyleDecoded := &newStyleAuth{}
	if err := json.Unmarshal(secretBytes, newStyleDecoded); err != nil {
		return nil, false, fmt.Errorf("could not decode new-style pull secret: %w", err)
	}

	// We have an new-style secret, so we can just return here.
	if len(newStyleDecoded.Auths) != 0 {
		return secretBytes, false, nil
	}

	// We need to convert the legacy-style secret to the new-style.
	oldStyleDecoded := map[string]interface{}{}
	if err := json.Unmarshal(secretBytes, &oldStyleDecoded); err != nil {
		return nil, false, fmt.Errorf("could not decode legacy-style pull secret: %w", err)
	}

	oldStyleDecoded[extHostname] = oldStyleDecoded[internalRegistryHostname]

	out, err := json.Marshal(&newStyleAuth{
		Auths: oldStyleDecoded,
	})

	return out, err == nil, err
}
