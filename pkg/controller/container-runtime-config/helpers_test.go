package containerruntimeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updateRegistriesConfig(templateBytes, tt.insecure, tt.blocked, tt.icspRules)
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
		icspRules   []*apioperatorsv1alpha1.ImageContentSourcePolicy
		expectedErr error
	}{
		{
			insecure: []string{""},
			blocked:  []string{"*.block.com"},
			allowed:  []string{"*.allowed.com"},
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
			expectedErr: errors.New(`invalid entry for insecure registries ""`),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{""},
			allowed:  []string{"*.allowed.com"},
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
			expectedErr: errors.New(`invalid entry for blocked registries ""`),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{"*.block.com"},
			allowed:  []string{""},
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
			expectedErr: errors.New(`invalid entry for allowed registries ""`),
		},
		{
			insecure: []string{"*.insecure.com"},
			blocked:  []string{"*.block.com"},
			allowed:  []string{"*.allowed.com"},
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "", Mirrors: []string{"blocked.com/ns-b1", "other.com/ns-o1"}},
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
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []string{"", "other.com/ns-o1"}},
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
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "insecure.com/ns-i1", Mirrors: []string{"other.com/ns-o1"}},
						},
					},
				},
			},
			expectedErr: nil,
		},
	}

	for _, tc := range tests {
		res := validateRegistriesConfScopes(tc.insecure, tc.blocked, tc.allowed, tc.icspRules)
		require.Equal(t, tc.expectedErr, res)
	}
}

func TestGetValidBlockAndAllowedRegistries(t *testing.T) {
	tests := []struct {
		name, releaseImg                                                  string
		imgSpec                                                           *apicfgv1.ImageSpec
		icspRules                                                         []*apioperatorsv1alpha1.ImageContentSourcePolicy
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
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "src.io/payload", Mirrors: []string{"mirror-1.io/payload", "mirror-2.io/payload"}},
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
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "src.io/payload", Mirrors: []string{"mirror-1.io/payload", "mirror-2.io/payload"}},
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
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "payload-reg.io/release-image", Mirrors: []string{"mirror-1.io/payload", "mirror-2.io/payload"}},
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
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "quay.io", Mirrors: []string{"block.io"}}, // quay.io/openshift-release-dev -> block.io/openshift-release-dev
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
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "quay.io/openshift-release-dev", Mirrors: []string{"block.io/openshift-release-dev"}}, // quay.io/openshift-release-dev -> block.io/openshift-release-dev
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
			gotRegistries, gotPolicy, gotAllowed, err := getValidBlockedAndAllowedRegistries(tt.releaseImg, tt.imgSpec, tt.icspRules)
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

func TestConvertICSPtoIDMS(t *testing.T) {
	for _, tc := range []struct {
		icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy
		idmsRules []*apicfgv1.ImageDigestMirrorSet
		expected  []*apicfgv1.ImageDigestMirrorSet
	}{
		{
			// convert icsp rules to apicfgv1.ImageDigestMirrorSet, expect idmsRules
			// no existing idms
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icsp-0",
					},
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "registry-a.com/ns-a", Mirrors: []string{"mirror-a-1.com/ns-a", "mirror-a-2.com/ns-a"}},
							{Source: "registry-b/ns-b/ns1-b", Mirrors: []string{"mirror-b-1.com/ns-b", "mirror-b-2.com/ns-b"}},
							{Source: "registry-c/ns-c", Mirrors: []string{"mirror-c-1.com/ns-c", "mirror-c-2.com/ns-c/ns1-c", "mirror-c-3.com/ns-c/ns1-c"}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "icsp-1",
					},
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Source: "other.com/ns-o3", Mirrors: []string{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{},
			expected: []*apicfgv1.ImageDigestMirrorSet{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "icsp-0",
						Labels: map[string]string{CreatedICSPLabelKey: "true"},
					},

					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "registry-a.com/ns-a", Mirrors: []apicfgv1.ImageMirror{"mirror-a-1.com/ns-a", "mirror-a-2.com/ns-a"}},
							{Source: "registry-b/ns-b/ns1-b", Mirrors: []apicfgv1.ImageMirror{"mirror-b-1.com/ns-b", "mirror-b-2.com/ns-b"}},
							{Source: "registry-c/ns-c", Mirrors: []apicfgv1.ImageMirror{"mirror-c-1.com/ns-c", "mirror-c-2.com/ns-c/ns1-c", "mirror-c-3.com/ns-c/ns1-c"}},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "icsp-1",
						Labels: map[string]string{CreatedICSPLabelKey: "true"},
					},

					Spec: apicfgv1.ImageDigestMirrorSetSpec{
						ImageDigestMirrors: []apicfgv1.ImageDigestMirrors{
							{Source: "other.com/ns-o3", Mirrors: []apicfgv1.ImageMirror{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b", "foo.insecure-example.com/bar"}},
						},
					},
				},
			},
		},
		{
			// no existing icspRules
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{},
			idmsRules: []*apicfgv1.ImageDigestMirrorSet{},
			expected:  []*apicfgv1.ImageDigestMirrorSet{},
		},
	} {
		res := convertICSPtoIDMS(tc.icspRules)
		if !reflect.DeepEqual(res, tc.expected) {
			t.Errorf("mergeICSPtoIDMS Diff:\n %s", diff.ObjectGoPrintDiff(tc.expected, res))
		}
	}
}
