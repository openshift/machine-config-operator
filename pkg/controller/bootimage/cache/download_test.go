package cache

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/stream-metadata-go/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testArtifactName = "rhcos-vmware.x86_64.ova"

var (
	testArtifactContent = []byte("not really an OVA, but good enough for a checksum")
	loopbackIPs         = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
)

// testCA is an independently generated certificate authority used to sign test
// server certificates.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, commonName string) *testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          newSerial(t),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return &testCA{
		cert: cert,
		key:  key,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// newTLSServer starts an HTTPS server whose certificate is signed by the CA and
// valid for the given DNS names and IP addresses.
func (ca *testCA) newTLSServer(t *testing.T, handler http.Handler, dnsNames []string, ips []net.IP) *httptest.Server {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: newSerial(t),
		Subject:      pkix.Name{CommonName: "boot image test server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// newLoopbackServer starts an HTTPS server, signed by the CA, that is valid for the
// loopback address it listens on and serves testArtifactContent.
func (ca *testCA) newLoopbackServer(t *testing.T) *httptest.Server {
	t.Helper()
	return ca.newTLSServer(t, artifactHandler(testArtifactContent), nil, loopbackIPs)
}

func newSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	require.NoError(t, err)
	return serial
}

func artifactHandler(content []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newTestArtifact(srv *httptest.Server, content []byte) *stream.Artifact {
	return &stream.Artifact{
		Location: srv.URL + "/" + testArtifactName,
		Sha256:   sha256Hex(content),
	}
}

// downloadWithBundle builds a client for the bundle, exactly as cacheFile does, and
// downloads the artifact into a fresh directory.
func downloadWithBundle(t *testing.T, a *stream.Artifact, bundle []byte) (string, string, error) {
	t.Helper()

	client, closeIdleConnections, err := newDownloadClient(bundle)
	require.NoError(t, err)
	defer closeIdleConnections()

	dir := t.TempDir()
	path, err := downloadArtifact(client, a, dir, t.Logf)
	return dir, path, err
}

// logRecorder collects formatted log messages for a single test.
type logRecorder struct {
	messages []string
}

func (r *logRecorder) errorf(format string, args ...interface{}) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

// assertNoTempFiles verifies that no temporary download file remains in dir.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()

	temps, err := filepath.Glob(filepath.Join(dir, ".coreos-artifact-*"))
	require.NoError(t, err)
	assert.Empty(t, temps, "temporary download files must not be left behind")
}

// assertNoDownloadLeftovers verifies that neither the final file nor a temporary
// download file remains in dir.
func assertNoDownloadLeftovers(t *testing.T, dir string) {
	t.Helper()

	assert.NoFileExists(t, filepath.Join(dir, testArtifactName))
	assertNoTempFiles(t, dir)
}

func requireUnknownAuthority(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var unknownAuthority x509.UnknownAuthorityError
	require.True(t, errors.As(err, &unknownAuthority), "expected an unknown authority error, got: %v", err)
}

func TestDownloadArtifactTrustsAdditionalTrustBundle(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	srv := ca.newLoopbackServer(t)

	dir, path, err := downloadWithBundle(t, newTestArtifact(srv, testArtifactContent), ca.pem)
	require.NoError(t, err)

	assert.Equal(t, filepath.Join(dir, testArtifactName), path)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, testArtifactContent, content)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	assertNoTempFiles(t, dir)
}

func TestDownloadArtifactRejectsUntrustedCA(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	srv := ca.newLoopbackServer(t)
	artifact := newTestArtifact(srv, testArtifactContent)

	t.Run("no additional trust bundle", func(t *testing.T) {
		dir, _, err := downloadWithBundle(t, artifact, nil)
		requireUnknownAuthority(t, err)
		assertNoDownloadLeftovers(t, dir)
	})

	t.Run("additional trust bundle without valid certificates", func(t *testing.T) {
		dir, _, err := downloadWithBundle(t, artifact, []byte("-----BEGIN CERTIFICATE-----\nbm90IGEgY2VydA==\n-----END CERTIFICATE-----\n"))
		requireUnknownAuthority(t, err)
		assertNoDownloadLeftovers(t, dir)
	})

	t.Run("additional trust bundle for a different CA", func(t *testing.T) {
		otherCA := newTestCA(t, "unrelated test CA")
		dir, _, err := downloadWithBundle(t, artifact, otherCA.pem)
		requireUnknownAuthority(t, err)
		assertNoDownloadLeftovers(t, dir)
	})
}

// TestDownloadArtifactTrustRotation verifies that trust follows the bundle passed for
// each download: after rotating from one CA to an independent one, servers signed by
// the old CA are no longer trusted.
func TestDownloadArtifactTrustRotation(t *testing.T) {
	oldCA := newTestCA(t, "old test CA")
	newCA := newTestCA(t, "new test CA")
	oldSrv := oldCA.newLoopbackServer(t)
	newSrv := newCA.newLoopbackServer(t)
	oldArtifact := newTestArtifact(oldSrv, testArtifactContent)
	newArtifact := newTestArtifact(newSrv, testArtifactContent)

	// Before rotation: only the old CA is trusted.
	_, _, err := downloadWithBundle(t, oldArtifact, oldCA.pem)
	require.NoError(t, err)
	dir, _, err := downloadWithBundle(t, newArtifact, oldCA.pem)
	requireUnknownAuthority(t, err)
	assertNoDownloadLeftovers(t, dir)

	// After rotation: the new CA is trusted and the old one is not.
	_, _, err = downloadWithBundle(t, newArtifact, newCA.pem)
	require.NoError(t, err)
	dir, _, err = downloadWithBundle(t, oldArtifact, newCA.pem)
	requireUnknownAuthority(t, err)
	assertNoDownloadLeftovers(t, dir)

	// During an overlap both CAs can be trusted at once.
	overlap := append(append([]byte{}, oldCA.pem...), newCA.pem...)
	_, _, err = downloadWithBundle(t, oldArtifact, overlap)
	require.NoError(t, err)
	_, _, err = downloadWithBundle(t, newArtifact, overlap)
	require.NoError(t, err)
}

func TestDownloadArtifactVerifiesHostname(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	// The certificate is signed by a trusted CA but is not valid for the loopback
	// address the client connects to.
	srv := ca.newTLSServer(t, artifactHandler(testArtifactContent), []string{"mirror.example.invalid"}, nil)

	dir, _, err := downloadWithBundle(t, newTestArtifact(srv, testArtifactContent), ca.pem)
	require.Error(t, err)
	var hostnameErr x509.HostnameError
	require.True(t, errors.As(err, &hostnameErr), "expected a hostname error, got: %v", err)
	assertNoDownloadLeftovers(t, dir)
}

// newConnectProxy starts an HTTP proxy that tunnels CONNECT requests and counts them.
func newConnectProxy(t *testing.T) (*url.URL, *atomic.Int32) {
	t.Helper()

	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
			return
		}
		connects.Add(1)

		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer upstream.Close()

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijacking not supported", http.StatusInternalServerError)
			return
		}
		client, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer client.Close()

		if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			return
		}
		go func() {
			_, _ = io.Copy(upstream, client)
			upstream.Close()
		}()
		_, _ = io.Copy(client, upstream)
	}))
	t.Cleanup(proxy.Close)

	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	return proxyURL, &connects
}

// TestDownloadArtifactThroughProxy verifies that downloads tunnelled through an HTTP
// proxy keep using the proxy and verify the server against the merged trust roots.
func TestDownloadArtifactThroughProxy(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	srv := ca.newLoopbackServer(t)
	artifact := newTestArtifact(srv, testArtifactContent)
	systemRoots := func() (*x509.CertPool, error) { return x509.NewCertPool(), nil }

	download := func(t *testing.T, bundle []byte) (*atomic.Int32, string, error) {
		t.Helper()
		proxyURL, connects := newConnectProxy(t)
		base := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		client, closeIdleConnections, err := buildDownloadClient(base, systemRoots, bundle)
		require.NoError(t, err)
		defer closeIdleConnections()

		dir := t.TempDir()
		_, err = downloadArtifact(client, artifact, dir, t.Logf)
		return connects, dir, err
	}

	t.Run("trusted through the proxy", func(t *testing.T) {
		connects, _, err := download(t, ca.pem)
		require.NoError(t, err)
		assert.Positive(t, connects.Load(), "the download must go through the proxy")
	})

	t.Run("untrusted through the proxy", func(t *testing.T) {
		connects, dir, err := download(t, nil)
		requireUnknownAuthority(t, err)
		assert.Positive(t, connects.Load(), "the download must go through the proxy")
		assertNoDownloadLeftovers(t, dir)
	})
}

func TestDownloadArtifactChecksumMismatch(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	srv := ca.newLoopbackServer(t)
	artifact := newTestArtifact(srv, []byte("different content"))

	dir, _, err := downloadWithBundle(t, artifact, ca.pem)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
	assertNoDownloadLeftovers(t, dir)
}

func TestDownloadArtifactNonOKStatus(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	srv := ca.newTLSServer(t, http.NotFoundHandler(), nil, loopbackIPs)

	dir, _, err := downloadWithBundle(t, newTestArtifact(srv, testArtifactContent), ca.pem)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned status: 404")
	assertNoDownloadLeftovers(t, dir)
}

func TestDownloadArtifactOverwritesExistingFile(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	srv := ca.newLoopbackServer(t)

	client, closeIdleConnections, err := newDownloadClient(ca.pem)
	require.NoError(t, err)
	defer closeIdleConnections()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, testArtifactName), []byte("stale"), 0o644))

	path, err := downloadArtifact(client, newTestArtifact(srv, testArtifactContent), dir, t.Logf)
	require.NoError(t, err)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, testArtifactContent, content)
}

func TestCacheFileUsesAdditionalTrustBundle(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	srv := ca.newLoopbackServer(t)
	artifact := newTestArtifact(srv, testArtifactContent)

	t.Run("trusted", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, testArtifactName)

		path, err := cacheFile(artifact, filePath, dir, ca.pem)
		require.NoError(t, err)
		assert.Equal(t, filePath, path)
		assert.NoFileExists(t, filePath+".lock")
	})

	t.Run("untrusted", func(t *testing.T) {
		dir := t.TempDir()
		filePath := filepath.Join(dir, testArtifactName)

		_, err := cacheFile(artifact, filePath, dir, nil)
		requireUnknownAuthority(t, err)
		assertNoDownloadLeftovers(t, dir)
		assert.NoFileExists(t, filePath+".lock")
	})
}

// TestDownloadArtifactReturnsOriginalErrorWhenCleanupFails verifies that a failure to
// remove the temporary file neither replaces nor wraps the download error.
func TestDownloadArtifactReturnsOriginalErrorWhenCleanupFails(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	dir := t.TempDir()
	// Registered after t.TempDir, so it runs first and the directory can be removed.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// Privileged processes (root or CAP_DAC_OVERRIDE) can still modify a read-only
	// directory, so removal would not fail for them.
	require.NoError(t, os.Chmod(dir, 0o555))
	if probe, err := os.CreateTemp(dir, "probe-"); err == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Skip("directory permissions do not prevent file removal for this process")
	}
	require.NoError(t, os.Chmod(dir, 0o755))

	// The temporary file already exists when the request is served. Making the
	// directory read-only then makes removing it fail once the download fails.
	srv := ca.newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Errorf("failed to make %s read-only: %v", dir, err)
		}
		http.NotFound(w, r)
	}), nil, loopbackIPs)

	client, closeIdleConnections, err := newDownloadClient(ca.pem)
	require.NoError(t, err)
	defer closeIdleConnections()

	artifact := newTestArtifact(srv, testArtifactContent)
	logs := &logRecorder{}
	_, err = downloadArtifact(client, artifact, dir, logs.errorf)
	require.EqualError(t, err, fmt.Sprintf("%s returned status: 404 Not Found", artifact.Location))

	temps, globErr := filepath.Glob(filepath.Join(dir, ".coreos-artifact-*"))
	require.NoError(t, globErr)
	require.Len(t, temps, 1, "the temporary file must remain because its removal failed")
	require.Len(t, logs.messages, 1)
	assert.Contains(t, logs.messages[0], "Failed to remove temporary boot image download file "+temps[0])
}

// TestDownloadArtifactFailureAfterCloseIsNotNoisy verifies that when the download
// fails after the temporary file was closed (here, because the final path is a
// non-empty directory), the file is removed without a second close being reported
// and the original error is returned.
func TestDownloadArtifactFailureAfterCloseIsNotNoisy(t *testing.T) {
	ca := newTestCA(t, "boot image test CA")
	srv := ca.newLoopbackServer(t)

	client, closeIdleConnections, err := newDownloadClient(ca.pem)
	require.NoError(t, err)
	defer closeIdleConnections()

	dir := t.TempDir()
	blocker := filepath.Join(dir, testArtifactName)
	require.NoError(t, os.Mkdir(blocker, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0o644))

	logs := &logRecorder{}
	_, err = downloadArtifact(client, newTestArtifact(srv, testArtifactContent), dir, logs.errorf)
	var linkErr *os.LinkError
	require.ErrorAs(t, err, &linkErr, "the rename error must be returned unchanged")
	assert.Empty(t, logs.messages, "cleanup must not report the expected second close")
	assertNoTempFiles(t, dir)
}

func TestDiscardTempFile(t *testing.T) {
	newTempFile := func(t *testing.T) *os.File {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), ".coreos-artifact-")
		require.NoError(t, err)
		return f
	}

	tests := []struct {
		name string
		// prepare returns whether the file is already closed and the remove function to use.
		prepare      func(t *testing.T, f *os.File) (bool, func(string) error)
		expectLogs   []string
		expectExists bool
	}{
		{
			name: "open file is closed and removed",
			prepare: func(_ *testing.T, _ *os.File) (bool, func(string) error) {
				return false, os.Remove
			},
		},
		{
			name: "already closed file is not closed again",
			prepare: func(t *testing.T, f *os.File) (bool, func(string) error) {
				require.NoError(t, f.Close())
				return true, os.Remove
			},
		},
		{
			name: "unexpected close failure is logged",
			prepare: func(t *testing.T, f *os.File) (bool, func(string) error) {
				require.NoError(t, f.Close())
				return false, os.Remove
			},
			expectLogs: []string{"Failed to close temporary boot image download file"},
		},
		{
			name: "file that no longer exists is not reported",
			prepare: func(t *testing.T, f *os.File) (bool, func(string) error) {
				require.NoError(t, os.Remove(f.Name()))
				return false, os.Remove
			},
		},
		{
			name: "removal failure is logged",
			prepare: func(_ *testing.T, f *os.File) (bool, func(string) error) {
				return false, func(name string) error {
					return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrPermission}
				}
			},
			expectLogs:   []string{"Failed to remove temporary boot image download file"},
			expectExists: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newTempFile(t)
			closed, remove := tc.prepare(t, f)

			logs := &logRecorder{}
			discardTempFile(f, closed, remove, logs.errorf)

			require.Len(t, logs.messages, len(tc.expectLogs))
			for i, want := range tc.expectLogs {
				assert.Contains(t, logs.messages[i], want)
				assert.Contains(t, logs.messages[i], f.Name(), "the log must name the temporary file")
			}
			assert.ErrorIs(t, f.Close(), os.ErrClosed, "the file must be closed")
			if tc.expectExists {
				assert.FileExists(t, f.Name())
			} else {
				assert.NoFileExists(t, f.Name())
			}
		})
	}
}

// fakeRoundTripper is an http.RoundTripper that is not an *http.Transport.
type fakeRoundTripper struct{}

func (fakeRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func certPoolWith(t *testing.T, pems ...[]byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	for _, p := range pems {
		require.True(t, pool.AppendCertsFromPEM(p))
	}
	return pool
}

func failIfCalled(t *testing.T) func() (*x509.CertPool, error) {
	return func() (*x509.CertPool, error) {
		t.Fatal("system roots must not be loaded")
		return nil, nil
	}
}

func TestBuildDownloadClient(t *testing.T) {
	systemCA := newTestCA(t, "stand-in system root")
	bundleCA := newTestCA(t, "additional trust bundle CA")
	systemRoots := func() (*x509.CertPool, error) { return certPoolWith(t, systemCA.pem), nil }

	t.Run("empty or whitespace bundle keeps the base transport", func(t *testing.T) {
		for _, bundle := range [][]byte{nil, {}, []byte(" \n\t ")} {
			base := &http.Transport{}
			client, cleanup, err := buildDownloadClient(base, failIfCalled(t), bundle)
			require.NoError(t, err)
			cleanup()
			assert.Same(t, base, client.Transport)
			assert.Nil(t, base.TLSClientConfig)
		}
	})

	t.Run("bundle without valid certificates keeps the base transport", func(t *testing.T) {
		base := &http.Transport{}
		client, _, err := buildDownloadClient(base, failIfCalled(t), []byte("garbage"))
		require.NoError(t, err)
		assert.Same(t, base, client.Transport)
	})

	t.Run("bundle is merged with the system roots", func(t *testing.T) {
		base := &http.Transport{}
		client, cleanup, err := buildDownloadClient(base, systemRoots, bundleCA.pem)
		require.NoError(t, err)
		defer cleanup()

		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		assert.NotSame(t, base, transport)
		require.NotNil(t, transport.TLSClientConfig)
		assert.True(t, transport.TLSClientConfig.RootCAs.Equal(certPoolWith(t, systemCA.pem, bundleCA.pem)))
		assert.False(t, transport.TLSClientConfig.InsecureSkipVerify)
		assert.Empty(t, transport.TLSClientConfig.ServerName)
		// Clone may run the base's one-time HTTP/2 setup, but its trust must not change.
		if base.TLSClientConfig != nil {
			assert.NotSame(t, base.TLSClientConfig, transport.TLSClientConfig)
			assert.Nil(t, base.TLSClientConfig.RootCAs, "the base transport trust must not be modified")
		}
	})

	t.Run("missing TLS configuration defaults to TLS 1.2 minimum", func(t *testing.T) {
		// A custom dialer disables the HTTP/2 setup, so the clone has no TLS configuration.
		base := &http.Transport{DialContext: (&net.Dialer{}).DialContext}
		client, cleanup, err := buildDownloadClient(base, systemRoots, bundleCA.pem)
		require.NoError(t, err)
		defer cleanup()

		tlsConfig := client.Transport.(*http.Transport).TLSClientConfig
		require.NotNil(t, tlsConfig)
		assert.Equal(t, uint16(tls.VersionTLS12), tlsConfig.MinVersion)
		assert.True(t, tlsConfig.RootCAs.Equal(certPoolWith(t, systemCA.pem, bundleCA.pem)))
		assert.Nil(t, base.TLSClientConfig, "the base transport must not be modified")
	})

	t.Run("system root loading failure fails instead of dropping system trust", func(t *testing.T) {
		loadErr := errors.New("no system roots")
		client, _, err := buildDownloadClient(&http.Transport{}, func() (*x509.CertPool, error) { return nil, loadErr }, bundleCA.pem)
		require.ErrorIs(t, err, loadErr)
		assert.Nil(t, client)

		client, _, err = buildDownloadClient(&http.Transport{}, func() (*x509.CertPool, error) { return nil, nil }, bundleCA.pem)
		require.Error(t, err)
		assert.Nil(t, client)
	})

	t.Run("existing TLS configuration and roots are preserved", func(t *testing.T) {
		existingRoots := certPoolWith(t, systemCA.pem)
		base := &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
				RootCAs:    existingRoots,
				NextProtos: []string{"http/1.1"},
			},
		}
		client, cleanup, err := buildDownloadClient(base, failIfCalled(t), bundleCA.pem)
		require.NoError(t, err)
		defer cleanup()

		transport := client.Transport.(*http.Transport)
		tlsConfig := transport.TLSClientConfig
		assert.NotSame(t, base.TLSClientConfig, tlsConfig)
		assert.Equal(t, uint16(tls.VersionTLS13), tlsConfig.MinVersion)
		assert.Equal(t, []string{"http/1.1"}, tlsConfig.NextProtos)
		assert.True(t, tlsConfig.RootCAs.Equal(certPoolWith(t, systemCA.pem, bundleCA.pem)))
		assert.Same(t, existingRoots, base.TLSClientConfig.RootCAs)
		assert.True(t, existingRoots.Equal(certPoolWith(t, systemCA.pem)), "the base root pool must not be modified")
	})

	t.Run("proxy configuration is preserved", func(t *testing.T) {
		proxyURL := &url.URL{Scheme: "http", Host: "proxy.example.invalid:3128"}
		base := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		client, cleanup, err := buildDownloadClient(base, systemRoots, bundleCA.pem)
		require.NoError(t, err)
		defer cleanup()

		transport := client.Transport.(*http.Transport)
		require.NotNil(t, transport.Proxy)
		req, err := http.NewRequest(http.MethodGet, "https://mirror.example.invalid/rhcos.ova", nil)
		require.NoError(t, err)
		got, err := transport.Proxy(req)
		require.NoError(t, err)
		assert.Equal(t, proxyURL, got)
	})

	t.Run("unsupported transport type", func(t *testing.T) {
		client, _, err := buildDownloadClient(fakeRoundTripper{}, systemRoots, bundleCA.pem)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported HTTP transport type")
		assert.Nil(t, client)

		// Without a bundle the transport is used unchanged, as before.
		client, _, err = buildDownloadClient(fakeRoundTripper{}, systemRoots, nil)
		require.NoError(t, err)
		assert.Equal(t, fakeRoundTripper{}, client.Transport)
	})
}

func TestNewDownloadClientDoesNotModifyDefaultTransport(t *testing.T) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)

	client, cleanup, err := newDownloadClient(newTestCA(t, "boot image test CA").pem)
	require.NoError(t, err)
	defer cleanup()

	assert.Same(t, defaultTransport, http.DefaultTransport)
	assert.NotSame(t, http.DefaultClient, client)
	if defaultTransport.TLSClientConfig != nil {
		assert.Nil(t, defaultTransport.TLSClientConfig.RootCAs, "the default transport trust must not be modified")
		assert.False(t, defaultTransport.TLSClientConfig.InsecureSkipVerify)
	}

	transport := client.Transport.(*http.Transport)
	assert.NotSame(t, defaultTransport, transport)
	assert.Equal(t, reflect.ValueOf(http.ProxyFromEnvironment).Pointer(), reflect.ValueOf(transport.Proxy).Pointer(),
		"the proxy environment configuration must be preserved")
}
