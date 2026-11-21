package common

import (
	"os"
	"regexp"
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

func TestAWSCredentialsRequest(t *testing.T) {
	type statementEntry struct {
		Effect   string   `json:"effect"`
		Action   []string `json:"action"`
		Resource string   `json:"resource"`
	}
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
			ServiceAccountNames []string `json:"serviceAccountNames"`
			ProviderSpec        struct {
				APIVersion       string           `json:"apiVersion"`
				Kind             string           `json:"kind"`
				StatementEntries []statementEntry `json:"statementEntries"`
			} `json:"providerSpec"`
		} `json:"spec"`
	}

	raw, err := os.ReadFile("../../../install/0000_80_machine-config_01_credentials-request-aws.yaml")
	require.NoError(t, err)

	request := credentialsRequest{}
	require.NoError(t, yaml.UnmarshalStrict(raw, &request))
	assert.Equal(t, "cloudcredential.openshift.io/v1", request.APIVersion)
	assert.Equal(t, "CredentialsRequest", request.Kind)
	assert.Equal(t, "openshift-machine-config-operator-aws", request.Metadata.Name)
	assert.Equal(t, "openshift-cloud-credential-operator", request.Metadata.Namespace)
	assert.Equal(t, "1.0", request.Metadata.Labels["controller-tools.k8s.io"])
	assert.Equal(t, "MachineAPI+CloudCredential", request.Metadata.Annotations["capability.openshift.io/name"])
	assert.Equal(t, "true", request.Metadata.Annotations["include.release.openshift.io/self-managed-high-availability"])
	assert.Equal(t, AWSCredentialsSecretName, request.Spec.SecretRef.Name)
	assert.Equal(t, MCONamespace, request.Spec.SecretRef.Namespace)
	assert.Equal(t, "cloudcredential.openshift.io/v1", request.Spec.ProviderSpec.APIVersion)
	assert.Equal(t, "AWSProviderSpec", request.Spec.ProviderSpec.Kind)

	// The boot image controller only calls ec2:DescribeImages, which does not support
	// resource-level permissions. Keep the request limited to exactly that action.
	assert.Equal(t, []statementEntry{{Effect: "Allow", Action: []string{"ec2:DescribeImages"}, Resource: "*"}},
		request.Spec.ProviderSpec.StatementEntries)

	// ccoctl derives the STS role trust policy from secretRef.namespace and
	// serviceAccountNames, so the service account must be the one that runs the
	// boot image controller in the Secret's namespace.
	saManifest, err := os.ReadFile("../../../manifests/machineconfigcontroller/sa.yaml")
	require.NoError(t, err)
	saName := regexp.MustCompile(`(?m)^\s+name:\s*(\S+)\s*$`).FindSubmatch(saManifest)
	require.Len(t, saName, 2, "service account name not found in manifest")
	assert.Equal(t, []string{string(saName[1])}, request.Spec.ServiceAccountNames)
}
