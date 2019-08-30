package registries

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/pkg/sysregistriesv2"
	"github.com/containers/image/types"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/diff"
)

func TestScopeMatchesRegistry(t *testing.T) {
	for _, tt := range []struct {
		scope, reg string
		expected   bool
	}{
		{"quay.io", "example.com", false},             // Host mismatch
		{"quay.io", "quay.io", true},                  // Host match
		{"quay.io:443", "quay.io", false},             // Port mismatch (although reg is a prefix of scope)
		{"quay.io:443", "quay.io:444", false},         // Port mismatch
		{"quay.io.example.com", "quay.io", false},     // Host mismatch (although reg is a prefix of scope)
		{"quay.io2", "quay.io", false},                // Host mismatch (although reg is a prefix of scope)
		{"quay.io/ns1", "quay.io", true},              // Valid namespace
		{"quay.io/ns1/ns2/ns3", "quay.io", true},      // Valid namespace
		{"quay.io/ns1/ns2/ns3", "not-quay.io", false}, // Host mismatch
	} {
		t.Run(fmt.Sprintf("%#v, %#v", tt.scope, tt.reg), func(t *testing.T) {
			res := scopeMatchesRegistry(tt.scope, tt.reg)
			assert.Equal(t, tt.expected, res)
		})
	}
}

func TestRDMContainsARealMirror(t *testing.T) {
	const source = "source.example.com"

	for _, tt := range []struct {
		mirrors  []string
		expected bool
	}{
		{[]string{}, false},                                  // No mirrors listed
		{[]string{"mirror.local"}, true},                     // A single real mirror
		{[]string{source}, false},                            // The source only
		{[]string{source, source, source}, false},            // Source only, repeated
		{[]string{"mirror.local", source}, true},             // Both
		{[]string{source, "mirror.local"}, true},             // Both
		{[]string{"m1.local", "m2.local", "m3.local"}, true}, // Multiple real mirrors
	} {
		t.Run(fmt.Sprintf("%#v", tt.mirrors), func(t *testing.T) {
			set := apioperatorsv1alpha1.RepositoryDigestMirrors{
				Source:  source,
				Mirrors: tt.mirrors,
			}
			res := rdmContainsARealMirror(&set)
			assert.Equal(t, tt.expected, res)
		})
	}
}

func TestMergedMirrorSets(t *testing.T) {
	for _, c := range []struct {
		name   string
		input  [][]apioperatorsv1alpha1.RepositoryDigestMirrors
		result []apioperatorsv1alpha1.RepositoryDigestMirrors
	}{
		{
			name:   "Empty",
			input:  [][]apioperatorsv1alpha1.RepositoryDigestMirrors{},
			result: []apioperatorsv1alpha1.RepositoryDigestMirrors{},
		},
		{
			name: "Irrelevant singletons",
			input: [][]apioperatorsv1alpha1.RepositoryDigestMirrors{
				{
					{Source: "a.example.com", Mirrors: nil},
					{Source: "b.example.com", Mirrors: []string{}},
				},
			},
			result: []apioperatorsv1alpha1.RepositoryDigestMirrors{},
		},
		// The registry names below start with an irrelevant letter, usually counting from the end of the alphabet, to verify that
		// the result is based on the order in the Sources array and is not just alphabetically-sorted.
		{
			name: "Separate mirror sets",
			input: [][]apioperatorsv1alpha1.RepositoryDigestMirrors{
				{
					{Source: "source.example.net", Mirrors: []string{"z1.example.net", "y2.example.net", "x3.example.net"}},
				},
				{
					{Source: "source.example.com", Mirrors: []string{"z1.example.com", "y2.example.com", "x3.example.com"}},
				},
			},
			result: []apioperatorsv1alpha1.RepositoryDigestMirrors{
				{Source: "source.example.com", Mirrors: []string{"z1.example.com", "y2.example.com", "x3.example.com"}},
				{Source: "source.example.net", Mirrors: []string{"z1.example.net", "y2.example.net", "x3.example.net"}},
			},
		},
		{
			name: "Sets with a shared element - strict order",
			input: [][]apioperatorsv1alpha1.RepositoryDigestMirrors{
				{
					{Source: "source.example.net", Mirrors: []string{"z1.example.net", "y2.example.net"}},
					{Source: "source.example.com", Mirrors: []string{"z1.example.com", "y2.example.com"}},
				},
				{
					{Source: "source.example.net", Mirrors: []string{"y2.example.net", "x3.example.net"}},
					{Source: "source.example.com", Mirrors: []string{"y2.example.com", "x3.example.com"}},
				},
			},
			result: []apioperatorsv1alpha1.RepositoryDigestMirrors{
				{Source: "source.example.com", Mirrors: []string{"z1.example.com", "y2.example.com", "x3.example.com"}},
				{Source: "source.example.net", Mirrors: []string{"z1.example.net", "y2.example.net", "x3.example.net"}},
			},
		},
		{
			// This is not technically impossible, and it could be in principle used to set up last-fallback mirrors that
			// are only accessed if the source is not available.
			// WARNING: The order in this case is unspecified by the ICSP specification, and may change at any time;
			// this test case only ensures that the corner case is handled reasonably, and that the output is stable
			// (i.e. the operator does not cause unnecessary changes in output objects.)
			name: "Source included in mirrors",
			input: [][]apioperatorsv1alpha1.RepositoryDigestMirrors{
				{
					{Source: "source.example.com", Mirrors: []string{"z1.example.com", "source.example.com", "y2.example.com"}},
					{Source: "source.example.com", Mirrors: []string{"source.example.com", "y2.example.com", "x3.example.com"}},
				},
			},
			result: []apioperatorsv1alpha1.RepositoryDigestMirrors{
				{Source: "source.example.com", Mirrors: []string{"z1.example.com", "source.example.com", "y2.example.com", "x3.example.com"}},
			},
		},
		{
			// Worst case of the above: _only_ the source included in mirrors, even perhaps several times.
			name: "Mirrors includes only source",
			input: [][]apioperatorsv1alpha1.RepositoryDigestMirrors{
				{
					{Source: "source.example.com", Mirrors: []string{"source.example.com"}},
					{Source: "source.example.net", Mirrors: []string{"source.example.net", "source.example.net", "source.example.net"}},
				},
			},
			result: []apioperatorsv1alpha1.RepositoryDigestMirrors{},
		},
		// More complex mirror set combinations are mostly tested in TestTopoGraph
		{
			name: "Example",
			input: [][]apioperatorsv1alpha1.RepositoryDigestMirrors{
				{ // Vendor-provided default configuration
					{Source: "source.vendor.com", Mirrors: []string{"registry2.vendor.com"}},
				},
				{ // Vendor2-provided default configuration
					{Source: "source.vendor2.com", Mirrors: []string{"registry1.vendor2.com", "registry2.vendor2.com"}},
				},
				{ // Admin-configured local mirrors:
					{Source: "source.vendor.com", Mirrors: []string{"local-mirror.example.com"}},
					// Opposite order of the vendor’s mirrors.
					// WARNING: The order in this case is unspecified by the ICSP specification, and may change at any time;
					// this test case only ensures that the corner case is handled reasonably, and that the output is stable
					// (i.e. the operator does not cause unnecessary changes in output objects.)
					{Source: "source.vendor2.com", Mirrors: []string{"local-mirror2.example.com", "registry2.vendor2.com", "registry1.vendor2.com"}},
				},
			},
			result: []apioperatorsv1alpha1.RepositoryDigestMirrors{
				{Source: "source.vendor.com", Mirrors: []string{"local-mirror.example.com", "registry2.vendor.com"}},
				{Source: "source.vendor2.com", Mirrors: []string{"local-mirror2.example.com", "registry1.vendor2.com", "registry2.vendor2.com"}},
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := []*apioperatorsv1alpha1.ImageContentSourcePolicy{}
			for _, rdms := range c.input {
				in = append(in, &apioperatorsv1alpha1.ImageContentSourcePolicy{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: rdms,
					},
				})
			}
			res, err := mergedMirrorSets(in)
			if err != nil {
				t.Errorf("Error %v", err)
				return
			}
			if !reflect.DeepEqual(res, c.result) {
				t.Errorf("Result %#v, expected %#v", res, c.result)
				return
			}
		})
	}
}

func TestEditRegistriesConfig(t *testing.T) {
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
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "common.com",
							Insecure: true,
						},
						Blocked: true,
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com",
						},
						Blocked: true,
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "docker.io",
						},
						Blocked: true,
					},
				},
			},
		},
		{
			name:     "insecure+blocked prefixes",
			insecure: []string{"insecure.com"},
			blocked:  []string{"blocked.com"},
			icspRules: []*apioperatorsv1alpha1.ImageContentSourcePolicy{
				{
					Spec: apioperatorsv1alpha1.ImageContentSourcePolicySpec{
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{ // other.com is neither insecure nor blocked
							{Source: "insecure.com/ns-i1", Mirrors: []string{"blocked.com/ns-b1", "other.com/ns-o1"}},
							{Source: "blocked.com/ns-b/ns2-b", Mirrors: []string{"other.com/ns-o2", "insecure.com/ns-i2"}},
							{Source: "other.com/ns-o3", Mirrors: []string{"insecure.com/ns-i2", "blocked.com/ns-b/ns3-b"}},
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
						},
					},

					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com",
							Insecure: true,
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com",
						},
						Blocked: true,
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create config from templateBytes to get a fresh copy we can edit.
			config := sysregistriesv2.V2RegistriesConf{}
			_, err := toml.Decode(string(templateBytes), &config)
			require.NoError(t, err)
			err = EditRegistriesConfig(&config, tt.insecure, tt.blocked, tt.icspRules)
			if err != nil {
				t.Errorf("updateRegistriesConfig() error = %v", err)
				return
			}
			// This assumes a specific order of Registries entries, which does not actually matter; ideally, this would
			// sort the two arrays before comparing, but right now hard-coding the order works well enough.
			if !reflect.DeepEqual(config, tt.want) {
				t.Errorf("updateRegistriesConfig() Diff:\n %s", diff.ObjectGoPrintDiff(tt.want, config))
			}
			// Ensure that the generated configuration is actually valid.
			buf := bytes.Buffer{}
			err = toml.NewEncoder(&buf).Encode(config)
			require.NoError(t, err)
			registriesConf, err := ioutil.TempFile("", "registries.conf")
			require.NoError(t, err)
			_, err = registriesConf.Write(buf.Bytes())
			require.NoError(t, err)
			defer os.Remove(registriesConf.Name())
			_, err = sysregistriesv2.GetRegistries(&types.SystemContext{
				SystemRegistriesConfPath: registriesConf.Name(),
			})
			assert.NoError(t, err)
		})
	}
}
