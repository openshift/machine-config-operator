package common

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
)

// --- test helpers ---

func newIRIRegistryCredentialsSecret(password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      InternalReleaseImageAuthSecretName,
			Namespace: MCONamespace,
		},
		Data: map[string][]byte{
			"password": []byte(password),
		},
	}
}

func cconfigWithDNS(baseDomain string) *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: ControllerConfigName,
		},
		Spec: mcfgv1.ControllerConfigSpec{
			DNS: &configv1.DNS{
				Spec: configv1.DNSSpec{BaseDomain: baseDomain},
			},
		},
	}
}

func newIRIObject() *mcfgv1alpha1.InternalReleaseImage {
	return &mcfgv1alpha1.InternalReleaseImage{
		ObjectMeta: metav1.ObjectMeta{Name: InternalReleaseImageInstanceName},
	}
}

func fgEnabled() FeatureGatesHandler {
	return NewFeatureGatesHardcodedHandler([]configv1.FeatureGateName{features.FeatureGateNoRegistryClusterInstall}, nil)
}

func fgDisabled() FeatureGatesHandler {
	return NewFeatureGatesHardcodedHandler(nil, []configv1.FeatureGateName{features.FeatureGateNoRegistryClusterInstall})
}

func newSecretLister(secrets ...*corev1.Secret) corelistersv1.SecretLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, s := range secrets {
		indexer.Add(s)
	}
	return corelistersv1.NewSecretLister(indexer)
}

func newCCLister(cconfigs ...*mcfgv1.ControllerConfig) mcfglistersv1.ControllerConfigLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, cc := range cconfigs {
		indexer.Add(cc)
	}
	return mcfglistersv1.NewControllerConfigLister(indexer)
}

func newIRILister(iris ...*mcfgv1alpha1.InternalReleaseImage) mcfglistersv1alpha1.InternalReleaseImageLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, iri := range iris {
		indexer.Add(iri)
	}
	return mcfglistersv1alpha1.NewInternalReleaseImageLister(indexer)
}

// --- object-based constructor tests ---

// TestIRISecretMergerFromObjects covers NewIRISecretMergerFromObjects. All checks
// (feature gate, iri presence, credential validation) are deferred to Merge time.
func TestIRISecretMergerFromObjects(t *testing.T) {
	basePullSecret := `{"auths":{"quay.io":{"auth":"dGVzdDp0ZXN0"}}}`
	validSecret := newIRIRegistryCredentialsSecret("testpassword")
	validCconfig := cconfigWithDNS("example.com")
	iri := newIRIObject()

	tests := []struct {
		name            string
		pullSecret      string
		secret          *corev1.Secret
		cconfig         *mcfgv1.ControllerConfig
		fgHandler       FeatureGatesHandler
		iri             *mcfgv1alpha1.InternalReleaseImage
		expectUnchanged bool
		expectError     bool
		verifyAuthHost  string
	}{
		{
			name:           "adds IRI auth entry",
			pullSecret:     basePullSecret,
			secret:         validSecret,
			cconfig:        validCconfig,
			fgHandler:      fgEnabled(),
			iri:            iri,
			verifyAuthHost: "api-int.example.com:22625",
		},
		{
			name:            "feature gate disabled skips merge",
			pullSecret:      basePullSecret,
			secret:          validSecret,
			cconfig:         validCconfig,
			fgHandler:       fgDisabled(),
			iri:             iri,
			expectUnchanged: true,
		},
		{
			name:            "nil iri skips merge",
			pullSecret:      basePullSecret,
			secret:          validSecret,
			cconfig:         validCconfig,
			fgHandler:       fgEnabled(),
			iri:             nil,
			expectUnchanged: true,
		},
		{
			name:        "nil secret returns error",
			pullSecret:  basePullSecret,
			secret:      nil,
			cconfig:     validCconfig,
			fgHandler:   fgEnabled(),
			iri:         iri,
			expectError: true,
		},
		{
			name:        "nil cconfig returns error",
			pullSecret:  basePullSecret,
			secret:      validSecret,
			cconfig:     nil,
			fgHandler:   fgEnabled(),
			iri:         iri,
			expectError: true,
		},
		{
			name:        "nil DNS returns error",
			pullSecret:  basePullSecret,
			secret:      validSecret,
			cconfig:     &mcfgv1.ControllerConfig{},
			fgHandler:   fgEnabled(),
			iri:         iri,
			expectError: true,
		},
		{
			name:        "empty password returns error",
			pullSecret:  basePullSecret,
			secret:      newIRIRegistryCredentialsSecret(""),
			cconfig:     validCconfig,
			fgHandler:   fgEnabled(),
			iri:         iri,
			expectError: true,
		},
		{
			name:            "already up-to-date returns unchanged",
			pullSecret:      pullSecretWithIRIRegistryCredentials("example.com", "testpassword"),
			secret:          validSecret,
			cconfig:         validCconfig,
			fgHandler:       fgEnabled(),
			iri:             iri,
			expectUnchanged: true,
		},
		{
			name:           "updates stale entry",
			pullSecret:     pullSecretWithIRIRegistryCredentials("example.com", "oldpassword"),
			secret:         newIRIRegistryCredentialsSecret("newpassword"),
			cconfig:        validCconfig,
			fgHandler:      fgEnabled(),
			iri:            iri,
			verifyAuthHost: "api-int.example.com:22625",
		},
		{
			name:        "invalid JSON returns error",
			pullSecret:  "not-json",
			secret:      validSecret,
			cconfig:     validCconfig,
			fgHandler:   fgEnabled(),
			iri:         iri,
			expectError: true,
		},
		{
			name:        `missing "auths" field returns error`,
			pullSecret:  `{"registry":"quay.io"}`,
			secret:      validSecret,
			cconfig:     validCconfig,
			fgHandler:   fgEnabled(),
			iri:         iri,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merger := NewIRISecretMergerFromObjects(tt.secret, tt.cconfig, tt.fgHandler, tt.iri)
			result, err := merger.Merge([]byte(tt.pullSecret))

			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assertMergeResult(t, tt.pullSecret, tt.verifyAuthHost, tt.expectUnchanged, tt.secret, result)
		})
	}
}

// --- lister-based constructor tests ---

// TestIRISecretMergerFromListers covers NewIRISecretMerger: lazy resolution from
// the informer cache, including the feature gate and IRI resource checks.
func TestIRISecretMergerFromListers(t *testing.T) {
	basePullSecret := `{"auths":{"quay.io":{"auth":"dGVzdDp0ZXN0"}}}`
	secret := newIRIRegistryCredentialsSecret("testpassword")
	cconfig := cconfigWithDNS("example.com")
	iri := newIRIObject()

	tests := []struct {
		name            string
		pullSecret      string
		secrets         []*corev1.Secret
		cconfigs        []*mcfgv1.ControllerConfig
		iris            []*mcfgv1alpha1.InternalReleaseImage
		fgHandler       FeatureGatesHandler
		expectUnchanged bool
		expectError     bool
		verifyAuthHost  string
	}{
		{
			name:           "adds IRI auth entry",
			pullSecret:     basePullSecret,
			secrets:        []*corev1.Secret{secret},
			cconfigs:       []*mcfgv1.ControllerConfig{cconfig},
			iris:           []*mcfgv1alpha1.InternalReleaseImage{iri},
			fgHandler:      fgEnabled(),
			verifyAuthHost: "api-int.example.com:22625",
		},
		{
			name:            "feature gate disabled skips merge",
			pullSecret:      basePullSecret,
			secrets:         []*corev1.Secret{secret},
			cconfigs:        []*mcfgv1.ControllerConfig{cconfig},
			iris:            []*mcfgv1alpha1.InternalReleaseImage{iri},
			fgHandler:       fgDisabled(),
			expectUnchanged: true,
		},
		{
			name:            "IRI resource not found skips merge",
			pullSecret:      basePullSecret,
			secrets:         []*corev1.Secret{secret},
			cconfigs:        []*mcfgv1.ControllerConfig{cconfig},
			iris:            nil,
			fgHandler:       fgEnabled(),
			expectUnchanged: true,
		},
		{
			name:        "secret not found returns error when IRI is enabled",
			pullSecret:  basePullSecret,
			secrets:     nil,
			cconfigs:    []*mcfgv1.ControllerConfig{cconfig},
			iris:        []*mcfgv1alpha1.InternalReleaseImage{iri},
			fgHandler:   fgEnabled(),
			expectError: true,
		},
		{
			name:            "already up-to-date returns unchanged",
			pullSecret:      pullSecretWithIRIRegistryCredentials("example.com", "testpassword"),
			secrets:         []*corev1.Secret{secret},
			cconfigs:        []*mcfgv1.ControllerConfig{cconfig},
			iris:            []*mcfgv1alpha1.InternalReleaseImage{iri},
			fgHandler:       fgEnabled(),
			expectUnchanged: true,
		},
		{
			name:           "updates stale entry",
			pullSecret:     pullSecretWithIRIRegistryCredentials("example.com", "oldpassword"),
			secrets:        []*corev1.Secret{newIRIRegistryCredentialsSecret("newpassword")},
			cconfigs:       []*mcfgv1.ControllerConfig{cconfig},
			iris:           []*mcfgv1alpha1.InternalReleaseImage{iri},
			fgHandler:      fgEnabled(),
			verifyAuthHost: "api-int.example.com:22625",
		},
		{
			name:        "ControllerConfig missing DNS returns error",
			pullSecret:  basePullSecret,
			secrets:     []*corev1.Secret{secret},
			cconfigs:    []*mcfgv1.ControllerConfig{{ObjectMeta: metav1.ObjectMeta{Name: ControllerConfigName}}},
			iris:        []*mcfgv1alpha1.InternalReleaseImage{iri},
			fgHandler:   fgEnabled(),
			expectError: true,
		},
		{
			name:        "secret missing password field returns error",
			pullSecret:  basePullSecret,
			secrets:     []*corev1.Secret{newIRIRegistryCredentialsSecret("")},
			cconfigs:    []*mcfgv1.ControllerConfig{cconfig},
			iris:        []*mcfgv1alpha1.InternalReleaseImage{iri},
			fgHandler:   fgEnabled(),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merger := NewIRISecretMerger(
				newSecretLister(tt.secrets...),
				newCCLister(tt.cconfigs...),
				newIRILister(tt.iris...),
				tt.fgHandler,
			)
			result, err := merger.Merge([]byte(tt.pullSecret))

			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			var resolvedSecret *corev1.Secret
			if len(tt.secrets) > 0 {
				resolvedSecret = tt.secrets[0]
			}
			assertMergeResult(t, tt.pullSecret, tt.verifyAuthHost, tt.expectUnchanged, resolvedSecret, result)
		})
	}
}

// --- shared assertion helper ---

func assertMergeResult(t *testing.T, originalPullSecret, verifyAuthHost string, expectUnchanged bool, secret *corev1.Secret, result []byte) {
	t.Helper()
	if expectUnchanged {
		assert.Equal(t, originalPullSecret, string(result), "pull secret should not change")
		return
	}

	var dockerConfig map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &dockerConfig))

	auths := dockerConfig["auths"].(map[string]interface{})
	password := string(secret.Data["password"])
	expectedAuth := base64.StdEncoding.EncodeToString([]byte("openshift:" + password))

	iriEntry, ok := auths[verifyAuthHost].(map[string]interface{})
	assert.True(t, ok, "IRI auth entry should be present for %s", verifyAuthHost)
	assert.Equal(t, expectedAuth, iriEntry["auth"])

	localEntry, ok := auths[fmt.Sprintf("localhost:%d", IRIRegistryPort)].(map[string]interface{})
	assert.True(t, ok, "IRI auth entry should be present for localhost:%d", IRIRegistryPort)
	assert.Equal(t, expectedAuth, localEntry["auth"])

	_, hasQuay := auths["quay.io"]
	assert.True(t, hasQuay, "original quay.io entry should be preserved")
}

// pullSecretWithIRIRegistryCredentials creates a pull secret JSON that already contains an IRI auth entry.
func pullSecretWithIRIRegistryCredentials(baseDomain string, password string) string {
	authValue := base64.StdEncoding.EncodeToString([]byte("openshift:" + password))
	apiIntHost := fmt.Sprintf("api-int.%s:%d", baseDomain, IRIRegistryPort)
	localHost := fmt.Sprintf("localhost:%d", IRIRegistryPort)
	dockerConfig := map[string]interface{}{
		"auths": map[string]interface{}{
			"quay.io": map[string]interface{}{
				"auth": "dGVzdDp0ZXN0",
			},
			apiIntHost: map[string]interface{}{
				"auth": authValue,
			},
			localHost: map[string]interface{}{
				"auth": authValue,
			},
		},
	}
	b, _ := json.Marshal(dockerConfig)
	return string(b)
}
