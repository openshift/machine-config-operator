package containerruntimeconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	signature "github.com/containers/image/v5/signature"
	apicfgv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The ML-DSA (FIPS 204) keys and certificates in testdata/mldsa are generated
// with OpenSSL 3.6, e.g.:
//
//	openssl genpkey -algorithm ML-DSA-65 | openssl pkey -pubout
//	openssl req -x509 -newkey ML-DSA-87 -nodes -subj /CN=root -days 36500 \
//	  -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign
//
// The ML-DSA-65 and ML-DSA-87 certificates are larger than the 8192 character
// limit that ClusterImagePolicy and ImagePolicy had before they were raised to
// 32768 characters.
func readMLDSATestData(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "mldsa", name))
	require.NoError(t, err)
	return data
}

// renderedSigstoreRequirement is the subset of a containers/image
// sigstoreSigned requirement that carries key and certificate data.
type renderedSigstoreRequirement struct {
	Type               string `json:"type"`
	KeyData            []byte `json:"keyData"`
	RekorPublicKeyData []byte `json:"rekorPublicKeyData"`
	Fulcio             *struct {
		CAData []byte `json:"caData"`
	} `json:"fulcio"`
	PKI *struct {
		CARootsData         []byte `json:"caRootsData"`
		CAIntermediatesData []byte `json:"caIntermediatesData"`
	} `json:"pki"`
}

func renderedRequirement(t *testing.T, policyJSON []byte, scope string) renderedSigstoreRequirement {
	t.Helper()

	// The rendered policy must be loadable by containers/image, which is what
	// CRI-O uses to enforce it.
	_, err := signature.NewPolicyFromBytes(policyJSON)
	require.NoError(t, err)

	var policy struct {
		Transports map[string]map[string][]renderedSigstoreRequirement `json:"transports"`
	}
	require.NoError(t, json.Unmarshal(policyJSON, &policy))
	reqs := policy.Transports["docker"][scope]
	require.Len(t, reqs, 1, "docker requirements for scope %s", scope)
	require.Equal(t, "sigstoreSigned", reqs[0].Type)
	return reqs[0]
}

func TestMLDSAImagePoliciesRender(t *testing.T) {
	mldsa44Key := readMLDSATestData(t, "ml-dsa-44.pub")
	mldsa65Key := readMLDSATestData(t, "ml-dsa-65.pub")
	mldsa87Key := readMLDSATestData(t, "ml-dsa-87.pub")
	mldsa65Root := readMLDSATestData(t, "ml-dsa-65-root.crt")
	mldsa87Root := readMLDSATestData(t, "ml-dsa-87-root.crt")
	mldsa87Intermediate := readMLDSATestData(t, "ml-dsa-87-intermediate.crt")

	pkiSubject := apicfgv1.PKICertificateSubject{Email: "test-user@example.com"}

	testCases := []struct {
		name   string
		policy apicfgv1.ImageSigstoreVerificationPolicy
		verify func(t *testing.T, req renderedSigstoreRequirement)
	}{
		{
			name: "PublicKey ML-DSA-44",
			policy: apicfgv1.ImageSigstoreVerificationPolicy{
				RootOfTrust: apicfgv1.PolicyRootOfTrust{
					PolicyType: apicfgv1.PublicKeyRootOfTrust,
					PublicKey:  &apicfgv1.ImagePolicyPublicKeyRootOfTrust{KeyData: mldsa44Key},
				},
			},
			verify: func(t *testing.T, req renderedSigstoreRequirement) {
				require.Equal(t, mldsa44Key, req.KeyData)
			},
		},
		{
			name: "PublicKey ML-DSA-65",
			policy: apicfgv1.ImageSigstoreVerificationPolicy{
				RootOfTrust: apicfgv1.PolicyRootOfTrust{
					PolicyType: apicfgv1.PublicKeyRootOfTrust,
					PublicKey:  &apicfgv1.ImagePolicyPublicKeyRootOfTrust{KeyData: mldsa65Key},
				},
			},
			verify: func(t *testing.T, req renderedSigstoreRequirement) {
				require.Equal(t, mldsa65Key, req.KeyData)
			},
		},
		{
			name: "PublicKey ML-DSA-87 with an ML-DSA-87 Rekor key",
			policy: apicfgv1.ImageSigstoreVerificationPolicy{
				RootOfTrust: apicfgv1.PolicyRootOfTrust{
					PolicyType: apicfgv1.PublicKeyRootOfTrust,
					PublicKey:  &apicfgv1.ImagePolicyPublicKeyRootOfTrust{KeyData: mldsa87Key, RekorKeyData: mldsa87Key},
				},
			},
			verify: func(t *testing.T, req renderedSigstoreRequirement) {
				require.Equal(t, mldsa87Key, req.KeyData)
				require.Equal(t, mldsa87Key, req.RekorPublicKeyData)
			},
		},
		{
			name: "PKI ML-DSA-65 root",
			policy: apicfgv1.ImageSigstoreVerificationPolicy{
				RootOfTrust: apicfgv1.PolicyRootOfTrust{
					PolicyType: apicfgv1.PKIRootOfTrust,
					PKI: &apicfgv1.ImagePolicyPKIRootOfTrust{
						CertificateAuthorityRootsData: mldsa65Root,
						PKICertificateSubject:         pkiSubject,
					},
				},
			},
			verify: func(t *testing.T, req renderedSigstoreRequirement) {
				require.NotNil(t, req.PKI)
				require.Equal(t, mldsa65Root, req.PKI.CARootsData)
			},
		},
		{
			name: "PKI ML-DSA-87 root and intermediate",
			policy: apicfgv1.ImageSigstoreVerificationPolicy{
				RootOfTrust: apicfgv1.PolicyRootOfTrust{
					PolicyType: apicfgv1.PKIRootOfTrust,
					PKI: &apicfgv1.ImagePolicyPKIRootOfTrust{
						CertificateAuthorityRootsData:         mldsa87Root,
						CertificateAuthorityIntermediatesData: mldsa87Intermediate,
						PKICertificateSubject:                 pkiSubject,
					},
				},
			},
			verify: func(t *testing.T, req renderedSigstoreRequirement) {
				require.NotNil(t, req.PKI)
				require.Equal(t, mldsa87Root, req.PKI.CARootsData)
				require.Equal(t, mldsa87Intermediate, req.PKI.CAIntermediatesData)
			},
		},
		{
			name: "FulcioCAWithRekor ML-DSA-87",
			policy: apicfgv1.ImageSigstoreVerificationPolicy{
				RootOfTrust: apicfgv1.PolicyRootOfTrust{
					PolicyType: apicfgv1.FulcioCAWithRekorRootOfTrust,
					FulcioCAWithRekor: &apicfgv1.ImagePolicyFulcioCAWithRekorRootOfTrust{
						FulcioCAData: mldsa87Root,
						RekorKeyData: mldsa87Key,
						FulcioSubject: apicfgv1.PolicyFulcioSubject{
							OIDCIssuer:  "https://oidc.example.com",
							SignedEmail: "test-user@example.com",
						},
					},
				},
			},
			verify: func(t *testing.T, req renderedSigstoreRequirement) {
				require.NotNil(t, req.Fulcio)
				require.Equal(t, mldsa87Root, req.Fulcio.CAData)
				require.Equal(t, mldsa87Key, req.RekorPublicKeyData)
			},
		},
	}

	templatePolicy := signature.Policy{
		Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
		Transports: map[string]signature.PolicyTransportScopes{
			"docker-daemon": map[string]signature.PolicyRequirements{
				"": {signature.NewPRInsecureAcceptAnything()},
			},
		},
	}
	buf := bytes.Buffer{}
	require.NoError(t, json.NewEncoder(&buf).Encode(templatePolicy))
	templatePolicyBytes := buf.Bytes()

	const (
		scope     = "example.com/mldsa/app"
		namespace = "mldsa-test"
	)

	for _, tc := range testCases {
		t.Run("ClusterImagePolicy "+tc.name, func(t *testing.T) {
			cip := &apicfgv1.ClusterImagePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "mldsa"},
				Spec: apicfgv1.ClusterImagePolicySpec{
					Scopes: []apicfgv1.ImageScope{scope},
					Policy: tc.policy,
				},
			}
			clusterScopePolicies, _, err := getValidScopePolicies([]*apicfgv1.ClusterImagePolicy{cip}, nil, nil)
			require.NoError(t, err)

			policyJSON, err := updatePolicyJSON(templatePolicyBytes, nil, nil, "release-reg.io/image/release", clusterScopePolicies)
			require.NoError(t, err)
			tc.verify(t, renderedRequirement(t, policyJSON, scope))
		})

		t.Run("ImagePolicy "+tc.name, func(t *testing.T) {
			ip := &apicfgv1.ImagePolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "mldsa", Namespace: namespace},
				Spec: apicfgv1.ImagePolicySpec{
					Scopes: []apicfgv1.ImageScope{scope},
					Policy: tc.policy,
				},
			}
			_, scopeNamespacePolicies, err := getValidScopePolicies(nil, []*apicfgv1.ImagePolicy{ip}, nil)
			require.NoError(t, err)

			namespacedPolicyJSONs, err := updateNamespacedPolicyJSONs(templatePolicyBytes, nil, nil, scopeNamespacePolicies)
			require.NoError(t, err)
			require.Contains(t, namespacedPolicyJSONs, namespace)
			tc.verify(t, renderedRequirement(t, namespacedPolicyJSONs[namespace], scope))
		})
	}
}
