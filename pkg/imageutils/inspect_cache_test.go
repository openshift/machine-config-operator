package imageutils

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileInspectionCache_PutAndGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, nil)

	entry := &InspectionCacheEntry{Labels: map[string]string{"k": "v"}}
	require.NoError(t, cache.Put("sha256:aaa", entry))

	got := cache.Get("sha256:aaa")
	require.NotNil(t, got)
	assert.Equal(t, "v", got.Labels["k"])

	assert.Nil(t, cache.Get("sha256:missing"))
}

func TestFileInspectionCache_PersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")

	c1 := NewFileInspectionCache(path, 0, nil)
	require.NoError(t, c1.Put("sha256:aaa", &InspectionCacheEntry{Labels: map[string]string{"a": "1"}}))
	require.NoError(t, c1.Put("sha256:bbb", &InspectionCacheEntry{Labels: map[string]string{"b": "2"}}))

	c2 := NewFileInspectionCache(path, 0, nil)
	got := c2.Get("sha256:aaa")
	require.NotNil(t, got)
	assert.Equal(t, "1", got.Labels["a"])

	got = c2.Get("sha256:bbb")
	require.NotNil(t, got)
	assert.Equal(t, "2", got.Labels["b"])
}

func TestFileInspectionCache_MissingFile(t *testing.T) {
	cache := NewFileInspectionCache(filepath.Join(t.TempDir(), "does-not-exist.json"), 0, nil)
	assert.Nil(t, cache.Get("sha256:any"))
}

func TestFileInspectionCache_CorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

	cache := NewFileInspectionCache(path, 0, nil)
	assert.Nil(t, cache.Get("sha256:any"))

	require.NoError(t, cache.Put("sha256:aaa", &InspectionCacheEntry{Labels: map[string]string{"k": "v"}}))
	assert.NotNil(t, cache.Get("sha256:aaa"))
}

func TestFileInspectionCache_WrongVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":999,"entries":{"sha256:aaa":{"labels":{"k":"v"}}}}`), 0o644))

	cache := NewFileInspectionCache(path, 0, nil)
	assert.Nil(t, cache.Get("sha256:aaa"))
}

type retainFunc func(digests []string) []string

func (f retainFunc) Retain(digests []string) []string { return f(digests) }

func TestFileInspectionCache_EvictNoEvicters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, nil)
	require.NoError(t, cache.Put("sha256:aaa", &InspectionCacheEntry{Labels: map[string]string{"a": "1"}}))
	require.NoError(t, cache.Put("sha256:bbb", &InspectionCacheEntry{Labels: map[string]string{"b": "2"}}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx, 50*time.Millisecond, 0, time.Hour)

	require.Eventually(t, func() bool {
		return cache.Get("sha256:aaa") == nil && cache.Get("sha256:bbb") == nil
	}, 5*time.Second, 50*time.Millisecond)

	reloaded := NewFileInspectionCache(path, 0, nil)
	assert.Nil(t, reloaded.Get("sha256:aaa"))
}

func TestFileInspectionCache_EvictRetainsUnion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, nil)
	require.NoError(t, cache.Put("sha256:aaa", &InspectionCacheEntry{Labels: map[string]string{"a": "1"}}))
	require.NoError(t, cache.Put("sha256:bbb", &InspectionCacheEntry{Labels: map[string]string{"b": "2"}}))
	require.NoError(t, cache.Put("sha256:ccc", &InspectionCacheEntry{Labels: map[string]string{"c": "3"}}))

	cache.RegisterEvicter(retainFunc(func(digests []string) []string {
		return []string{"sha256:aaa"}
	}))
	cache.RegisterEvicter(retainFunc(func(digests []string) []string {
		return []string{"sha256:bbb"}
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx, 50*time.Millisecond, 0, time.Hour)

	require.Eventually(t, func() bool {
		return cache.Get("sha256:ccc") == nil
	}, 5*time.Second, 50*time.Millisecond)

	assert.NotNil(t, cache.Get("sha256:aaa"))
	assert.NotNil(t, cache.Get("sha256:bbb"))

	reloaded := NewFileInspectionCache(path, 0, nil)
	assert.NotNil(t, reloaded.Get("sha256:aaa"))
	assert.NotNil(t, reloaded.Get("sha256:bbb"))
	assert.Nil(t, reloaded.Get("sha256:ccc"))
}

func TestFileInspectionCache_EvictRespectsMinAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 500*time.Millisecond, nil)
	require.NoError(t, cache.Put("sha256:aaa", &InspectionCacheEntry{Labels: map[string]string{"a": "1"}}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx, 50*time.Millisecond, 0, time.Hour)

	require.Never(t, func() bool {
		return cache.Get("sha256:aaa") == nil
	}, 400*time.Millisecond, 50*time.Millisecond)

	require.Eventually(t, func() bool {
		return cache.Get("sha256:aaa") == nil
	}, 5*time.Second, 50*time.Millisecond)
}

func TestFileInspectionCache_PutMergesFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, nil)

	require.NoError(t, cache.Put("sha256:aaa", &InspectionCacheEntry{Labels: map[string]string{"k": "v"}}))
	require.NoError(t, cache.Put("sha256:aaa", &InspectionCacheEntry{Files: map[string][]byte{"/etc/config": []byte("data")}}))

	got := cache.Get("sha256:aaa")
	require.NotNil(t, got)
	assert.Equal(t, "v", got.Labels["k"])
	assert.Equal(t, []byte("data"), got.Files["/etc/config"])

	reloaded := NewFileInspectionCache(path, 0, nil)
	got = reloaded.Get("sha256:aaa")
	require.NotNil(t, got)
	assert.Equal(t, "v", got.Labels["k"])
	assert.Equal(t, []byte("data"), got.Files["/etc/config"])
}

type mockSyncer struct {
	loadEntries map[string]*InspectionCacheEntry
	loadErr     error

	mu        sync.Mutex
	saved     map[string]*InspectionCacheEntry
	saveCount int
}

func (m *mockSyncer) Load(_ context.Context) (map[string]*InspectionCacheEntry, error) {
	return m.loadEntries, m.loadErr
}

func (m *mockSyncer) record(snapshot map[string]*InspectionCacheEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = snapshot
	m.saveCount++
}

func (m *mockSyncer) state() (map[string]*InspectionCacheEntry, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved, m.saveCount
}

func (m *mockSyncer) Start(ctx context.Context, src SyncableCache, debounce time.Duration) {
	ch := src.SyncNotify()
	go func() {
		for waitForNotify(ctx, ch) {
			if !debounceDrain(ctx, ch, debounce) {
				break
			}
			m.record(src.Snapshot())
		}
		m.record(src.Snapshot())
	}()
}

// Verifies that entries from the syncer are loaded into memory on Start
// and persisted to the local cache file for subsequent instances.
func TestFileInspectionCache_StartLoadsFromSyncer(t *testing.T) {
	syncer := &mockSyncer{
		loadEntries: map[string]*InspectionCacheEntry{
			"sha256:persisted": {Labels: map[string]string{"from": "configmap"}},
		},
	}

	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, syncer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx, time.Hour, time.Hour, time.Hour)

	got := cache.Get("sha256:persisted")
	require.NotNil(t, got)
	assert.Equal(t, "configmap", got.Labels["from"])

	reloaded := NewFileInspectionCache(path, 0, nil)
	got = reloaded.Get("sha256:persisted")
	require.NotNil(t, got, "syncer entries must be persisted to the local cache file")
	assert.Equal(t, "configmap", got.Labels["from"])
}

// Verifies that a Put triggers a sync flush to the external store.
func TestFileInspectionCache_StartSyncFlushesAfterPut(t *testing.T) {
	syncer := &mockSyncer{}

	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, syncer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx, time.Hour, time.Hour, 50*time.Millisecond)

	require.NoError(t, cache.Put("sha256:new", &InspectionCacheEntry{Labels: map[string]string{"k": "v"}}))

	require.Eventually(t, func() bool {
		_, count := syncer.state()
		return count > 0
	}, 5*time.Second, 50*time.Millisecond)

	saved, _ := syncer.state()
	assert.Contains(t, saved, "sha256:new")
}

// Verifies that the syncer is not called when no mutations occur.
func TestFileInspectionCache_StartSyncNoFlushWithoutChanges(t *testing.T) {
	syncer := &mockSyncer{}

	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, syncer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx, time.Hour, time.Hour, 50*time.Millisecond)

	require.Never(t, func() bool {
		_, count := syncer.state()
		return count > 0
	}, time.Second, 50*time.Millisecond)
}

// Verifies that pending entries are flushed to the syncer on shutdown,
// even when the debounce interval has not elapsed.
func TestFileInspectionCache_StartSyncFlushOnShutdown(t *testing.T) {
	syncer := &mockSyncer{}

	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, syncer)

	ctx, cancel := context.WithCancel(context.Background())
	// Debounce set to time.Hour so the only trigger is the context cancellation.
	cache.Start(ctx, time.Hour, time.Hour, time.Hour)

	require.NoError(t, cache.Put("sha256:flushed", &InspectionCacheEntry{Labels: map[string]string{"k": "v"}}))
	cancel()

	require.Eventually(t, func() bool {
		_, count := syncer.state()
		return count > 0
	}, 5*time.Second, 50*time.Millisecond)

	saved, _ := syncer.state()
	assert.Contains(t, saved, "sha256:flushed")
}

// Verifies that eviction triggers a sync notification so the syncer
// flushes the updated state to external storage.
func TestFileInspectionCache_StartSyncEvictionNotifies(t *testing.T) {
	syncer := &mockSyncer{}

	path := filepath.Join(t.TempDir(), "cache.json")
	cache := NewFileInspectionCache(path, 0, syncer)

	require.NoError(t, cache.Put("sha256:evictme", &InspectionCacheEntry{Labels: map[string]string{"k": "v"}}))

	// Drain the notification from the Put above.
	select {
	case <-cache.syncNotify:
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cache.Start(ctx, 50*time.Millisecond, 0, 50*time.Millisecond)

	require.Eventually(t, func() bool {
		_, count := syncer.state()
		return count > 0
	}, 5*time.Second, 50*time.Millisecond)
}

func TestDigestFromPullspec(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "quay.io/openshift/os@sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			want:  "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		},
		{
			input: "registry.example.com/repo/image@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			want:  "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{
			input: "quay.io/openshift/os:latest",
			want:  "",
		},
		{
			input: "not a valid ref @@@",
			want:  "",
		},
		{
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, DigestFromPullspec(tt.input))
		})
	}
}
