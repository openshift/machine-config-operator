package containerruntimeconfig

import (
	"bytes"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	signature "github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/types"
	storageconfig "github.com/containers/storage/pkg/config"
	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/maps"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestUpdateRegistriesConfig(t *testing.T) {
	templateConfig := sysregistriesv2.V2RegistriesConf{ // This matches templates/*/01-*-container-runtime/_base/files/container-registries.yaml
		UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
	}
	buf := bytes.Buffer{}
	err := toml.NewEncoder(&buf).Encode(templateConfig)
	require.NoError(t, err)
	templateBytes := buf.Bytes()

	tests := []struct {
		name              string
		insecure, blocked []string
		idmsRules         []*apicfgv1.ImageDigestMirrorSet
		itmsRules         []*apicfgv1.ImageTagMirrorSet
		icspRules         []*apioperatorsv1alpha1.ImageContentSourcePolicy
		want              sysregistriesv2.V2RegistriesConf
	}{
		{
			name: "unchanged",
			want: templateConfig,
		},
		{
			name:     "insecure+blocked",
			insecure: []string{"registry.access.redhat.com", "insecure.com", "common.com"},
			blocked:  []string{"blocked.com", "common.com", "docker.io"},
			want: sysregistriesv2.V2RegistriesConf{
				UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
				Registries: []sysregistriesv2.Registry{
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com",
						},
						Blocked: true,
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "common.com",
							Insecure: true,
						},
						Blocked: true,
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "docker.io",
						},
						Blocked: true,
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "registry.access.redhat.com",
							Insecure: true,
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com",
							Insecure: true,
						},
					},
				},
			},
		},
		{
			name:      "imageContentSourcePolicy",
			insecure:  []string{"insecure.com", "*.insecure-example.com", "*.insecure.blocked-example.com"},
			blocked:   []string{"blocked.com", "*.blocked.insecure-example.com", "*.blocked-example.com"},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{},
			itmsRules: []*apicfgv1.ImageTagMirrorSet{},
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []string{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []string{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []string{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			want: sysregistriesv2.V2RegistriesConf{
				UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
				Registries: []sysregistriesv2.Registry{
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com/ns-b/ns2-b",
						},
						Blocked: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "other.com/ns-o2", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "insecure.com/ns-i2", Insecure: true, PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com/ns-i1",
							Insecure: true,
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "blocked.com/ns-b1", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "other.com/ns-o1", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "other.com/ns-o3",
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "insecure.com/ns-i2", Insecure: true, PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "blocked.com/ns-b/ns3-b", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "foo.insecure-example.com/bar", Insecure: true, PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com",
						},
						Blocked: true,
					},
					{
						Prefix:  "*.blocked.insecure-example.com",
						Blocked: true,
						Endpoint: sysregistriesv2.Endpoint{
							Insecure: true,
						},
					},
					{
						Prefix:  "*.blocked-example.com",
						Blocked: true,
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com",
							Insecure: true,
						},
					},
					{
						Prefix: "*.insecure-example.com",
						Endpoint: sysregistriesv2.Endpoint{
							Insecure: true,
						},
					},
					{
						Prefix:  "*.insecure.blocked-example.com",
						Blocked: true,
						Endpoint: sysregistriesv2.Endpoint{
							Insecure: true,
						},
					},
				},
			},
		},
		{
			name:     "insecure+blocked prefixes with wildcard entries",
			insecure: []string{"insecure.com", "*.insecure-example.com", "*.insecure.blocked-example.com"},
			blocked:  []string{"blocked.com", "*.blocked.insecure-example.com", "*.blocked-example.com"},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.ImageMirror{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.ImageMirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.ImageMirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			want: sysregistriesv2.V2RegistriesConf{
				UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
				Registries: []sysregistriesv2.Registry{
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com/ns-b/ns2-b",
						},
						Blocked: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "other.com/ns-o2", PullFromMirror: "digest-only"},
							{Location: "insecure.com/ns-i2", Insecure: true, PullFromMirror: "digest-only"},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com/ns-i1",
							Insecure: true,
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "blocked.com/ns-b1", PullFromMirror: "digest-only"},
							{Location: "other.com/ns-o1", PullFromMirror: "digest-only"},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "other.com/ns-o3",
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "insecure.com/ns-i2", Insecure: true, PullFromMirror: "digest-only"},
							{Location: "blocked.com/ns-b/ns3-b", PullFromMirror: "digest-only"},
							{Location: "foo.insecure-example.com/bar", Insecure: true, PullFromMirror: "digest-only"},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com",
						},
						Blocked: true,
					},
					{
						Prefix:  "*.blocked.insecure-example.com",
						Blocked: true,
						Endpoint: sysregistriesv2.Endpoint{
							Location: "",
							Insecure: true,
						},
					},
					{
						Prefix: "*.blocked-example.com",
						Endpoint: sysregistriesv2.Endpoint{
							Location: "",
						},
						Blocked: true,
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com",
							Insecure: true,
						},
					},
					{
						Prefix: "*.insecure-example.com",
						Endpoint: sysregistriesv2.Endpoint{
							Location: "",
							Insecure: true,
						},
					},
					{
						Prefix:  "*.insecure.blocked-example.com",
						Blocked: true,
						Endpoint: sysregistriesv2.Endpoint{
							Location: "",
							Insecure: true,
						},
					},
				},
			},
		},
		{
			name: "imageTagMirrorSet",
			itmsRules: []*apicfgv1.ImageTagMirrorSet{
				{
					Spec: apicfgv1.ImageTagMirrorSetSpec{
						ImageTagMirrors: []apicfgv1.ImageTagMirrors{
							{Source: "registry-a.com", Mirrors: []apicfgv1.ImageMirror{"mirror-tag-1.registry-a.com"}},
							{Source: "registry-b.com", Mirrors: []apicfgv1.ImageMirror{"mirror-tag-1.registry-b.com"}},
						},
					},
				},
			},
			want: sysregistriesv2.V2RegistriesConf{
				UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
				Registries: []sysregistriesv2.Registry{
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "registry-a.com",
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "mirror-tag-1.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "registry-b.com",
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "mirror-tag-1.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
						},
					},
				},
			},
		},
		{
			name: "imageDigestMirrorSet + imageTagMirrorSet",
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "registry-a.com", Mirrors: []apicfgv1.ImageMirror{"mirror-digest-1.registry-a.com", "mirror-digest-2.registry-a.com"}},
							{Source: "registry-b.com", Mirrors: []apicfgv1.ImageMirror{"mirror-digest-1.registry-b.com", "mirror-digest-2.registry-b.com"}},
						},
					},
				},
			},
			itmsRules: []*apicfgv1.ImageTagMirrorSet{
				{
					Spec: apicfgv1.ImageTagMirrorSetSpec{
						ImageTagMirrors: []apicfgv1.ImageTagMirrors{
							{Source: "registry-a.com", Mirrors: []apicfgv1.ImageMirror{"mirror-tag-1.registry-a.com", "mirror-tag-2.registry-a.com"}},
							{Source: "registry-b.com", Mirrors: []apicfgv1.ImageMirror{"mirror-tag-1.registry-b.com", "mirror-tag-2.registry-b.com"}},
						},
					},
				},
			},

			want: sysregistriesv2.V2RegistriesConf{
				UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
				Registries: []sysregistriesv2.Registry{
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "registry-a.com",
						},
						Mirrors: []sysregistriesv2.Endpoint{

							{Location: "mirror-digest-1.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-digest-2.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-tag-1.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
							{Location: "mirror-tag-2.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "registry-b.com",
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "mirror-digest-1.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-digest-2.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-tag-1.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
							{Location: "mirror-tag-2.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
						},
					},
				},
			},
		},
		{
			name: "imageDigestMirrorSet + imageTagMirrorSet + imageContentSourcePolicy merging",
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "registry-a.com", Mirrors: []apicfgv1.ImageMirror{"mirror-digest-1.registry-a.com", "mirror-digest-2.registry-a.com"}},
							{Source: "registry-b.com", Mirrors: []apicfgv1.ImageMirror{"mirror-digest-1.registry-b.com", "mirror-digest-2.registry-b.com"}},
						},
					},
				},
			},
			itmsRules: []*apicfgv1.ImageTagMirrorSet{
				{
					Spec: apicfgv1.ImageTagMirrorSetSpec{
						ImageTagMirrors: []apicfgv1.ImageTagMirrors{
							{Source: "registry-a.com", Mirrors: []apicfgv1.ImageMirror{"mirror-tag-1.registry-a.com", "mirror-tag-2.registry-a.com"}},
							{Source: "registry-b.com", Mirrors: []apicfgv1.ImageMirror{"mirror-tag-1.registry-b.com", "mirror-tag-2.registry-b.com"}},
						},
					},
				},
			},
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						// icsp contains duplicated and newly added sources mirrors
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "registry-a.com", Mirrors: []string{"mirror-digest-1.registry-a.com", "mirror-digest-2.registry-a.com", "mirror-icsp-1.registry-a.com"}},
							{Source: "registry-b.com", Mirrors: []string{"mirror-digest-1.registry-b.com", "mirror-digest-2.registry-b.com", "mirror-icsp-1.registry-b.com"}},
							{Source: "registry-c.com", Mirrors: []string{"mirror-icsp-1.registry-c.com", "mirror-icsp-2.registry-c.com"}},
						},
					},
				},
			},

			want: sysregistriesv2.V2RegistriesConf{
				UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
				Registries: []sysregistriesv2.Registry{
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "registry-a.com",
						},
						Mirrors: []sysregistriesv2.Endpoint{

							{Location: "mirror-digest-1.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-digest-2.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-icsp-1.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-tag-1.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
							{Location: "mirror-tag-2.registry-a.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "registry-b.com",
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "mirror-digest-1.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-digest-2.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-icsp-1.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-tag-1.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
							{Location: "mirror-tag-2.registry-b.com", PullFromMirror: sysregistriesv2.MirrorByTagOnly},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "registry-c.com",
						},
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "mirror-icsp-1.registry-c.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
							{Location: "mirror-icsp-2.registry-c.com", PullFromMirror: sysregistriesv2.MirrorByDigestOnly},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updateRegistriesConfig(templateBytes, tt.insecure, tt.blocked, tt.icspRules, tt.idmsRules, tt.itmsRules)
			if err != nil {
				t.Errorf("updateRegistriesConfig() error = %v", err)
				return
			}
			gotConf := sysregistriesv2.V2RegistriesConf{}
			if _, err := toml.Decode(string(got), &gotConf); err != nil {
				t.Errorf("error unmarshalling result: %v", err)
				return
			}
			// This assumes a specific order of Registries entries, which does not actually matter; ideally, this would
			// sort the two arrays before comparing, but right now hard-coding the order works well enough.
			assert.Equal(t, tt.want, gotConf, "updateRegistriesConfig() Diff")
			// Ensure that the generated configuration is actually valid.
			registriesConf, err := os.CreateTemp("", "registries.conf")
			require.NoError(t, err)
			_, err = registriesConf.Write(got)
			require.NoError(t, err)
			defer os.Remove(registriesConf.Name())
			_, err = sysregistriesv2.GetRegistries(&types.SystemContext{
				SystemRegistriesConfPath: registriesConf.Name(),
			})
			assert.NoError(t, err)
		})
	}
}

func clusterImagePolicyTestCRs() map[string]apicfgv1.ClusterImagePolicy {
	testFulcioData, _ := b64.StdEncoding.DecodeString("dGVzdC1jYS1kYXRhLWRhdGE=")
	testRekorKeyData, _ := b64.StdEncoding.DecodeString("dGVzdC1yZWtvci1rZXktZGF0YQ==")
	testKeyData, _ := b64.StdEncoding.DecodeString("dGVzdC1rZXktZGF0YQ==")
	testCertsData, _ := b64.StdEncoding.DecodeString("LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=")

	testClusterImagePolicyCRs := map[string]apicfgv1.ClusterImagePolicy{
		"test-cr0": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cr0",
			},
			Spec: apicfgv1.ClusterImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"test0.com"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.FulcioCAWithRekorRootOfTrust,
						FulcioCAWithRekor: &apicfgv1.FulcioCAWithRekor{
							FulcioCAData: testFulcioData,
							RekorKeyData: testRekorKeyData,
							FulcioSubject: apicfgv1.PolicyFulcioSubject{
								OIDCIssuer:  "https://OIDC.example.com",
								SignedEmail: "test-user@example.com",
							},
						},
					},
					SignedIdentity: &apicfgv1.PolicyIdentity{
						MatchPolicy: apicfgv1.IdentityMatchPolicyRemapIdentity,
						PolicyMatchRemapIdentity: &apicfgv1.PolicyMatchRemapIdentity{
							Prefix:       "test-remap-prefix",
							SignedPrefix: "test-remap-signed-prefix",
						},
					},
				},
			},
		},
		"test-cr1": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cr1",
			},
			Spec: apicfgv1.ClusterImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"test0.com", "test1.com"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.PublicKeyRootOfTrust,
						PublicKey: &apicfgv1.PublicKey{
							KeyData:      testKeyData,
							RekorKeyData: testRekorKeyData,
						},
					},
					SignedIdentity: &apicfgv1.PolicyIdentity{
						MatchPolicy: apicfgv1.IdentityMatchPolicyRemapIdentity,
						PolicyMatchRemapIdentity: &apicfgv1.PolicyMatchRemapIdentity{
							Prefix:       "test-remap-prefix",
							SignedPrefix: "test-remap-signed-prefix",
						},
					},
				},
			},
		},
		"test-cr2": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cr2",
			},
			Spec: apicfgv1.ClusterImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "*.example.com", "policy.scope", "foo.example.com/ns/repo"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.PublicKeyRootOfTrust,
						PublicKey: &apicfgv1.PublicKey{
							KeyData:      testKeyData,
							RekorKeyData: testRekorKeyData,
						},
					},
				},
			},
		},
		"test-cr3": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cr3",
			},
			Spec: apicfgv1.ClusterImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"test3.com/ns/repo"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.PKIRootOfTrust,
						PKI: &apicfgv1.PKI{
							CertificateAuthorityRootsData:         testCertsData,
							CertificateAuthorityIntermediatesData: testCertsData,
							PKICertificateSubject: apicfgv1.PKICertificateSubject{
								Email:    "test-user@example.com",
								Hostname: "my-host.example.com",
							},
						},
					},
				},
			},
		},
	}
	return testClusterImagePolicyCRs
}

func imagePolicyTestCRs() map[string]apicfgv1.ImagePolicy {
	testKeyData, _ := b64.StdEncoding.DecodeString("dGVzdC1rZXktZGF0YQ==")
	testCertsData, _ := b64.StdEncoding.DecodeString("LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=")

	testImagePolicyCRs := map[string]apicfgv1.ImagePolicy{
		"test-cr0": {
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cr0",
				Namespace: "testnamespace",
			},
			Spec: apicfgv1.ImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"test0.com", "test2.com"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.PublicKeyRootOfTrust,
						PublicKey: &apicfgv1.PublicKey{
							KeyData: testKeyData,
						},
					},
				},
			},
		},
		"test-cr1": {
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cr1",
				Namespace: "testnamespace",
			},
			Spec: apicfgv1.ImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "*.example.com", "policy.scope", "foo.example.com/ns/repo"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.PublicKeyRootOfTrust,
						PublicKey: &apicfgv1.PublicKey{
							KeyData: testKeyData,
						},
					},
				},
			},
		},
		"test-cr2": {
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cr0",
				Namespace: "testnamespace",
			},
			Spec: apicfgv1.ImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"test2.com"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.PublicKeyRootOfTrust,
						PublicKey: &apicfgv1.PublicKey{
							KeyData: testKeyData,
						},
					},
				},
			},
		},
		"test-cr3": {
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cr3",
				Namespace: "test-1-namespace",
			},
			Spec: apicfgv1.ImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"test3.com"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.PublicKeyRootOfTrust,
						PublicKey: &apicfgv1.PublicKey{
							KeyData: testKeyData,
						},
					},
				},
			},
		},
		"test-cr4": {
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cr4",
				Namespace: "testnamespace",
			},
			Spec: apicfgv1.ImagePolicySpec{
				Scopes: []apicfgv1.ImageScope{"test4.com/ns-policy/repo"},
				Policy: apicfgv1.Policy{
					RootOfTrust: apicfgv1.PolicyRootOfTrust{
						PolicyType: apicfgv1.PKIRootOfTrust,
						PKI: &apicfgv1.PKI{
							CertificateAuthorityRootsData:         testCertsData,
							CertificateAuthorityIntermediatesData: testCertsData,
							PKICertificateSubject: apicfgv1.PKICertificateSubject{
								Email:    "test-user@example.com",
								Hostname: "my-host.example.com",
							},
						},
					},
				},
			},
		},
	}
	return testImagePolicyCRs
}

func TestUpdatePolicyJSON(t *testing.T) {
	testClusterImagePolicyCR := clusterImagePolicyTestCRs()["test-cr0"]
	expectSigRequirement, policyerr := policyItemFromSpec(testClusterImagePolicyCR.Spec.Policy)
	require.NoError(t, policyerr)

	templateConfig := signature.Policy{
		Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
		Transports: map[string]signature.PolicyTransportScopes{
			"docker-daemon": map[string]signature.PolicyRequirements{
				"": {signature.NewPRInsecureAcceptAnything()},
			},
		},
	}
	buf := bytes.Buffer{}
	err := json.NewEncoder(&buf).Encode(templateConfig)
	require.NoError(t, err)
	templateBytes := buf.Bytes()

	tests := []struct {
		name               string
		allowed, blocked   []string
		clusterimagepolicy *apicfgv1.ClusterImagePolicy
		errorExpected      bool
		want               signature.Policy
	}{
		{
			name:          "unchanged",
			want:          templateConfig,
			errorExpected: false,
		},
		{
			name:    "allowed",
			allowed: []string{"allow.io", "*.allowed-example.com"},
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRReject()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"allow.io":              {signature.NewPRInsecureAcceptAnything()},
						"*.allowed-example.com": {signature.NewPRInsecureAcceptAnything()},
					},
					"docker": map[string]signature.PolicyRequirements{
						"allow.io":              {signature.NewPRInsecureAcceptAnything()},
						"*.allowed-example.com": {signature.NewPRInsecureAcceptAnything()},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": {signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
			errorExpected: false,
		},
		{
			name:    "blocked",
			blocked: []string{"block.com", "*.blocked-example.com"},
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"block.com":             {signature.NewPRReject()},
						"*.blocked-example.com": {signature.NewPRReject()},
					},
					"docker": map[string]signature.PolicyRequirements{
						"block.com":             {signature.NewPRReject()},
						"*.blocked-example.com": {signature.NewPRReject()},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": {signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
			errorExpected: false,
		},
		{
			name:    "block payload image",
			blocked: []string{"block.com"},
			allowed: []string{"release-reg.io/image/release"},
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"block.com":                    {signature.NewPRReject()},
						"release-reg.io/image/release": {signature.NewPRInsecureAcceptAnything()},
					},
					"docker": map[string]signature.PolicyRequirements{
						"block.com":                    {signature.NewPRReject()},
						"release-reg.io/image/release": {signature.NewPRInsecureAcceptAnything()},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": {signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
			errorExpected: false,
		},
		{
			name:    "block registry of payload image",
			blocked: []string{"block.com", "release-reg.io"},
			allowed: []string{"release-reg.io/image/release"},
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"block.com":                    {signature.NewPRReject()},
						"release-reg.io":               {signature.NewPRReject()},
						"release-reg.io/image/release": {signature.NewPRInsecureAcceptAnything()},
					},
					"docker": map[string]signature.PolicyRequirements{
						"block.com":                    {signature.NewPRReject()},
						"release-reg.io":               {signature.NewPRReject()},
						"release-reg.io/image/release": {signature.NewPRInsecureAcceptAnything()},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": {signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
			errorExpected: false,
		},
		{
			name:          "blocked list and allowed list is set but allowed list doesn't contain the payload repo",
			blocked:       []string{"block.com", "another-block.io"},
			allowed:       []string{"allow.io"},
			errorExpected: true,
		},
		{
			name:    "allowed registries: atomic does not allow deep namespaces",
			allowed: []string{"allow.io", "allow.io/namespace1/namespace2", "allowed-example.com/namespace1/namespace2/namespace3"},
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRReject()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"allow.io":                       {signature.NewPRInsecureAcceptAnything()},
						"allow.io/namespace1/namespace2": {signature.NewPRInsecureAcceptAnything()},
					},
					"docker": map[string]signature.PolicyRequirements{
						"allow.io":                       {signature.NewPRInsecureAcceptAnything()},
						"allow.io/namespace1/namespace2": {signature.NewPRInsecureAcceptAnything()},
						"allowed-example.com/namespace1/namespace2/namespace3": {signature.NewPRInsecureAcceptAnything()},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": {signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
			errorExpected: false,
		},
		{
			name:    "blocked registries: atomic does not allow deep namespaces",
			blocked: []string{"block.com", "block.com/namespace1/namespace2", "blocked-example.com/namespace1/namespace2/namespace3"},
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"block.com":                       {signature.NewPRReject()},
						"block.com/namespace1/namespace2": {signature.NewPRReject()},
					},
					"docker": map[string]signature.PolicyRequirements{
						"block.com":                       {signature.NewPRReject()},
						"block.com/namespace1/namespace2": {signature.NewPRReject()},
						"blocked-example.com/namespace1/namespace2/namespace3": {signature.NewPRReject()},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": {signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
			errorExpected: false,
		},
		{
			name:               "add ClusterImagePolicy CR to policy json",
			allowed:            []string{"allow.io", "test0.com"},
			clusterimagepolicy: &testClusterImagePolicyCR,
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRReject()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"allow.io":  {signature.NewPRInsecureAcceptAnything()},
						"test0.com": {expectSigRequirement},
					},
					"docker": map[string]signature.PolicyRequirements{
						"allow.io":  {signature.NewPRInsecureAcceptAnything()},
						"test0.com": {expectSigRequirement},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": {signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
			errorExpected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			var (
				clusterImagePolicies map[string]signature.PolicyRequirements
				policyerr            error
			)

			if tt.clusterimagepolicy != nil {
				clusterImagePolicies, _, policyerr = getValidScopePolicies([]*apicfgv1.ClusterImagePolicy{tt.clusterimagepolicy}, nil, nil)
				require.NoError(t, policyerr)
			}
			got, err := updatePolicyJSON(templateBytes, tt.blocked, tt.allowed, "release-reg.io/image/release", clusterImagePolicies)
			if err == nil && tt.errorExpected {
				t.Errorf("updatePolicyJSON() error = %v", err)
				return
			}
			if err != nil {
				if tt.errorExpected {
					return
				}
				t.Errorf("updatePolicyJSON() error = %v", err)
				return
			}
			gotConf := signature.Policy{}
			if err := json.Unmarshal(got, &gotConf); err != nil {
				t.Errorf("error unmarshalling result: %v", err)
				return
			}
			if !reflect.DeepEqual(gotConf, tt.want) {
				t.Errorf("updatePolicyJSON() Diff:\n %s", diff.ObjectGoPrintDiff(tt.want, gotConf))
			}
			// Ensure that the generated configuration is actually valid.
			_, err = signature.NewPolicyFromBytes(got)
			require.NoError(t, err)
		})
	}
}

func TestValidateRegistriesConfScopes(t *testing.T) {
	tests := []struct {
		insecure    []string
		blocked     []string
		allowed     []string
		icspRules   []*apioperatorsv1alpha1.ImageContentSourcePolicy
		idmsRules   []*apicfgv1.ImageDigestMirrorSet
		expectedErr error
	}{
		{
			insecure:    []string{""},
			blocked:     []string{"*.block.com"},
			allowed:     []string{"*.allowed.com"},
			expectedErr: errors.New(`invalid entry for insecure registries ""`),
		},
		{
			insecure: []string{""},
			blocked:  []string{"*.block.com"},
			allowed:  []string{"*.allowed.com"},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.ImageMirror{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.ImageMirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.ImageMirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			expectedErr: errors.New(`invalid entry for insecure registries ""`),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{""},
			allowed:  []string{"*.allowed.com"},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.ImageMirror{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.ImageMirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.ImageMirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			expectedErr: errors.New(`invalid entry for blocked registries ""`),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{"*.block.com"},
			allowed:  []string{""},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.ImageMirror{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.ImageMirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.ImageMirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			expectedErr: errors.New(`invalid entry for allowed registries ""`),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{"*.block.com"},
			allowed:  []string{"*.allowed.com"},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "", Mirrors: []apicfgv1.ImageMirror{"blocked.com/ns-b1", "other.com/ns-o1"}},
						},
					},
				},
			},
			expectedErr: errors.New("invalid format for source \"\""),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{"*.block.com"},
			allowed:  []string{"*.allowed.com"},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.ImageMirror{"", "other.com/ns-o1"}},
						},
					},
				},
			},
			expectedErr: errors.New("invalid format for mirror \"\""),
		},
		{
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "*.insecure.com/ns-i1", Mirrors: []string{"", "other.com/ns-o1"}},
						},
					},
				},
			},
			expectedErr: errors.New("invalid format for source \"*.insecure.com/ns-i1\""),
		},
		{
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "*.insecure.com", Mirrors: []string{"*.other.com/ns-o1"}},
						},
					},
				},
			},
			expectedErr: errors.New("invalid format for mirror \"*.other.com/ns-o1\""),
		},
		{
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1 ", Mirrors: []string{"", "other.com/ns-o1"}},
						},
					},
				},
			},
			expectedErr: errors.New("invalid format for source \"insecure.com/ns-i1 \""),
		},
		{
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []string{"other.com/ns-o1  "}},
						},
					},
				},
			},
			expectedErr: errors.New("invalid format for mirror \"other.com/ns-o1  \""),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{"*.block.com"},
			allowed:  []string{"*.allowed.com"},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "*.insecure.com", Mirrors: []apicfgv1.ImageMirror{"other.com/ns-o1"}},
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.ImageMirror{"other.com/ns-o1"}},
						},
					},
				},
			},
			expectedErr: nil,
		},
		{
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []string{"other.com/ns-o1  "}},
						},
					},
				},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.ImageMirror{"blocked.com/ns-b1", "other.com/ns-o1"}, MirrorSourcePolicy: apicfgv1.NeverContactSource},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.ImageMirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.ImageMirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			expectedErr: errors.New(`conflicting mirrorSourcePolicy is set for the same source "insecure.com/ns-i1" in imagedigestmirrorsets, imagetagmirrorsets, or imagecontentsourcepolicies`),
		},
	}

	for _, tc := range tests {
		res := validateRegistriesConfScopes(tc.insecure, tc.blocked, tc.allowed, tc.icspRules, tc.idmsRules, nil)
		require.Equal(t, tc.expectedErr, res)
	}
}

func TestGetValidBlockAndAllowedRegistries(t *testing.T) {
	tests := []struct {
		name, releaseImg                                                  string
		imgSpec                                                           *apicfgv1.ImageSpec
		idmsRules                                                         []*apicfgv1.ImageDigestMirrorSet
		expectedRegistriesBlocked, expectedPolicyBlocked, expectedAllowed []string
		expectedErr                                                       bool
	}{
		{
			name:       "regular blocked list with no mirror rules configured",
			releaseImg: "payload-reg.io/release-image@sha256:4207ba569ff014931f1b5d125fe3751936a768e119546683c899eb09f3cdceb0",
			imgSpec: &apicfgv1.ImageSpec{
				RegistrySources: apicfgv1.RegistrySources{
					BlockedRegistries: []string{"block.io", "block-2.io"},
				},
			},
			expectedRegistriesBlocked: []string{"block.io", "block-2.io"},
			expectedPolicyBlocked:     []string{"block.io", "block-2.io"},
			expectedErr:               false,
		},
		{
			name:       "regular blocked list with unrelated mirror rules configured",
			releaseImg: "payload-reg.io/release-image@sha256:4207ba569ff014931f1b5d125fe3751936a768e119546683c899eb09f3cdceb0",
			imgSpec: &apicfgv1.ImageSpec{
				RegistrySources: apicfgv1.RegistrySources{
					BlockedRegistries: []string{"block.io", "block-2.io"},
				},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "src.io/payload", Mirrors: []apicfgv1.ImageMirror{"mirror-1.io/payload", "mirror-2.io/payload"}},
						},
					},
				},
			},
			expectedRegistriesBlocked: []string{"block.io", "block-2.io"},
			expectedPolicyBlocked:     []string{"block.io", "block-2.io"},
			expectedErr:               false,
		},
		{
			name:       "payload reg does not have mirror configured and is in blocked list",
			releaseImg: "payload-reg.io/release-image@sha256:4207ba569ff014931f1b5d125fe3751936a768e119546683c899eb09f3cdceb0",
			imgSpec: &apicfgv1.ImageSpec{
				RegistrySources: apicfgv1.RegistrySources{
					BlockedRegistries: []string{"block.io", "payload-reg.io", "block-2.io"},
				},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "src.io/payload", Mirrors: []apicfgv1.ImageMirror{"mirror-1.io/payload", "mirror-2.io/payload"}},
						},
					},
				},
			},
			expectedRegistriesBlocked: []string{"block.io", "block-2.io"},
			expectedPolicyBlocked:     []string{"block.io", "block-2.io"},
			expectedErr:               true,
		},
		{
			name:       "payload reg has mirror configured and is in blocked list",
			releaseImg: "payload-reg.io/release-image@sha256:4207ba569ff014931f1b5d125fe3751936a768e119546683c899eb09f3cdceb0",
			imgSpec: &apicfgv1.ImageSpec{
				RegistrySources: apicfgv1.RegistrySources{
					BlockedRegistries: []string{"block.io", "payload-reg.io", "block-2.io"},
				},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "payload-reg.io/release-image", Mirrors: []apicfgv1.ImageMirror{"mirror-1.io/payload", "mirror-2.io/payload"}},
						},
					},
				},
			},
			expectedRegistriesBlocked: []string{"block.io", "payload-reg.io", "block-2.io"},
			expectedPolicyBlocked:     []string{"block.io", "payload-reg.io", "block-2.io"},
			expectedAllowed:           []string{"payload-reg.io/release-image"},
			expectedErr:               false,
		},
		{
			name:       "payload is blocked; all of mirror is not blocked, but the mirror of the payload is blocked",
			releaseImg: "quay.io/openshift-release-dev@sha256:4207ba569ff014931f1b5d125fe3751936a768e119546683c899eb09f3cdceb0",
			imgSpec: &apicfgv1.ImageSpec{
				RegistrySources: apicfgv1.RegistrySources{
					BlockedRegistries: []string{"quay.io", "block.io/openshift-release-dev"},
				},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "quay.io", Mirrors: []apicfgv1.ImageMirror{"block.io"}}, // quay.io/openshift-release-dev -> block.io/openshift-release-dev
						},
					},
				},
			},
			expectedRegistriesBlocked: []string{"block.io/openshift-release-dev"},
			expectedPolicyBlocked:     []string{"block.io/openshift-release-dev"},
			expectedErr:               true,
		},
		{
			name:       "payload is blocked; parent of the mirror of the payload is blocked",
			releaseImg: "quay.io/openshift-release-dev@sha256:4207ba569ff014931f1b5d125fe3751936a768e119546683c899eb09f3cdceb0",
			imgSpec: &apicfgv1.ImageSpec{
				RegistrySources: apicfgv1.RegistrySources{
					BlockedRegistries: []string{"quay.io", "block.io"},
				},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "quay.io/openshift-release-dev", Mirrors: []apicfgv1.ImageMirror{"block.io/openshift-release-dev"}}, // quay.io/openshift-release-dev -> block.io/openshift-release-dev
						},
					},
				},
			},
			expectedRegistriesBlocked: []string{"block.io"},
			expectedPolicyBlocked:     []string{"block.io"},
			expectedErr:               true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRegistries, gotPolicy, gotAllowed, err := getValidBlockedAndAllowedRegistries(tt.releaseImg, tt.imgSpec, nil, tt.idmsRules)
			if (err != nil && !tt.expectedErr) || (err == nil && tt.expectedErr) {
				t.Errorf("getValidBlockedRegistries() error = %v", err)
				return
			}
			require.Equal(t, tt.expectedRegistriesBlocked, gotRegistries)
			require.Equal(t, tt.expectedPolicyBlocked, gotPolicy)
			require.Equal(t, tt.expectedAllowed, gotAllowed)
		})
	}
}

func TestCreateCRIODropinFiles(t *testing.T) {
	zeroLogSizeMax := resource.MustParse("0k")
	validLogSizeMax := resource.MustParse("10G")

	// Test zero value of logSizeMax will not be applied
	zeroValueTests := []struct {
		name     string
		cfg      *mcfgv1.ContainerRuntimeConfiguration
		filepath string
	}{
		{
			name: "01-ctrcfg-logSizeMax will not be created if logSizeMax is zero",
			cfg: &mcfgv1.ContainerRuntimeConfiguration{
				LogSizeMax: &zeroLogSizeMax,
			},
			filepath: crioDropInFilePathLogSizeMax,
		},
	}

	// Test valid value of logSizeMax will be applied to the drop-in file
	validValueTests := []struct {
		name     string
		cfg      *mcfgv1.ContainerRuntimeConfiguration
		filepath string
		want     []byte
	}{
		{
			name: "01-ctrcfg-logSizeMax created for valid logSizeMax",
			cfg: &mcfgv1.ContainerRuntimeConfiguration{
				LogSizeMax: &validLogSizeMax,
			},
			filepath: crioDropInFilePathLogSizeMax,
			want: []byte(`[crio]
  [crio.runtime]
    log_size_max = 10000000000
`),
		},
	}

	for _, test := range zeroValueTests {
		ctrcfg := newContainerRuntimeConfig(test.name, test.cfg, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "", ""))
		files := createCRIODropinFiles(ctrcfg)
		for _, file := range files {
			if file.filePath == test.filepath {
				t.Errorf("%s: failed. should not have created dropin file", test.name)
			}
		}
	}

	for _, test := range validValueTests {
		ctrcfg := newContainerRuntimeConfig(test.name, test.cfg, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "", ""))
		files := createCRIODropinFiles(ctrcfg)
		for _, file := range files {
			if file.filePath == test.filepath {
				require.Equal(t, test.want, file.data, "createCRIODropinFiles() Diff, want %v, got %v", test.want, string(file.data))
			}
		}
	}
}

func TestUpdateStorageConfig(t *testing.T) {
	templateStorageConfig := tomlConfigStorage{}
	buf := bytes.Buffer{}
	err := toml.NewEncoder(&buf).Encode(templateStorageConfig)
	require.NoError(t, err)
	templateBytes := buf.Bytes()

	zeroOverLayerSize := resource.MustParse("0k")
	validOverLaySize := resource.MustParse("10G")

	tests := []struct {
		name string
		cfg  *mcfgv1.ContainerRuntimeConfiguration
		want tomlConfigStorage
	}{
		{
			name: "not apply zero value of overlaySize",
			cfg: &mcfgv1.ContainerRuntimeConfiguration{
				OverlaySize: &zeroOverLayerSize,
			},
			want: tomlConfigStorage{},
		},
		{
			name: "apply valid overlaySize",
			cfg: &mcfgv1.ContainerRuntimeConfiguration{
				OverlaySize: &validOverLaySize,
			},
			want: tomlConfigStorage{
				Storage: struct {
					Driver    string                                "toml:\"driver\""
					RunRoot   string                                "toml:\"runroot\""
					GraphRoot string                                "toml:\"graphroot\""
					Options   struct{ storageconfig.OptionsConfig } "toml:\"options\""
				}{
					Options: struct{ storageconfig.OptionsConfig }{
						storageconfig.OptionsConfig{
							Size: "10G",
						},
					},
				},
			},
		},
	}

	for _, test := range tests {
		got, err := updateStorageConfig(templateBytes, test.cfg)
		require.NoError(t, err)
		gotConf := tomlConfigStorage{}
		if _, err := toml.Decode(string(got), &gotConf); err != nil {
			t.Errorf("error unmarshalling result: %v", err)
			return
		}
		if !reflect.DeepEqual(gotConf, test.want) {
			t.Errorf("%s: failed. got %v, want %v", test.name, got, test.want)
		}
	}
}

func TestGetValidScopePolicies(t *testing.T) {
	type testcase struct {
		name                   string
		clusterImagePolicyCRs  []*apicfgv1.ClusterImagePolicy
		imagePolicyCRs         []*apicfgv1.ImagePolicy
		expectedScopePolicies  map[string]signature.PolicyRequirements
		expectedScopeNamespace map[string]map[string]signature.PolicyRequirements
		errorExpected          bool
	}

	clusterTestCR0 := clusterImagePolicyTestCRs()["test-cr0"]
	clusterTestCR1 := clusterImagePolicyTestCRs()["test-cr1"]
	clusterPolicyreq0, err := policyItemFromSpec(clusterTestCR0.Spec.Policy)
	require.NoError(t, err)
	clusterPolicyreq1, err := policyItemFromSpec(clusterTestCR1.Spec.Policy)
	require.NoError(t, err)

	imgPolicyTestCR0 := imagePolicyTestCRs()["test-cr0"]
	imagePolicyreq0, err := policyItemFromSpec(imgPolicyTestCR0.Spec.Policy)
	require.NoError(t, err)

	tests := []testcase{
		{
			name:                  "cluster CRs contain the same scope",
			clusterImagePolicyCRs: []*apicfgv1.ClusterImagePolicy{&clusterTestCR0, &clusterTestCR1},
			expectedScopePolicies: map[string]signature.PolicyRequirements{
				"test0.com": {clusterPolicyreq0, clusterPolicyreq1},
				"test1.com": {clusterPolicyreq1},
			},
			expectedScopeNamespace: map[string]map[string]signature.PolicyRequirements{},
			errorExpected:          false,
		},
		{
			name:                  "cluster and namespace CRs contains the same scope",
			clusterImagePolicyCRs: []*apicfgv1.ClusterImagePolicy{&clusterTestCR0, &clusterTestCR1},
			imagePolicyCRs:        []*apicfgv1.ImagePolicy{&imgPolicyTestCR0},
			expectedScopePolicies: map[string]signature.PolicyRequirements{
				"test0.com": {clusterPolicyreq0, clusterPolicyreq1},
				"test1.com": {clusterPolicyreq1},
			},
			expectedScopeNamespace: map[string]map[string]signature.PolicyRequirements{ // skip namespaced imagepolicy for test0.com
				"test2.com": {
					imgPolicyTestCR0.ObjectMeta.Namespace: {imagePolicyreq0},
				},
			},
			errorExpected: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotScopePolicies, gotScopeNamespacePolicies, err := getValidScopePolicies(test.clusterImagePolicyCRs, test.imagePolicyCRs, nil)
			require.Equal(t, test.errorExpected, err != nil)
			if !test.errorExpected {
				require.Equal(t, test.expectedScopePolicies, gotScopePolicies)
				require.Equal(t, test.expectedScopeNamespace, gotScopeNamespacePolicies)
			}
		})
	}
}

func TestGenerateSigstoreRegistriesConfig(t *testing.T) {

	// testcase CIP/IP scopes:
	// "a.com/a1/a2",
	// "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000",
	// "*.example.com",
	// "foo.example.com/ns/repo",
	// "policy.scope"
	testClusterImagePolicyCR2 := clusterImagePolicyTestCRs()["test-cr2"]
	testImagePolicyCR1 := imagePolicyTestCRs()["test-cr1"]

	clusterScopePolicies, _, err := getValidScopePolicies([]*apicfgv1.ClusterImagePolicy{&testClusterImagePolicyCR2}, nil, nil)
	require.NoError(t, err)

	_, scopeNamespacePolicies, err := getValidScopePolicies(nil, []*apicfgv1.ImagePolicy{&testImagePolicyCR1}, nil)
	require.NoError(t, err)

	testImagePolicyCR0 := imagePolicyTestCRs()["test-cr0"]
	niMirrorClusterScopePolicies, noMirrorScopeNamespacePolicies, err := getValidScopePolicies([]*apicfgv1.ClusterImagePolicy{&testClusterImagePolicyCR2}, []*apicfgv1.ImagePolicy{&testImagePolicyCR0}, nil)
	require.NoError(t, err)

	type testcase struct {
		name                                   string
		clusterScopePolicies                   map[string]signature.PolicyRequirements
		scopeNamespacePolicies                 map[string]map[string]signature.PolicyRequirements
		icspRules                              []*apioperatorsv1alpha1.ImageContentSourcePolicy
		idmsRules                              []*apicfgv1.ImageDigestMirrorSet
		itmsRules                              []*apicfgv1.ImageTagMirrorSet
		expectedSigstoreRegistriesConfigScopes []string
	}

	testcases := []testcase{
		{
			name:                                   "cip objects exist, icsp/idms/itms are empty",
			clusterScopePolicies:                   clusterScopePolicies,
			expectedSigstoreRegistriesConfigScopes: []string{"*.example.com", "a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "foo.example.com/ns/repo", "policy.scope"},
		},
		{
			name:                 "cip has no revelant registries mirrors",
			clusterScopePolicies: clusterScopePolicies,
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "x.com/x1/x2", Mirrors: []string{"x-x1-x2.mirror/x1/x2"}},
						},
					},
				},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "y.com/y1/y2", Mirrors: []apicfgv1.ImageMirror{"y-y1-y2.mirror/y1/y2"}},
						},
					},
				},
			},
			itmsRules: []*apicfgv1.ImageTagMirrorSet{
				{
					Spec: apicfgv1.ImageTagMirrorSetSpec{
						ImageTagMirrors: []apicfgv1.ImageTagMirrors{
							{Source: "z.com/z1/z2", Mirrors: []apicfgv1.ImageMirror{"z-z1-z2.mirror/z1/z2"}},
						},
					},
				},
			},
			expectedSigstoreRegistriesConfigScopes: []string{"*.example.com", "a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "foo.example.com/ns/repo", "policy.scope"},
		},
		{
			name:                 "cip scope a.com/a1/a2 is super scope of registries sources",
			clusterScopePolicies: clusterScopePolicies,
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "a.com/a1/a2", Mirrors: []apicfgv1.ImageMirror{"a-a1-a2.mirror/a1/a2"}},
							{Source: "a.com/a1/a2/a3", Mirrors: []apicfgv1.ImageMirror{"a-a1-a2-a3.mirror/m1/m2/m3"}},
							{Source: "a.com/a1/a2/a3-1", Mirrors: []apicfgv1.ImageMirror{"a-a1-a2-a3-1.mirror/m1/m2/m3"}},
							{Source: "x.com/x1/x2", Mirrors: []apicfgv1.ImageMirror{"x-x1-x2.mirror/m1/m2"}},
						},
					},
				},
			},
			expectedSigstoreRegistriesConfigScopes: []string{"*.example.com", "a-a1-a2-a3-1.mirror/m1/m2/m3", "a-a1-a2-a3.mirror/m1/m2/m3", "a-a1-a2.mirror/a1/a2", "a.com/a1/a2", "a.com/a1/a2/a3", "a.com/a1/a2/a3-1", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "foo.example.com/ns/repo", "policy.scope"},
		},
		{
			name:                 "cip scope a.com/a1/a2 nested in registries sources",
			clusterScopePolicies: clusterScopePolicies,
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "a.com", Mirrors: []apicfgv1.ImageMirror{"a.mirror"}},
							{Source: "a.com/a1", Mirrors: []apicfgv1.ImageMirror{"a-a1.mirror/a1"}},
							{Source: "x.com/x1/x2", Mirrors: []apicfgv1.ImageMirror{"x-x1-x2.mirror/x1/x2"}},
						},
					},
				},
			},
			expectedSigstoreRegistriesConfigScopes: []string{"*.example.com", "a-a1.mirror/a1/a2", "a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "policy.scope", "foo.example.com/ns/repo"},
		},
		{
			name:                 "cip wildcard scope *.example.com is the mirror source",
			clusterScopePolicies: clusterScopePolicies,
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "*.example.com", Mirrors: []apicfgv1.ImageMirror{"our.mirror/example"}},
						},
					},
				},
			},
			expectedSigstoreRegistriesConfigScopes: []string{"*.example.com", "a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "policy.scope", "foo.example.com/ns/repo", "our.mirror/example/ns/repo", "our.mirror/example"},
		},
		{
			name:                 "cip scope *.example.com and mirror source have wildcard matching",
			clusterScopePolicies: clusterScopePolicies,
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "a.example.com", Mirrors: []apicfgv1.ImageMirror{"a-example.mirror"}},
							{Source: "*.x.example.com", Mirrors: []apicfgv1.ImageMirror{"matched.example.mirror", "star-x.example.mirror"}},
							{Source: "*.scope", Mirrors: []apicfgv1.ImageMirror{"start-scope.mirror"}},
							{Source: "x/x1/x2", Mirrors: []apicfgv1.ImageMirror{"x-x1-x2.mirror/x1/x2"}},
						},
					},
				},
			},
			expectedSigstoreRegistriesConfigScopes: []string{"*.example.com", "a-example.mirror", "a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "a.example.com", "star-x.example.mirror", "matched.example.mirror", "policy.scope", "start-scope.mirror", "foo.example.com/ns/repo"},
		},
		{
			name:                                   "imagepolicy objects exist, icsp/idms/itms are empty",
			scopeNamespacePolicies:                 scopeNamespacePolicies,
			expectedSigstoreRegistriesConfigScopes: []string{"*.example.com", "a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "foo.example.com/ns/repo", "policy.scope"},
		},
		{
			name:                                   "cip and imagepolicy objects exist, icsp/idms/itms are empty",
			clusterScopePolicies:                   niMirrorClusterScopePolicies,
			scopeNamespacePolicies:                 noMirrorScopeNamespacePolicies,
			expectedSigstoreRegistriesConfigScopes: []string{"*.example.com", "a.com/a1/a2", "a.com/a1/a2@sha256:0000000000000000000000000000000000000000000000000000000000000000", "foo.example.com/ns/repo", "policy.scope", "test0.com", "test2.com"},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			registriesTOML, err := updateRegistriesConfig(templateRegistriesConfig, nil, nil, tc.icspRules, tc.idmsRules, tc.itmsRules)
			require.NoError(t, err)
			got, err := generateSigstoreRegistriesdConfig(tc.clusterScopePolicies, tc.scopeNamespacePolicies, registriesTOML)
			require.NoError(t, err)

			gotRegistriesCfg := &registriesConfig{}
			err = yaml.Unmarshal(got, &gotRegistriesCfg)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.expectedSigstoreRegistriesConfigScopes, maps.Keys(gotRegistriesCfg.Docker))
		})
	}
}

func TestGeneratePolicyJSON(t *testing.T) {

	testImagePolicyCR0 := clusterImagePolicyTestCRs()["test-cr0"]
	testImagePolicyCR3 := clusterImagePolicyTestCRs()["test-cr3"]

	expectClusterPolicy := []byte(`
		{
			"default": [
			  {
				"type": "insecureAcceptAnything"
			  }
			],
			"transports": {
				"atomic": {
					"test0.com": [
					  {
						"type": "sigstoreSigned",
						"fulcio": {
						  "caData": "dGVzdC1jYS1kYXRhLWRhdGE=",
						  "oidcIssuer": "https://OIDC.example.com",
						  "subjectEmail": "test-user@example.com"
						},
						"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
						"signedIdentity": {
						  "type": "remapIdentity",
						  "prefix": "test-remap-prefix",
						  "signedPrefix": "test-remap-signed-prefix"
						}
					  }
					],
					"test3.com/ns/repo": [
                      {
					    "type": "sigstoreSigned",
                        "pki": {
						  "caRootsData": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=",
						  "caIntermediatesData": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=",
						  "subjectEmail": "test-user@example.com",
						  "subjectHostname": "my-host.example.com"
						},
						"signedIdentity": {
						  "type": "matchRepoDigestOrExact"
						}
					  }
					]
				  },	
			  "docker": {
				"test0.com": [
				  {
					"type": "sigstoreSigned",
					"fulcio": {
					  "caData": "dGVzdC1jYS1kYXRhLWRhdGE=",
					  "oidcIssuer": "https://OIDC.example.com",
					  "subjectEmail": "test-user@example.com"
					},
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test3.com/ns/repo": [
                  {
					"type": "sigstoreSigned",
					"pki": {
					  "caRootsData": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=",
					  "caIntermediatesData": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=",
					  "subjectEmail": "test-user@example.com",
					  "subjectHostname": "my-host.example.com"
					},
					"signedIdentity": {
					  "type": "matchRepoDigestOrExact"
					}
				   }
				]				
			  },
			  "docker-daemon": {
				"": [
				  {
					"type": "insecureAcceptAnything"
				  }
				]
			  }
			}
		  }
	`)

	clusterScopePolicies, _, err := getValidScopePolicies([]*apicfgv1.ClusterImagePolicy{&testImagePolicyCR0, &testImagePolicyCR3}, nil, nil)
	require.NoError(t, err)

	templateConfig := signature.Policy{
		Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
		Transports: map[string]signature.PolicyTransportScopes{
			"docker-daemon": map[string]signature.PolicyRequirements{
				"": {signature.NewPRInsecureAcceptAnything()},
			},
		},
	}
	buf := bytes.Buffer{}
	err = json.NewEncoder(&buf).Encode(templateConfig)
	require.NoError(t, err)
	templateBytes := buf.Bytes()

	baseData, err := updatePolicyJSON(templateBytes, []string{}, []string{}, "release-reg.io/image/release", clusterScopePolicies)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile("expected.json", expectClusterPolicy, 0o755))
	require.NoError(t, os.WriteFile("actual.json", baseData, 0o755))
	require.JSONEq(t, string(expectClusterPolicy), string(baseData))

}

func TestValidateClusterImagePolicyWithAllowedBlockedRegistries(t *testing.T) {
	tests := []struct {
		name                   string
		allowed                []string
		blocked                []string
		clusterScopePolicies   map[string]signature.PolicyRequirements
		scopeNamespacePolicies map[string]map[string]signature.PolicyRequirements
		errorExpected          bool
	}{
		{
			name: "allowed and blocked registries are not set",
			clusterScopePolicies: map[string]signature.PolicyRequirements{
				"example.com":         {},
				"test.registries.com": {},
			},
			scopeNamespacePolicies: map[string]map[string]signature.PolicyRequirements{
				"exaple.app.com":         {},
				"testapp.registries.com": {},
			},
			errorExpected: false,
		},
		{
			name: "success test allowed registries set",
			allowed: []string{
				"example.com",
				"allowed.io/namespace1",
			},
			clusterScopePolicies: map[string]signature.PolicyRequirements{
				"allowed.io/namespace1/policysigned": {},
				"example.com":                        {},
			},
			scopeNamespacePolicies: map[string]map[string]signature.PolicyRequirements{
				"allowed.io/namespace1/app/policysigned": {},
			},
			errorExpected: false,
		},
		{
			name: "failure test allowed registries set",
			allowed: []string{
				"allowed.io/namespace1/namespace2",
			},
			clusterScopePolicies: map[string]signature.PolicyRequirements{
				"allowed.io/policysigned": {},
			},
			errorExpected: true,
		},
		{
			name: "success test blocked registries set",
			blocked: []string{
				"blocked.io/namespace1/namespace2",
				"blocked.example.com/namespace1/namespace2",
			},
			clusterScopePolicies: map[string]signature.PolicyRequirements{
				"allowed.io/policysigned": {},
				"blocked.io":              {},
			},
			scopeNamespacePolicies: map[string]map[string]signature.PolicyRequirements{
				"allowed.io/namespace1/app/policysigned": {},
				"blocked.example.com/app/policysigned":   {},
			},
			errorExpected: false,
		},
		{
			name: "failure test blocked registries set",
			blocked: []string{
				"blocked.io",
				"blocker-reg.com/namespace1",
			},
			clusterScopePolicies: map[string]signature.PolicyRequirements{
				"blocker-reg.com/namespace1/policysigned": {},
				"blocked.io": {},
			},
			errorExpected: true,
		},
		{
			name: "imagepolicy failure test allowed registries set",
			allowed: []string{
				"allowed.io/namespace1/namespace2",
			},
			scopeNamespacePolicies: map[string]map[string]signature.PolicyRequirements{
				"allowed.io/app/policysigned": {},
			},
			errorExpected: true,
		},
		{
			name: "imagepolicy failure test blocked registries set",
			blocked: []string{
				"blocked.io",
				"blocker-reg.com/namespace1",
			},
			scopeNamespacePolicies: map[string]map[string]signature.PolicyRequirements{
				"blocker-reg.com/namespace1/policysigned": {},
				"blocked.io": {},
			},
			errorExpected: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImagePolicyWithAllowedBlockedRegistries(tt.clusterScopePolicies, tt.scopeNamespacePolicies, tt.allowed, tt.blocked)
			if err == nil && tt.errorExpected {
				t.Errorf("validateImagePolicyWithAllowedBlockedRegistries() error = %v", err)
				return
			}
			if err != nil {
				if tt.errorExpected {
					return
				}
				t.Errorf("validateImagePolicyWithAllowedBlockedRegistries() error = %v", err)
				return
			}
		})
	}
}

func TestUpdateNamespacedPolicyJSONs(t *testing.T) {

	templatePolicy := signature.Policy{
		Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
		Transports: map[string]signature.PolicyTransportScopes{
			"docker-daemon": map[string]signature.PolicyRequirements{
				"": {signature.NewPRInsecureAcceptAnything()},
			},
		},
	}
	buf := bytes.Buffer{}
	err := json.NewEncoder(&buf).Encode(templatePolicy)
	require.NoError(t, err)
	templatePolicyBytes := buf.Bytes()

	// Test empty namespacePolicies does not generate any namespaced policy
	namespacedPoliciesJsons, err := updateNamespacedPolicyJSONs(templatePolicyBytes, nil, nil, make(map[string]map[string]signature.PolicyRequirements))
	require.NoError(t, err)
	require.Equal(t, 0, len(namespacedPoliciesJsons))

	// Test namespaced policy inherits cluster override policy
	testImagePolicyCR0 := clusterImagePolicyTestCRs()["test-cr0"]
	testImagePolicyCR1 := clusterImagePolicyTestCRs()["test-cr1"]
	testImagePolicyCR2 := imagePolicyTestCRs()["test-cr0"]
	testImagePolicyCR3 := imagePolicyTestCRs()["test-cr3"]
	testImagePolicyCR4 := imagePolicyTestCRs()["test-cr4"]

	expectClusterPolicy := []byte(`
			{
				"default": [
				  {
					"type": "insecureAcceptAnything"
				  }
				],
				"transports": {
				  "atomic": {
					"test0.com": [
					  {
						"type": "sigstoreSigned",
						"fulcio": {
						  "caData": "dGVzdC1jYS1kYXRhLWRhdGE=",
						  "oidcIssuer": "https://OIDC.example.com",
						  "subjectEmail": "test-user@example.com"
						},
						"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
						"signedIdentity": {
						  "type": "remapIdentity",
						  "prefix": "test-remap-prefix",
						  "signedPrefix": "test-remap-signed-prefix"
						}
					  },
					  {
						"type": "sigstoreSigned",
						"keyData": "dGVzdC1rZXktZGF0YQ==",
						"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
						"signedIdentity": {
						  "type": "remapIdentity",
						  "prefix": "test-remap-prefix",
						  "signedPrefix": "test-remap-signed-prefix"
						}
					  }
					],
					"test1.com": [
					  {
						"type": "sigstoreSigned",
						"keyData": "dGVzdC1rZXktZGF0YQ==",
						"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
						"signedIdentity": {
						  "type": "remapIdentity",
						  "prefix": "test-remap-prefix",
						  "signedPrefix": "test-remap-signed-prefix"
						}
					  }
					]
				  },
				  "docker": {
					"test0.com": [
					  {
						"type": "sigstoreSigned",
						"fulcio": {
						  "caData": "dGVzdC1jYS1kYXRhLWRhdGE=",
						  "oidcIssuer": "https://OIDC.example.com",
						  "subjectEmail": "test-user@example.com"
						},
						"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
						"signedIdentity": {
						  "type": "remapIdentity",
						  "prefix": "test-remap-prefix",
						  "signedPrefix": "test-remap-signed-prefix"
						}
					  },
					  {
						"type": "sigstoreSigned",
						"keyData": "dGVzdC1rZXktZGF0YQ==",
						"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
						"signedIdentity": {
						  "type": "remapIdentity",
						  "prefix": "test-remap-prefix",
						  "signedPrefix": "test-remap-signed-prefix"
						}
					  }
					],
					"test1.com": [
					  {
						"type": "sigstoreSigned",
						"keyData": "dGVzdC1rZXktZGF0YQ==",
						"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
						"signedIdentity": {
						  "type": "remapIdentity",
						  "prefix": "test-remap-prefix",
						  "signedPrefix": "test-remap-signed-prefix"
						}
					  }
					]
				  },
				  "docker-daemon": {
					"": [
					  {
						"type": "insecureAcceptAnything"
					  }
					]
				  }
				}
			  }
		`)
	expectTestnamespacedPolicy := []byte(`
		{
			"default": [
			  {
				"type": "insecureAcceptAnything"
			  }
			],
			"transports": {
			  "atomic": {
				"test0.com": [
				  {
					"type": "sigstoreSigned",
					"fulcio": {
					  "caData": "dGVzdC1jYS1kYXRhLWRhdGE=",
					  "oidcIssuer": "https://OIDC.example.com",
					  "subjectEmail": "test-user@example.com"
					},
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  },
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test1.com": [
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test2.com": [
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "matchRepoDigestOrExact"
					}
				  }
				],
                "test4.com/ns-policy/repo": [
                  {
					"type": "sigstoreSigned",
					"pki": {
					  "caRootsData": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=",
					  "caIntermediatesData": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=",
					  "subjectEmail": "test-user@example.com",
					  "subjectHostname": "my-host.example.com"
					},
					"signedIdentity": {
					  "type": "matchRepoDigestOrExact"
					}
				   }
				]
			  },
			  "docker": {
				"test0.com": [
				  {
					"type": "sigstoreSigned",
					"fulcio": {
					  "caData": "dGVzdC1jYS1kYXRhLWRhdGE=",
					  "oidcIssuer": "https://OIDC.example.com",
					  "subjectEmail": "test-user@example.com"
					},
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  },
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test1.com": [
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test2.com": [
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "matchRepoDigestOrExact"
					}
				  }
				],
                "test4.com/ns-policy/repo": [
                  {
					"type": "sigstoreSigned",
					"pki": {
					  "caRootsData": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=",
					  "caIntermediatesData": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJBVEE9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0=",
					  "subjectEmail": "test-user@example.com",
					  "subjectHostname": "my-host.example.com"
					},
					"signedIdentity": {
					  "type": "matchRepoDigestOrExact"
					}
				   }
				]
			  },
			  "docker-daemon": {
				"": [
				  {
					"type": "insecureAcceptAnything"
				  }
				]
			  }
			}
		  }
	`)

	expectTest1namespacedPolicy := []byte(`
		{
			"default": [
			  {
				"type": "insecureAcceptAnything"
			  }
			],
			"transports": {
			  "atomic": {
				"test0.com": [
				  {
					"type": "sigstoreSigned",
					"fulcio": {
					  "caData": "dGVzdC1jYS1kYXRhLWRhdGE=",
					  "oidcIssuer": "https://OIDC.example.com",
					  "subjectEmail": "test-user@example.com"
					},
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  },
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test1.com": [
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test3.com": [
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "matchRepoDigestOrExact"
					}
				  }
				]
			  },
			  "docker": {
				"test0.com": [
				  {
					"type": "sigstoreSigned",
					"fulcio": {
					  "caData": "dGVzdC1jYS1kYXRhLWRhdGE=",
					  "oidcIssuer": "https://OIDC.example.com",
					  "subjectEmail": "test-user@example.com"
					},
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  },
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test1.com": [
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"rekorPublicKeyData": "dGVzdC1yZWtvci1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "remapIdentity",
					  "prefix": "test-remap-prefix",
					  "signedPrefix": "test-remap-signed-prefix"
					}
				  }
				],
				"test3.com": [
				  {
					"type": "sigstoreSigned",
					"keyData": "dGVzdC1rZXktZGF0YQ==",
					"signedIdentity": {
					  "type": "matchRepoDigestOrExact"
					}
				  }
				]
			  },
			  "docker-daemon": {
				"": [
				  {
					"type": "insecureAcceptAnything"
				  }
				]
			  }
			}
		  }
	`)

	expectRet := map[string][]byte{
		testImagePolicyCR2.ObjectMeta.Namespace: expectTestnamespacedPolicy,
		testImagePolicyCR3.ObjectMeta.Namespace: expectTest1namespacedPolicy,
	}

	clusterScopePolicies, scopeNamespacePolicies, err := getValidScopePolicies([]*apicfgv1.ClusterImagePolicy{&testImagePolicyCR0, &testImagePolicyCR1}, []*apicfgv1.ImagePolicy{&testImagePolicyCR2, &testImagePolicyCR3, &testImagePolicyCR4}, nil)
	require.NoError(t, err)

	clusterOverridePolicyJSON, err := updatePolicyJSON(templatePolicyBytes, []string{}, []string{}, "release-reg.io/image/release", clusterScopePolicies)
	require.NoError(t, err)
	require.JSONEq(t, string(expectClusterPolicy), string(clusterOverridePolicyJSON))
	got, err := updateNamespacedPolicyJSONs(clusterOverridePolicyJSON, nil, nil, scopeNamespacePolicies)
	require.NoError(t, err)

	for namespace, v := range got {
		require.JSONEq(t, string(expectRet[namespace]), string(v))
	}
}
