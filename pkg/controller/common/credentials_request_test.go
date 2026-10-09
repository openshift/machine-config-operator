package common

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestVSphereCredentialsRequest(t *testing.T) {
	type credentialsRequest struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name        string            `json:"name"`
			Namespace   string            `json:"namespace"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			SecretRef struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"secretRef"`
			ProviderSpec struct {
				APIVersion string `json:"apiVersion"`
				Kind       string `json:"kind"`
			} `json:"providerSpec"`
		} `json:"spec"`
	}

	raw, err := os.ReadFile("../../../install/0000_80_machine-config_01_credentials-request.yaml")
	require.NoError(t, err)

	request := credentialsRequest{}
	require.NoError(t, yaml.UnmarshalStrict(raw, &request))
	assert.Equal(t, "cloudcredential.openshift.io/v1", request.APIVersion)
	assert.Equal(t, "CredentialsRequest", request.Kind)
	assert.Equal(t, "openshift-machine-config-operator-vsphere", request.Metadata.Name)
	assert.Equal(t, "openshift-cloud-credential-operator", request.Metadata.Namespace)
	assert.Equal(t, "1.0", request.Metadata.Labels["controller-tools.k8s.io"])
	assert.Equal(t, "MachineAPI+CloudCredential", request.Metadata.Annotations["capability.openshift.io/name"])
	assert.Equal(t, "true", request.Metadata.Annotations["include.release.openshift.io/self-managed-high-availability"])
	assert.Equal(t, VSphereCredentialsSecretName, request.Spec.SecretRef.Name)
	assert.Equal(t, MCONamespace, request.Spec.SecretRef.Namespace)
	assert.Equal(t, "cloudcredential.openshift.io/v1", request.Spec.ProviderSpec.APIVersion)
	assert.Equal(t, "VSphereProviderSpec", request.Spec.ProviderSpec.Kind)
}
