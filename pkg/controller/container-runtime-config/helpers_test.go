package containerruntimeconfig

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	signature "github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/types"
	apicfgv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/diff"
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
		icspRules         []*apioperatorsv1alpha1.ImageContentSourcePolicy
		icpRules          []*apicfgv1.ImageContentPolicy
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
			name:     "insecure+blocked prefixes with wildcard entries",
			insecure: []string{"insecure.com", "*.insecure-example.com", "*.insecure.blocked-example.com"},
			blocked:  []string{"blocked.com", "*.blocked.insecure-example.com", "*.blocked-example.com"},
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{ // other.com is neither insecure nor blocked
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
						Blocked:            true,
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "other.com/ns-o2"},
							{Location: "insecure.com/ns-i2", Insecure: true},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com/ns-i1",
							Insecure: true,
						},
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "blocked.com/ns-b1"},
							{Location: "other.com/ns-o1"},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "other.com/ns-o3",
						},
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "insecure.com/ns-i2", Insecure: true},
							{Location: "blocked.com/ns-b/ns3-b"},
							{Location: "foo.insecure-example.com/bar", Insecure: true},
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
			name:     "icp,insecure+blocked prefixes with wildcard entries",
			insecure: []string{"insecure.com", "*.insecure-example.com", "*.insecure.blocked-example.com"},
			blocked:  []string{"blocked.com", "*.blocked.insecure-example.com", "*.blocked-example.com"},
			icpRules: []*apicfgv1.ImageContentPolicy{
				{
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.Mirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
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
						Blocked:            true,
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "other.com/ns-o2"},
							{Location: "insecure.com/ns-i2", Insecure: true},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com/ns-i1",
							Insecure: true,
						},
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "blocked.com/ns-b1"},
							{Location: "other.com/ns-o1"},
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "other.com/ns-o3",
						},
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "insecure.com/ns-i2", Insecure: true},
							{Location: "blocked.com/ns-b/ns3-b"},
							{Location: "foo.insecure-example.com/bar", Insecure: true},
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			icpRules := mergeToICPRules(tt.icspRules, tt.icpRules)
			got, err := updateRegistriesConfig(templateBytes, tt.insecure, tt.blocked, icpRules)
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
			if !reflect.DeepEqual(gotConf, tt.want) {
				t.Errorf("updateRegistriesConfig() Diff:\n %s", diff.ObjectGoPrintDiff(tt.want, gotConf))
			}
			// Ensure that the generated configuration is actually valid.
			registriesConf, err := ioutil.TempFile("", "registries.conf")
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

func TestUpdatePolicyJSON(t *testing.T) {
	templateConfig := signature.Policy{
		Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
		Transports: map[string]signature.PolicyTransportScopes{
			"docker-daemon": map[string]signature.PolicyRequirements{
				"": signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
			},
		},
	}
	buf := bytes.Buffer{}
	err := json.NewEncoder(&buf).Encode(templateConfig)
	require.NoError(t, err)
	templateBytes := buf.Bytes()

	tests := []struct {
		name             string
		allowed, blocked []string
		want             signature.Policy
	}{
		{
			name: "unchanged",
			want: templateConfig,
		},
		{
			name:    "allowed",
			allowed: []string{"allow.io", "*.allowed-example.com"},
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRReject()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"allow.io":              signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
						"*.allowed-example.com": signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
					},
					"docker": map[string]signature.PolicyRequirements{
						"allow.io":              signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
						"*.allowed-example.com": signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
		},
		{
			name:    "blocked",
			blocked: []string{"block.com", "*.blocked-example.com"},
			want: signature.Policy{
				Default: signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
				Transports: map[string]signature.PolicyTransportScopes{
					"atomic": map[string]signature.PolicyRequirements{
						"block.com":             signature.PolicyRequirements{signature.NewPRReject()},
						"*.blocked-example.com": signature.PolicyRequirements{signature.NewPRReject()},
					},
					"docker": map[string]signature.PolicyRequirements{
						"block.com":             signature.PolicyRequirements{signature.NewPRReject()},
						"*.blocked-example.com": signature.PolicyRequirements{signature.NewPRReject()},
					},
					"docker-daemon": map[string]signature.PolicyRequirements{
						"": signature.PolicyRequirements{signature.NewPRInsecureAcceptAnything()},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updatePolicyJSON(templateBytes, tt.blocked, tt.allowed)
			if err != nil {
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

func TestMergeToICPRules(t *testing.T) {
	for _, tc := range []struct {
		icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy
		icpRules  []*apicfgv1.ImageContentPolicy
		expected  []*apicfgv1.ImageContentPolicy
	}{
		{
			// convert icsp rules to apicfgv1.ImageContentPolicy, expect icpRules
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
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "other.com/ns-o3", Mirrors: []string{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			icpRules: []*apicfgv1.ImageContentPolicy{},
			expected: []*apicfgv1.ImageContentPolicy{
				{
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.Mirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.Mirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
		},
		{
			// convert and append the icsprules to icprules, except the conflic setting for the same source
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []string{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "other.com/ns-o3", Mirrors: []string{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "other.com/ns-o3", Mirrors: []string{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			icpRules: []*apicfgv1.ImageContentPolicy{
				{
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1"}, AllowMirrorByTags: true},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
						},
					},
				},
			},
			expected: []*apicfgv1.ImageContentPolicy{
				{
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1"}, AllowMirrorByTags: true},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
						},
					},
				},
				{
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.Mirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.Mirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
		},
	} {
		res := mergeToICPRules(tc.icspRules, tc.icpRules)
		if !reflect.DeepEqual(res, tc.expected) {
			t.Errorf("mergeToICPRules() Diff:\n %s", diff.ObjectGoPrintDiff(tc.expected, res))
		}
	}
}

func TestValidateICPRules(t *testing.T) {
	for _, tc := range []struct {
		icpRules  []*apicfgv1.ImageContentPolicy
		expectErr error
	}{
		{
			icpRules:  []*apicfgv1.ImageContentPolicy{},
			expectErr: nil,
		},
		{
			// valid case
			icpRules: []*apicfgv1.ImageContentPolicy{
				{
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1"}, AllowMirrorByTags: true},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"other.com/ns-o2"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"insecure.com/ns-i2"}},
						},
					},
				},
				{
					// no conflict duplicate previouse spec
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1"}, AllowMirrorByTags: true},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"other.com/ns-o2"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"insecure.com/ns-i2"}},
						},
					},
				},
				{
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{
							// no conflict add new source
							{Source: "other.com/ns-b/ns2-b", Mirrors: []apicfgv1.Mirror{"other.com/ns-o2", "insecure.com/ns-i2"}},
						},
					},
				},
				{
					// no conflict explicit false or leave the default
					Spec: apicfgv1.ImageContentPolicySpec{
						RepositoryDigestMirrors: []apicfgv1.RepositoryDigestMirrors{
							{Source: "other.com/ns-i1", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1"}, AllowMirrorByTags: true},
							{Source: "other.com/ns-i2", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1"}},
							{Source: "other.com/ns-i2", Mirrors: []apicfgv1.Mirror{"blocked.com/ns-b1"}, AllowMirrorByTags: false},
						},
					},
				},
			},
			expectErr: nil,
		},
	} {
		err := validateICPRules(tc.icpRules)
		require.Equal(t, tc.expectErr, err)
	}
}
