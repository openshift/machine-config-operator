package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Golden payloads. legacyFullPeersFile is the exact frr-peers.json payload the
// operator used to write (resolved plaintext in "password"); the resolver must
// reproduce it byte-for-byte from fullPeersTransport.
const (
	legacyFullPeersFile = `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"s3cret","port":1790,"bfdEnabled":"true","ebgpMultiHop":"true","holdTime":"90","keepaliveTime":"30"}],"communities":["64512:100","64512:4200000000:300"],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"holdTime":"180"}]}}`
	fullPeersTransport  = `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"passwordSecret":{"name":"bgp-peer-pw","namespace":"openshift-config"},"port":1790,"bfdEnabled":"true","ebgpMultiHop":"true","holdTime":"90","keepaliveTime":"30"}],"communities":["64512:100","64512:4200000000:300"],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"holdTime":"180"}]}}`
	goldenDualStack     = `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513},{"peerAddress":"fd2e:6f44:5dd8:c956::1","peerASN":64513}],"apiVIPs":["192.168.111.5","fd2e:6f44:5dd8:c956::5"],"ingressVIPs":["192.168.111.4","fd2e:6f44:5dd8:c956::4"]}`
	// leakMarker is a synthetic password proving errors never carry Secret
	// contents.
	leakMarker = "SYNTHETIC-LEAK-MARKER"
)

// basicAuthSecret builds a kubernetes.io/basic-auth Secret with the given
// password.
func basicAuthSecret(namespace, name string, password []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeBasicAuth,
		Data:       map[string][]byte{"password": password},
	}
}

// recordingGetter serves the given Secrets and counts lookups per Secret.
type recordingGetter struct {
	secrets map[types.NamespacedName]*corev1.Secret
	calls   map[types.NamespacedName]int
}

// newRecordingGetter returns a getter serving the given Secrets.
func newRecordingGetter(secrets ...*corev1.Secret) *recordingGetter {
	g := &recordingGetter{secrets: map[types.NamespacedName]*corev1.Secret{}, calls: map[types.NamespacedName]int{}}
	for _, s := range secrets {
		g.secrets[types.NamespacedName{Namespace: s.Namespace, Name: s.Name}] = s
	}
	return g
}

// get implements BGPVIPSecretGetter and records the lookup.
func (g *recordingGetter) get(namespace, name string) (*corev1.Secret, error) {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	g.calls[key]++
	if s, ok := g.secrets[key]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("secrets %q not found", name)
}

// totalCalls returns the number of lookups made.
func (g *recordingGetter) totalCalls() int {
	n := 0
	for _, c := range g.calls {
		n += c
	}
	return n
}

// TestResolveBGPVIPPeersJSON locks the node-file bytes produced from the
// transport: verbatim without references, the pre-change payload with them.
func TestResolveBGPVIPPeersJSON(t *testing.T) {
	pw := basicAuthSecret("openshift-config", "bgp-peer-pw", []byte("s3cret"))
	cases := []struct {
		name          string
		transport     string
		secrets       []*corev1.Secret
		expected      string
		expectedCalls int
	}{
		{
			name:      "empty transport renders nothing",
			transport: "",
			expected:  "",
		},
		{
			name:      "no references: returned verbatim",
			transport: goldenDualStack,
			expected:  goldenDualStack,
		},
		{
			name:          "full case: reference resolved to the pre-change payload byte-for-byte",
			transport:     fullPeersTransport,
			secrets:       []*corev1.Secret{pw},
			expected:      legacyFullPeersFile,
			expectedCalls: 1,
		},
		{
			name:          "shared Secret on a default peer and a host override is read once",
			transport:     `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"passwordSecret":{"name":"bgp-peer-pw","namespace":"openshift-config"}}],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"passwordSecret":{"name":"bgp-peer-pw","namespace":"openshift-config"},"holdTime":"180"}],"master-1":[{"peerAddress":"192.168.111.3","peerASN":64514}]}}`,
			secrets:       []*corev1.Secret{pw},
			expected:      `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"s3cret"}],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"password":"s3cret","holdTime":"180"}],"master-1":[{"peerAddress":"192.168.111.3","peerASN":64514}]}}`,
			expectedCalls: 1,
		},
		{
			name:          "host-override-only references keep an empty defaultPeers list",
			transport:     `{"localASN":64512,"defaultPeers":[],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"passwordSecret":{"name":"bgp-peer-pw","namespace":"openshift-config"}}]}}`,
			secrets:       []*corev1.Secret{pw},
			expected:      `{"localASN":64512,"defaultPeers":[],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"password":"s3cret"}]}}`,
			expectedCalls: 1,
		},
		{
			// Any transport without references is rendered verbatim,
			// inline passwords included.
			name:      "legacy transport with inline passwords is passed through verbatim",
			transport: legacyFullPeersFile,
			expected:  legacyFullPeersFile,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getter := newRecordingGetter(tc.secrets...)
			got, err := ResolveBGPVIPPeersJSON(tc.transport, getter.get)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
			assert.Equal(t, tc.expectedCalls, getter.totalCalls())
			assert.NotContains(t, got, "passwordSecret")
			if got != "" {
				var buf bytes.Buffer
				require.NoError(t, json.Compact(&buf, []byte(got)))
				assert.Equal(t, got, buf.String(), "the peers file must be compact JSON")
			}
		})
	}

	t.Run("the getter's Secret is not mutated", func(t *testing.T) {
		secret := basicAuthSecret("openshift-config", "bgp-peer-pw", []byte("s3cret"))
		before := secret.DeepCopy()
		_, err := ResolveBGPVIPPeersJSON(fullPeersTransport, newRecordingGetter(secret).get)
		require.NoError(t, err)
		assert.Equal(t, before, secret)
	})
}

// TestResolveBGPVIPPeersJSONErrors locks the fail-closed cases and that
// errors never carry Secret data, peer addresses or hostnames.
func TestResolveBGPVIPPeersJSONErrors(t *testing.T) {
	withRef := func(namespace, name string) string {
		return fmt.Sprintf(`{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"passwordSecret":{"name":%q,"namespace":%q}}]}`, name, namespace)
	}
	overrideRef := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513}],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.3","peerASN":64514},{"peerAddress":"192.168.111.2","peerASN":64514,"passwordSecret":{"name":"bgp-peer-pw","namespace":"openshift-config"}}]}}`
	cases := []struct {
		name            string
		transport       string
		secrets         []*corev1.Secret
		nilGetter       bool
		expectContains  []string
		expectNoLookups bool
	}{
		{
			name:           "missing Secret",
			transport:      withRef("openshift-config", "bgp-peer-pw"),
			expectContains: []string{"defaultPeers[0]", "openshift-config/bgp-peer-pw", "not found"},
		},
		{
			name:      "wrong Secret type",
			transport: withRef("openshift-config", "bgp-peer-pw"),
			secrets: []*corev1.Secret{{
				ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer-pw", Namespace: "openshift-config"},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{"password": []byte(leakMarker)},
			}},
			expectContains: []string{"defaultPeers[0]", "openshift-config/bgp-peer-pw", "kubernetes.io/basic-auth"},
		},
		{
			name:           "empty password key",
			transport:      withRef("openshift-config", "bgp-peer-pw"),
			secrets:        []*corev1.Secret{basicAuthSecret("openshift-config", "bgp-peer-pw", nil)},
			expectContains: []string{"defaultPeers[0]", `non-empty "password" key`},
		},
		{
			name:           "password over 80 bytes",
			transport:      withRef("openshift-config", "bgp-peer-pw"),
			secrets:        []*corev1.Secret{basicAuthSecret("openshift-config", "bgp-peer-pw", bytes.Repeat([]byte(leakMarker), 5))},
			expectContains: []string{"defaultPeers[0]", "80-byte TCP MD5 limit"},
		},
		{
			name:            "reference outside openshift-config is refused without a lookup",
			transport:       withRef("kube-system", "bgp-peer-pw"),
			secrets:         []*corev1.Secret{basicAuthSecret("kube-system", "bgp-peer-pw", []byte(leakMarker))},
			expectContains:  []string{"defaultPeers[0]", `"kube-system"`, "only \"openshift-config\""},
			expectNoLookups: true,
		},
		{
			name:            "reference without a name",
			transport:       withRef("openshift-config", ""),
			expectContains:  []string{"defaultPeers[0]", "without a name"},
			expectNoLookups: true,
		},
		{
			name:            "inline password and reference together",
			transport:       `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"SYNTHETIC-LEAK-MARKER","passwordSecret":{"name":"bgp-peer-pw","namespace":"openshift-config"}}]}`,
			expectContains:  []string{"defaultPeers[0]", "both an inline password and a passwordSecret"},
			expectNoLookups: true,
		},
		{
			name:           "host override locator",
			transport:      overrideRef,
			expectContains: []string{"hostOverrides[0].peers[1]", "openshift-config/bgp-peer-pw"},
		},
		{
			name:           "no Secret source",
			transport:      withRef("openshift-config", "bgp-peer-pw"),
			nilGetter:      true,
			expectContains: []string{"defaultPeers[0]", "no Secret source"},
		},
		{
			name:           "malformed transport does not echo its content",
			transport:      `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","password":"SYNTHETIC-LEAK-MARKER"`,
			expectContains: []string{"not valid JSON"},
		},
		{
			name:           "mistyped transport names only the field",
			transport:      `{"localASN":"SYNTHETIC-LEAK-MARKER","defaultPeers":[]}`,
			expectContains: []string{"unexpected type", "localASN"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getter := newRecordingGetter(tc.secrets...)
			get := BGPVIPSecretGetter(getter.get)
			if tc.nilGetter {
				get = nil
			}
			got, err := ResolveBGPVIPPeersJSON(tc.transport, get)
			require.Error(t, err)
			assert.Empty(t, got)
			for _, want := range tc.expectContains {
				assert.Contains(t, err.Error(), want)
			}
			// Errors reach status conditions and logs: never Secret data,
			// peer addresses or hostnames.
			assert.NotContains(t, err.Error(), leakMarker)
			assert.NotContains(t, err.Error(), "s3cret")
			assert.NotContains(t, err.Error(), "192.168.111.")
			assert.NotContains(t, err.Error(), "master-")
			if tc.expectNoLookups {
				assert.Zero(t, getter.totalCalls())
			}
		})
	}
}

// TestBGPVIPPasswordSecretRefs locks the distinct, sorted Secret references
// used for event filtering.
func TestBGPVIPPasswordSecretRefs(t *testing.T) {
	refs, err := BGPVIPPasswordSecretRefs(`{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"passwordSecret":{"name":"b","namespace":"openshift-config"}},{"peerAddress":"192.168.111.4","peerASN":64513}],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"passwordSecret":{"name":"a","namespace":"openshift-config"}},{"peerAddress":"192.168.111.3","peerASN":64514,"passwordSecret":{"name":"b","namespace":"openshift-config"}}]}}`)
	require.NoError(t, err)
	assert.Equal(t, []types.NamespacedName{{Namespace: "openshift-config", Name: "a"}, {Namespace: "openshift-config", Name: "b"}}, refs)

	refs, err = BGPVIPPasswordSecretRefs("")
	require.NoError(t, err)
	assert.Empty(t, refs)

	refs, err = BGPVIPPasswordSecretRefs(goldenDualStack)
	require.NoError(t, err)
	assert.Empty(t, refs)

	_, err = BGPVIPPasswordSecretRefs(`{"localASN":`)
	assert.Error(t, err)
}

// TestBGPVIPPasswordFromSecret locks the Secret validation shared by the
// operator, operator bootstrap, the template controller and the bootstrap
// controller: basic-auth type, non-empty, at most 80 bytes, valid UTF-8 made
// of printable characters only. Errors never echo the value.
func TestBGPVIPPasswordFromSecret(t *testing.T) {
	cases := []struct {
		name       string
		secretType corev1.SecretType
		data       map[string][]byte
		expectErr  string
	}{
		{name: "valid", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte("s3cret")}},
		{name: "exactly 80 bytes", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": bytes.Repeat([]byte("x"), 80)}},
		{name: "printable ASCII specials and a space", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(`p<a>s&s"w\o/rd'! ~` + "`")}},
		{name: "printable non-ASCII", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte("pässwörd€")}},
		{name: "wrong type", secretType: corev1.SecretTypeOpaque, data: map[string][]byte{"password": []byte("s3cret")}, expectErr: "kubernetes.io/basic-auth"},
		{name: "missing key", secretType: corev1.SecretTypeBasicAuth, expectErr: `non-empty "password" key`},
		{name: "over 80 bytes", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": bytes.Repeat([]byte("x"), 81)}, expectErr: "80-byte TCP MD5 limit"},
		{name: "invalid UTF-8", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\xff\xfe")}, expectErr: "must be valid UTF-8"},
		{name: "truncated UTF-8 sequence", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\xc3")}, expectErr: "must be valid UTF-8"},
		{name: "DEL U+007F", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\u007f")}, expectErr: "only printable characters"},
		{name: "C1 control U+0080", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\u0080")}, expectErr: "only printable characters"},
		{name: "NEL U+0085", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\u0085")}, expectErr: "only printable characters"},
		{name: "C1 control U+009F", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\u009f")}, expectErr: "only printable characters"},
		{name: "noncharacter U+FFFE", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\ufffe")}, expectErr: "only printable characters"},
		{name: "noncharacter U+FFFF", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\uffff")}, expectErr: "only printable characters"},
		{name: "C0 control (newline)", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\n")}, expectErr: "only printable characters"},
		{name: "line separator U+2028", secretType: corev1.SecretTypeBasicAuth, data: map[string][]byte{"password": []byte(leakMarker + "\u2028")}, expectErr: "only printable characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer-pw", Namespace: "openshift-config"},
				Type:       tc.secretType,
				Data:       tc.data,
			}
			got, err := BGPVIPPasswordFromSecret(secret)
			if tc.expectErr == "" {
				require.NoError(t, err)
				assert.Equal(t, string(tc.data["password"]), got)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expectErr)
			assert.Contains(t, err.Error(), `"bgp-peer-pw"`)
			assert.NotContains(t, err.Error(), leakMarker)
			assert.Empty(t, got)
		})
	}
}

// TestResolveBGPVIPPeersJSONPrintableSpecialCharacters locks the node-file
// bytes for printable passwords that JSON escapes or that are non-ASCII: the
// output is exactly what the previous producer (json.Marshal of the same
// field order) wrote.
func TestResolveBGPVIPPeersJSONPrintableSpecialCharacters(t *testing.T) {
	transport := `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"passwordSecret":{"name":"bgp-peer-pw","namespace":"openshift-config"}}]}`
	for password, golden := range map[string]string{
		`p<a>s&s"w\o/rd'! ~`: `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"p\u003ca\u003es\u0026s\"w\\o/rd'! ~"}]}`,
		"pässwörd€":          `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"pässwörd€"}]}`,
	} {
		t.Run(password, func(t *testing.T) {
			got, err := ResolveBGPVIPPeersJSON(transport, newRecordingGetter(basicAuthSecret("openshift-config", "bgp-peer-pw", []byte(password))).get)
			require.NoError(t, err)
			assert.Equal(t, golden, got)
			legacy, err := json.Marshal(BGPVIPPeersConfig{LocalASN: 64512, DefaultPeers: []BGPVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513, Password: password}}})
			require.NoError(t, err)
			assert.Equal(t, string(legacy), got)
		})
	}
}

// TestSecretWithStringDataMerged locks the manifest stringData merge, done on
// a copy.
func TestSecretWithStringDataMerged(t *testing.T) {
	in := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer-pw", Namespace: "openshift-config"},
		Type:       corev1.SecretTypeBasicAuth,
		StringData: map[string]string{"password": "s3cret"},
	}
	out := SecretWithStringDataMerged(in)
	assert.Equal(t, []byte("s3cret"), out.Data["password"])
	assert.Empty(t, out.StringData)
	// The input is not modified.
	assert.Nil(t, in.Data)
	assert.Equal(t, "s3cret", in.StringData["password"])
}
