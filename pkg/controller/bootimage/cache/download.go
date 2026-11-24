package cache

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/coreos/stream-metadata-go/stream"
	"k8s.io/klog/v2"
)

// downloadClient is the HTTP client used for a single boot image download. Close
// releases the idle connections of the transport that the client owns; it never
// closes the connections of a shared transport such as http.DefaultTransport.
type downloadClient struct {
	httpClient *http.Client
	// ownedTransport is the transport cloned for this client, or nil when the
	// client uses the shared base transport.
	ownedTransport *http.Transport
}

var _ io.Closer = (*downloadClient)(nil)

// Close releases the idle connections of the transport owned by the client.
func (c *downloadClient) Close() error {
	if c.ownedTransport != nil {
		c.ownedTransport.CloseIdleConnections()
	}
	return nil
}

// newDownloadClient returns a client for fetching boot image artifacts. Callers
// must close it when the download is done.
//
// When additionalTrustBundle contains PEM certificates, the client trusts them in
// addition to the system roots, so that downloads keep working behind a
// TLS-inspecting proxy whose CA is only distributed through the cluster's trust
// bundle. Otherwise the client behaves exactly like http.Get. A new client is built
// for every download so that updated or rotated bundles take effect immediately.
func newDownloadClient(additionalTrustBundle []byte) (*downloadClient, error) {
	return buildDownloadClient(http.DefaultTransport, x509.SystemCertPool, additionalTrustBundle)
}

// buildDownloadClient implements newDownloadClient on top of the given base round
// tripper and system root loader. The base round tripper is never modified.
func buildDownloadClient(base http.RoundTripper, loadSystemRoots func() (*x509.CertPool, error), additionalTrustBundle []byte) (*downloadClient, error) {
	if len(bytes.TrimSpace(additionalTrustBundle)) == 0 {
		return &downloadClient{httpClient: &http.Client{Transport: base}}, nil
	}

	if !x509.NewCertPool().AppendCertsFromPEM(additionalTrustBundle) {
		klog.Warningf("The additional trust bundle does not contain any valid PEM certificate; downloading boot images with the default trust roots only")
		return &downloadClient{httpClient: &http.Client{Transport: base}}, nil
	}

	baseTransport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("cannot apply the additional trust bundle: unsupported HTTP transport type %T", base)
	}

	// Clone deep-copies TLSClientConfig, so existing TLS settings (and the proxy
	// configuration) are kept without changing the shared base transport. Clone
	// only triggers the base transport's one-time HTTP/2 setup, which net/http
	// otherwise performs on its first request.
	transport := baseTransport.Clone()
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		// TLS 1.2 is the default minimum version of Go TLS clients.
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	var roots *x509.CertPool
	if tlsConfig.RootCAs != nil {
		// Extend the root set the base transport already trusts.
		roots = tlsConfig.RootCAs.Clone()
	} else {
		systemRoots, err := loadSystemRoots()
		if err != nil {
			return nil, fmt.Errorf("could not load the system root CAs needed to merge the additional trust bundle: %w", err)
		}
		if systemRoots == nil {
			return nil, fmt.Errorf("could not load the system root CAs needed to merge the additional trust bundle: no system root CAs found")
		}
		roots = systemRoots
	}
	roots.AppendCertsFromPEM(additionalTrustBundle)

	tlsConfig.RootCAs = roots
	transport.TLSClientConfig = tlsConfig

	return &downloadClient{httpClient: &http.Client{Transport: transport}, ownedTransport: transport}, nil
}

// downloadArtifact fetches the artifact with the given client, verifies its SHA256
// checksum and saves it in destdir, returning the full path of the saved file.
//
// It is adapted from (*stream.Artifact).Download in
// github.com/coreos/stream-metadata-go, which only supports the default HTTP client.
// The integrity guarantees are unchanged: the data is written to a temporary file
// in destdir that is removed on any failure, and only a fully verified and synced
// file is renamed to its final name, overwriting any existing file. On failure the
// original error is returned; problems cleaning up the temporary file are only
// reported through logError.
func downloadArtifact(client *http.Client, a *stream.Artifact, destdir string, logError func(string, ...interface{})) (string, error) {
	name, err := a.Name()
	if err != nil {
		return "", err
	}
	destfile := filepath.Join(destdir, name)

	w, err := os.CreateTemp(destdir, ".coreos-artifact-")
	if err != nil {
		return "", err
	}
	closed, finalized := false, false
	defer func() {
		if !finalized {
			discardTempFile(w, closed, os.Remove, logError)
		}
	}()

	if err := copyArtifactTo(client, a, w); err != nil {
		return "", err
	}
	if err := w.Sync(); err != nil {
		return "", err
	}
	if err := w.Chmod(0o644); err != nil {
		return "", err
	}
	closeErr := w.Close()
	closed = true
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(w.Name(), destfile); err != nil {
		return "", err
	}
	finalized = true

	return destfile, nil
}

// discardTempFile closes the temporary file of a failed download, unless it is
// already closed, and removes it. Cleanup failures are logged with the temporary
// path instead of being returned, so that the caller still gets the original
// download error and the controller status is not affected. A file that no longer
// exists is not a failure.
func discardTempFile(f *os.File, closed bool, remove func(string) error, logError func(string, ...interface{})) {
	if !closed {
		if err := f.Close(); err != nil {
			logError("Failed to close temporary boot image download file %s: %v", f.Name(), err)
		}
	}
	if err := remove(f.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logError("Failed to remove temporary boot image download file %s: %v", f.Name(), err)
	}
}

// copyArtifactTo downloads the artifact with the given client, copies it to w and
// validates its SHA256 checksum. It is adapted from (*stream.Artifact).Fetch in
// github.com/coreos/stream-metadata-go.
func copyArtifactTo(client *http.Client, a *stream.Artifact, w io.Writer) error {
	resp, err := client.Get(a.Location)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned status: %s", a.Location, resp.Status)
	}

	hasher := sha256.New()
	if _, err := io.Copy(w, io.TeeReader(resp.Body, hasher)); err != nil {
		return err
	}

	foundChecksum := hex.EncodeToString(hasher.Sum(nil))
	if a.Sha256 != foundChecksum {
		return fmt.Errorf("checksum mismatch for %s; expected=%s found=%s", a.Location, a.Sha256, foundChecksum)
	}

	return nil
}
