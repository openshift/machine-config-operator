package containerruntimeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestUpdatePolicyJSON(t *testing.T) {
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
		name             string
		allowed, blocked []string
		errorExpected    bool
		want             signature.Policy
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updatePolicyJSON(templateBytes, tt.blocked, tt.allowed, "release-reg.io/image/release")
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
			expectedErr: errors.New("invalid empty entry for source configuration"),
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
			expectedErr: errors.New("invalid empty entry for mirror configuration"),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{"*.block.com"},
			allowed:  []string{"*.allowed.com"},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{
				{
					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []apicfgv1.ImageMirror{"other.com/ns-o1"}},
						},
					},
				},
			},
			expectedErr: nil,
		},
	}

	for _, tc := range tests {
		res := validateRegistriesConfScopes(tc.insecure, tc.blocked, tc.allowed, nil, tc.idmsRules, nil)
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
