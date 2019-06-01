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

func TestUpdateRegistriesConfig(t *testing.T) {
	templateConfig := sysregistriesv2.V2RegistriesConf{ // This matches templates/*/01-*-container-runtime/_base/files/container-registries.yaml
		UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
	}

	tests := []struct {
		name              string
		input             sysregistriesv2.V2RegistriesConf
		insecure, blocked []string
		want              sysregistriesv2.V2RegistriesConf
	}{
		{
			name:  "unchanged",
			input: templateConfig,
			want:  templateConfig,
		},
		{
			name:     "insecure+blocked",
			input:    templateConfig,
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
			name: "insecure+blocked prefixes", // This is artificial, because mirror entries can’t currently be created.
			input: sysregistriesv2.V2RegistriesConf{
				Registries: []sysregistriesv2.Registry{
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com/ns-i",
						},
						Mirrors: []sysregistriesv2.Endpoint{{Location: "blocked.com/ns-bm"}, {Location: "unrelated.com/ns-u"}},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com/ns-b/ns2-b",
						},
						Mirrors: []sysregistriesv2.Endpoint{{Location: "unrelated.com/ns-u"}, {Location: "insecure.com/ns-im"}},
					},
				},
			},
			insecure: []string{"insecure.com"},
			blocked:  []string{"blocked.com"},
			want: sysregistriesv2.V2RegistriesConf{
				Registries: []sysregistriesv2.Registry{
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "insecure.com/ns-i",
							Insecure: true,
						},
						Mirrors: []sysregistriesv2.Endpoint{{Location: "blocked.com/ns-bm"}, {Location: "unrelated.com/ns-u"}},
					},
					{
						Endpoint: sysregistriesv2.Endpoint{
							Location: "blocked.com/ns-b/ns2-b",
						},
						Blocked: true,
						Mirrors: []sysregistriesv2.Endpoint{
							{Location: "unrelated.com/ns-u"},
							{Location: "insecure.com/ns-im", Insecure: true},
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
			buf := bytes.Buffer{}
			if err := toml.NewEncoder(&buf).Encode(tt.input); err != nil {
				t.Errorf("Error marshaling templateConfig: %v", err)
				return
			}
			templateBytes := buf.Bytes()
			got, err := updateRegistriesConfig(templateBytes, tt.insecure, tt.blocked)
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
