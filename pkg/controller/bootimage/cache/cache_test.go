package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/coreos/stream-metadata-go/stream"
)

var testFileCounter atomic.Int64

// newTestArtifact starts an httptest server serving content at a uniquely-named path (so
// concurrent/sequential test cases never collide in the shared /tmp/imagebased/image_cache
// directory DownloadOva always uses) and returns a stream.Artifact pointing at it, along with
// a cleanup func that removes the resulting cached file.
func newTestArtifact(t *testing.T, content []byte) (*stream.Artifact, *httptest.Server, func()) {
	t.Helper()

	n := testFileCounter.Add(1)
	fileName := fmt.Sprintf("mco-cache-test-%d.ova", n)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)

	sum := sha256.Sum256(content)
	artifact := &stream.Artifact{
		Location: srv.URL + "/" + fileName,
		Sha256:   hex.EncodeToString(sum[:]),
	}

	cacheDir, err := getCacheDir()
	if err != nil {
		t.Fatalf("newTestArtifact: failed to resolve cache dir: %v", err)
	}
	cleanup := func() {
		_ = os.Remove(filepath.Join(cacheDir, fileName))
	}
	t.Cleanup(cleanup)

	return artifact, srv, cleanup
}

func TestDownloadOva(t *testing.T) {
	t.Run("fresh download", func(t *testing.T) {
		content := []byte("fake-ova-content-fresh-download")
		artifact, _, _ := newTestArtifact(t, content)

		path, err := DownloadOva(artifact)
		if err != nil {
			t.Fatalf("DownloadOva() unexpected error: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read downloaded file: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("downloaded content = %q, want %q", got, content)
		}
	})

	t.Run("cache hit avoids re-downloading", func(t *testing.T) {
		content := []byte("fake-ova-content-cache-hit")
		artifact, srv, _ := newTestArtifact(t, content)

		firstPath, err := DownloadOva(artifact)
		if err != nil {
			t.Fatalf("DownloadOva() (first call) unexpected error: %v", err)
		}

		// Shut the server down before the second call: if DownloadOva tried to re-fetch instead
		// of serving from cache, it would fail with a connection error.
		srv.Close()

		secondPath, err := DownloadOva(artifact)
		if err != nil {
			t.Fatalf("DownloadOva() (second call) unexpected error: %v", err)
		}
		if secondPath != firstPath {
			t.Errorf("DownloadOva() second call path = %q, want %q (same cached file)", secondPath, firstPath)
		}
		got, err := os.ReadFile(secondPath)
		if err != nil {
			t.Fatalf("failed to read cached file: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("cached content = %q, want %q", got, content)
		}
	})

	t.Run("corrupted cache is re-downloaded", func(t *testing.T) {
		content := []byte("fake-ova-content-corruption-repair")
		artifact, _, cleanup := newTestArtifact(t, content)

		cacheDir, err := getCacheDir()
		if err != nil {
			t.Fatalf("failed to resolve cache dir: %v", err)
		}
		name, err := artifact.Name()
		if err != nil {
			t.Fatalf("failed to compute artifact name: %v", err)
		}
		cachedPath := filepath.Join(cacheDir, name)

		// Pre-populate the cache with content that does NOT match artifact.Sha256.
		if err := os.WriteFile(cachedPath, []byte("stale-corrupted-bytes"), 0o644); err != nil {
			t.Fatalf("failed to seed corrupted cache file: %v", err)
		}
		defer cleanup()

		path, err := DownloadOva(artifact)
		if err != nil {
			t.Fatalf("DownloadOva() unexpected error: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read repaired file: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("repaired content = %q, want %q (freshly re-downloaded)", got, content)
		}
	})

	t.Run("download failure propagates", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		artifact := &stream.Artifact{
			Location: srv.URL + "/unreachable-mco-test.ova",
			Sha256:   "0000000000000000000000000000000000000000000000000000000000000",
		}
		cacheDir, err := getCacheDir()
		if err != nil {
			t.Fatalf("failed to resolve cache dir: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(filepath.Join(cacheDir, "unreachable-mco-test.ova")) })

		if _, err := DownloadOva(artifact); err == nil {
			t.Fatalf("DownloadOva() expected an error for a failing download, got nil")
		}
	})
}
