package daemon

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// writeTempFile writes contents to a file in a per-test temporary directory and
// returns its path.
func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	return path
}

func TestGetMCSConnectionInfo(t *testing.T) {
	tests := []struct {
		name            string
		contents        string
		missing         bool
		expectErr       bool
		expectURL       string
		expectTLSMinVer string
	}{
		{
			name:            "url and TLS min version",
			contents:        `{"url":"https://api-int.test.example.com:22623","tlsMinVersion":"VersionTLS13"}`,
			expectURL:       "https://api-int.test.example.com:22623",
			expectTLSMinVer: "VersionTLS13",
		},
		{
			// tlsMinVersion is omitempty, so an MCS serving the default profile
			// writes the URL alone. That must still be usable.
			name:      "url only",
			contents:  `{"url":"https://api-int.test.example.com:22623"}`,
			expectURL: "https://api-int.test.example.com:22623",
		},
		{
			// The file is absent on clusters with reporting disabled, which is the
			// common case rather than an error worth escalating.
			name:      "file absent",
			missing:   true,
			expectErr: true,
		},
		{
			// A node provisioned by an older MCS could carry a bare URL at the old
			// path. The rename means we should never find it here, but parsing must
			// fail closed rather than produce a half-populated struct.
			name:      "not JSON",
			contents:  "https://api-int.test.example.com:22623",
			expectErr: true,
		},
		{
			// Valid JSON with nothing to connect to is as good as no file at all.
			name:      "empty url",
			contents:  `{"tlsMinVersion":"VersionTLS12"}`,
			expectErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "connection-info.json")
			if !test.missing {
				path = writeTempFile(t, "connection-info.json", test.contents)
			}

			connInfo, err := getMCSConnectionInfo(path)
			if test.expectErr {
				assert.Error(t, err)
				assert.Nil(t, connInfo)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.expectURL, connInfo.URL)
			assert.Equal(t, test.expectTLSMinVer, connInfo.TLSMinVersion)
		})
	}
}

// testCAPEM returns the PEM encoding of an httptest TLS server's certificate,
// which stands in for the MCS root CA bundle the server writes to the node.
func testCAPEM(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

func TestBuildMCSTLSConfig(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	caPEM := testCAPEM(t, srv)

	t.Run("trusts only the supplied bundle", func(t *testing.T) {
		cfg, err := buildMCSTLSConfig(writeTempFile(t, "ca.crt", caPEM), "")
		require.NoError(t, err)
		require.NotNil(t, cfg.RootCAs)
		// There is deliberately no fallback to the system pool, so the MCS cert
		// must verify against this config and nothing else should be trusted.
		assert.False(t, cfg.InsecureSkipVerify)
	})

	t.Run("min version", func(t *testing.T) {
		tests := []struct {
			name     string
			input    string
			expected uint16
		}{
			// An empty value means the MCS served no explicit floor, which the
			// Kubernetes flag helper already resolves to TLS 1.2.
			{name: "empty falls back to 1.2", input: "", expected: tls.VersionTLS12},
			{name: "1.2 is honoured", input: "VersionTLS12", expected: tls.VersionTLS12},
			// A raised cluster profile must reach the client, otherwise the report
			// would negotiate below what the MCS itself enforces.
			{name: "1.3 is honoured", input: "VersionTLS13", expected: tls.VersionTLS13},
			// An unrecognized value is not worth dropping the only signal this
			// node can send, so it degrades to the default floor instead.
			{name: "unknown falls back to 1.2", input: "VersionTLS99", expected: tls.VersionTLS12},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				cfg, err := buildMCSTLSConfig(writeTempFile(t, "ca.crt", caPEM), test.input)
				require.NoError(t, err)
				assert.Equal(t, test.expected, cfg.MinVersion)
			})
		}
	})

	t.Run("bundle absent", func(t *testing.T) {
		cfg, err := buildMCSTLSConfig(filepath.Join(t.TempDir(), "missing.crt"), "")
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})

	t.Run("bundle holds no certificates", func(t *testing.T) {
		cfg, err := buildMCSTLSConfig(writeTempFile(t, "ca.crt", "not a certificate"), "")
		assert.Error(t, err)
		assert.Nil(t, cfg)
	})
}

func TestSendFailureReportHTTP(t *testing.T) {
	report := ctrlcommon.FirstbootFailureReport{
		Pool:         "worker",
		NodeID:       "test-node-1",
		Stage:        "firstboot-update",
		ImageURL:     "quay.io/openshift/release@sha256:abc123",
		ErrorMessage: "failed to update OS",
	}

	type capture struct {
		method      string
		path        string
		contentType string
		body        ctrlcommon.FirstbootFailureReport
	}

	// newServer returns a TLS server that records the request it receives and
	// replies with status, along with a tls.Config pinned to its certificate.
	newServer := func(t *testing.T, status int) (*httptest.Server, *tls.Config, *capture) {
		t.Helper()
		got := new(capture)
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got.method = r.Method
			got.path = r.URL.Path
			got.contentType = r.Header.Get("Content-Type")
			if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
				t.Errorf("failed to decode request body: %v", err)
			}
			w.WriteHeader(status)
		}))
		t.Cleanup(srv.Close)

		certPool := x509.NewCertPool()
		certPool.AddCert(srv.Certificate())
		return srv, &tls.Config{RootCAs: certPool, MinVersion: tls.VersionTLS12}, got
	}

	t.Run("posts the report to the node-failure endpoint", func(t *testing.T) {
		srv, tlsConfig, got := newServer(t, http.StatusAccepted)

		require.NoError(t, sendFailureReportHTTP(srv.URL, tlsConfig, &report))

		assert.Equal(t, http.MethodPost, got.method)
		assert.Equal(t, "/v1/node-failure", got.path)
		assert.Equal(t, "application/json", got.contentType)
		assert.Equal(t, report, got.body)
	})

	t.Run("trailing slash on the base URL does not duplicate the separator", func(t *testing.T) {
		srv, tlsConfig, got := newServer(t, http.StatusAccepted)

		require.NoError(t, sendFailureReportHTTP(srv.URL+"/", tlsConfig, &report))

		assert.Equal(t, "/v1/node-failure", got.path)
	})

	t.Run("non-202 response is an error", func(t *testing.T) {
		// The caller uses this to decide whether to write the sentinel file, so a
		// rejected report must not be recorded as delivered.
		srv, tlsConfig, _ := newServer(t, http.StatusBadRequest)

		assert.Error(t, sendFailureReportHTTP(srv.URL, tlsConfig, &report))
	})

	t.Run("untrusted serving cert is an error", func(t *testing.T) {
		srv, _, _ := newServer(t, http.StatusAccepted)

		// An empty pool stands in for a node that has no MCS CA: the handshake
		// must fail rather than fall back to the system trust store.
		err := sendFailureReportHTTP(srv.URL, &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}, &report)
		assert.Error(t, err)
	})
}

func TestGetPoolNameFromMachineConfig(t *testing.T) {
	t.Run("uses the owning pool", func(t *testing.T) {
		mc := &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: "rendered-infra-abc123",
				OwnerReferences: []metav1.OwnerReference{{
					Kind: "MachineConfigPool",
					Name: "infra",
				}},
			},
		}
		assert.Equal(t, "infra", getPoolNameFromMachineConfig(mc))
	})

	t.Run("falls back to master without owner references", func(t *testing.T) {
		mc := &mcfgv1.MachineConfig{ObjectMeta: metav1.ObjectMeta{Name: "rendered-abc123"}}
		assert.Equal(t, ctrlcommon.MachineConfigPoolMaster, getPoolNameFromMachineConfig(mc))
	})
}
