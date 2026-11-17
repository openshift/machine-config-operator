package cache

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/coreos/stream-metadata-go/stream"
	"k8s.io/klog/v2"
)

// newDownloadClient returns an HTTP client for fetching boot image artifacts,
// together with a function that releases the idle connections it opened.
//
// When additionalTrustBundle contains PEM certificates, the client trusts them in
// addition to the system roots, so that downloads keep working behind a
// TLS-inspecting proxy whose CA is only distributed through the cluster's trust
// bundle. Otherwise the client behaves exactly like http.Get. A new client is built
// for every download so that updated or rotated bundles take effect immediately.
func newDownloadClient(additionalTrustBundle []byte) (*http.Client, func(), error) {
	return buildDownloadClient(http.DefaultTransport, x509.SystemCertPool, additionalTrustBundle)
}

// buildDownloadClient implements newDownloadClient on top of the given base round
// tripper and system root loader. The base round tripper is never modified.
func buildDownloadClient(base http.RoundTripper, loadSystemRoots func() (*x509.CertPool, error), additionalTrustBundle []byte) (*http.Client, func(), error) {
	noop := func() {}

	if len(bytes.TrimSpace(additionalTrustBundle)) == 0 {
		return &http.Client{Transport: base}, noop, nil
	}

	if !x509.NewCertPool().AppendCertsFromPEM(additionalTrustBundle) {
		klog.Warningf("The additional trust bundle does not contain any valid PEM certificate; downloading boot images with the default trust roots only")
		return &http.Client{Transport: base}, noop, nil
	}

	baseTransport, ok := base.(*http.Transport)
	if !ok {
		return nil, noop, fmt.Errorf("cannot apply the additional trust bundle: unsupported HTTP transport type %T", base)
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
			return nil, noop, fmt.Errorf("could not load the system root CAs needed to merge the additional trust bundle: %w", err)
		}
		if systemRoots == nil {
			return nil, noop, fmt.Errorf("could not load the system root CAs needed to merge the additional trust bundle: no system root CAs found")
		}
		roots = systemRoots
	}
	roots.AppendCertsFromPEM(additionalTrustBundle)

	tlsConfig.RootCAs = roots
	transport.TLSClientConfig = tlsConfig

	return &http.Client{Transport: transport}, transport.CloseIdleConnections, nil
}

// downloadArtifact fetches the artifact with the given client, verifies its SHA256
// checksum and saves it in destdir, returning the full path of the saved file.
//
// It is adapted from (*stream.Artifact).Download in
// github.com/coreos/stream-metadata-go, which only supports the default HTTP client.
// The integrity guarantees are unchanged: the data is written to a temporary file
// in destdir that is removed on any failure, and only a fully verified and synced
// file is renamed to its final name, overwriting any existing file.
func downloadArtifact(client *http.Client, a *stream.Artifact, destdir string) (string, error) {
	name, err := a.Name()
	if err != nil {
		return "", err
	}
	destfile := filepath.Join(destdir, name)

	w, err := os.CreateTemp(destdir, ".coreos-artifact-")
	if err != nil {
		return "", err
	}
	finalized := false
	defer func() {
		if !finalized {
			// The download already failed; errors while cleaning up are ignored.
			_ = w.Close()
			_ = os.Remove(w.Name())
		}
	}()

	if err := fetchArtifact(client, a, w); err != nil {
		return "", err
	}
	if err := w.Sync(); err != nil {
		return "", err
	}
	if err := w.Chmod(0o644); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(w.Name(), destfile); err != nil {
		return "", err
	}
	finalized = true

	return destfile, nil
}

// fetchArtifact writes the artifact to w and validates its SHA256 checksum. It is
// adapted from (*stream.Artifact).Fetch in github.com/coreos/stream-metadata-go.
func fetchArtifact(client *http.Client, a *stream.Artifact, w io.Writer) error {
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
