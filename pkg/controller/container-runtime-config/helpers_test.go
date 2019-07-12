package containerruntimeconfig

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

func TestDisjointOrderedMirrorSets(t *testing.T) {
	for _, c := range []struct {
		name   string
		input  [][][]string
		result [][]string
	}{
		{
			name:   "Empty",
			input:  [][][]string{},
			result: [][]string{},
		},
		{
			name: "Irrelevant singletons",
			input: [][][]string{
				{
					nil,
					{},
				},
				{
					{"a.example.com"},
					{"b.example.com"},
				},
			},
			result: [][]string{},
		},
		// The registry names below start with an irrelevant letter, usually counting from the end of the alphabet, to verify that
		// the result is based on the order in the Sources array and is not just alphabetically-sorted.
		{
			name: "Separate ordered sets",
			input: [][][]string{
				{
					{"z1.example.net", "y2.example.net", "x3.example.net"},
				},
				{
					{"z1.example.com", "y2.example.com", "x3.example.com"},
				},
			},
			result: [][]string{
				{"z1.example.com", "y2.example.com", "x3.example.com"},
				{"z1.example.net", "y2.example.net", "x3.example.net"},
			},
		},
		{
			name: "Sets with a shared element - strict order",
			input: [][][]string{
				{
					{"z1.example.net", "y2.example.net"},
					{"z1.example.com", "y2.example.com"},
				},
				{
					{"y2.example.net", "x3.example.net"},
					{"y2.example.com", "x3.example.com"},
				},
			},
			result: [][]string{
				{"z1.example.com", "y2.example.com", "x3.example.com"},
				{"z1.example.net", "y2.example.net", "x3.example.net"},
			},
		},
		// More complex mirror set combinations are mostly tested in TestTopoGraph
		{
			name: "Example",
			input: [][][]string{
				{ // Vendor-provided default configuration
					{"registry1.vendor.com", "registry2.vendor.com"},
				},
				{ // Vendor2-provided default configuration
					{"registry1.vendor2.com", "registry2.vendor2.com"},
				},
				{ // Admin-configured local mirrors:
					{"local-mirror.example.com", "registry1.vendor.com"},
					{"local-mirror2.example.com", "registry2.vendor2.com", "registry1.vendor2.com"}, // Opposite order of the vendor’s mirrors
				},
			},
			result: [][]string{
				{"local-mirror.example.com", "registry1.vendor.com", "registry2.vendor.com"},
				{"local-mirror2.example.com", "registry1.vendor2.com", "registry2.vendor2.com"},
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := []*apioperatorsv1alpha1.ImageContentSourcePolicy{}
			for _, l1 := range c.input {
				in1 := apioperatorsv1alpha1.ImageContentSourcePolicy{}
				for _, l2 := range l1 {
					in1.Spec.RepositoryDigestMirrors = append(in1.Spec.RepositoryDigestMirrors, apioperatorsv1alpha1.RepositoryDigestMirrors{
						Sources: l2,
					})
				}
				in = append(in, &in1)
			}
			res, err := disjointOrderedMirrorSets(in)
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
						RepositoryDigestMirrors: []apioperatorsv1alpha1.RepositoryDigestMirrors{
							{Sources: []string{"insecure.com/ns-i1", "blocked.com/ns-b1", "unrelated.com/ns-u1"}},
							{Sources: []string{"blocked.com/ns-b/ns2-b", "unrelated.com/ns-u2", "insecure.com/ns-i2"}},
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
							{Location: "blocked.com/ns-b/ns2-b"},
							{Location: "unrelated.com/ns-u2"},
							{Location: "insecure.com/ns-i2", Insecure: true},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "unrelated.com/ns-u2",
						},
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "blocked.com/ns-b/ns2-b"},
							{Location: "unrelated.com/ns-u2"},
							{Location: "insecure.com/ns-i2", Insecure: true},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com/ns-i2",
							Insecure: true,
						},
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "blocked.com/ns-b/ns2-b"},
							{Location: "unrelated.com/ns-u2"},
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
							{Location: "insecure.com/ns-i1", Insecure: true},
							{Location: "blocked.com/ns-b1"},
							{Location: "unrelated.com/ns-u1"},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com/ns-b1",
						},
						Blocked:            true,
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "insecure.com/ns-i1", Insecure: true},
							{Location: "blocked.com/ns-b1"},
							{Location: "unrelated.com/ns-u1"},
						},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "unrelated.com/ns-u1",
						},
						MirrorByDigestOnly: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "insecure.com/ns-i1", Insecure: true},
							{Location: "blocked.com/ns-b1"},
							{Location: "unrelated.com/ns-u1"},
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
