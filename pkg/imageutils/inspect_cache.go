package imageutils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/renameio"
	"k8s.io/klog/v2"
)

const inspectionCacheVersion = 1

// InspectionCacheEntry holds cached inspection data for a single image.
type InspectionCacheEntry struct {
	Labels    map[string]string `json:"labels,omitempty"`
	Files     map[string][]byte `json:"files,omitempty"`
	CreatedAt time.Time         `json:"createdAt,omitempty"`
}

func (e *InspectionCacheEntry) deepCopy() *InspectionCacheEntry {
	cp := &InspectionCacheEntry{
		CreatedAt: e.CreatedAt,
		Labels:    maps.Clone(e.Labels),
	}
	if e.Files != nil {
		cp.Files = make(map[string][]byte, len(e.Files))
		for k, v := range e.Files {
			cp.Files[k] = bytes.Clone(v)
		}
	}
	return cp
}

// InspectionCache provides access to cached image inspection results keyed by digest.
type InspectionCache interface {
	// Get returns the cached entry for the given digest, or nil if not found.
	Get(digest string) *InspectionCacheEntry
	// Put stores or merges an entry for the given digest.
	Put(digest string, entry *InspectionCacheEntry) error
}

// CacheEvicter determines which cached digests should be retained.
// Retain receives all digests currently in the cache and returns the subset
// that this evicter wants to keep. An entry is purged only if no registered
// evicter includes it in its retain list.
type CacheEvicter interface {
	Retain(digests []string) []string
}

type inspectionCacheFile struct {
	Version int                              `json:"version"`
	Entries map[string]*InspectionCacheEntry `json:"entries"`
}

// FileInspectionCache is a file-backed InspectionCache.
// It keeps an in-memory copy for fast reads and writes through to a JSON file on every Put.
type FileInspectionCache struct {
	path     string
	mu       sync.RWMutex
	entries  map[string]*InspectionCacheEntry
	evicters []CacheEvicter
	minAge   time.Duration
}

// NewFileInspectionCache creates a cache backed by the given file path.
// If the file exists it is loaded; a missing or corrupt file starts an empty cache.
// minAge is the minimum time an entry must live before it can be evicted.
func NewFileInspectionCache(path string, minAge time.Duration) *FileInspectionCache {
	c := &FileInspectionCache{
		path:    path,
		entries: make(map[string]*InspectionCacheEntry),
		minAge:  minAge,
	}
	c.load()
	return c
}

func (c *FileInspectionCache) Get(digest string) *InspectionCacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e := c.entries[digest]
	if e == nil {
		return nil
	}
	return e.deepCopy()
}

func (c *FileInspectionCache) Put(digest string, entry *InspectionCacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[digest]; ok {
		if entry.Labels != nil {
			existing.Labels = maps.Clone(entry.Labels)
		}
		for k, v := range entry.Files {
			if existing.Files == nil {
				existing.Files = map[string][]byte{}
			}
			existing.Files[k] = bytes.Clone(v)
		}
	} else {
		cp := entry.deepCopy()
		cp.CreatedAt = time.Now()
		c.entries[digest] = cp
	}
	return c.saveLocked()
}

func (c *FileInspectionCache) load() {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var file inspectionCacheFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != inspectionCacheVersion {
		return
	}
	if file.Entries != nil {
		c.entries = file.Entries
	}
}

func (c *FileInspectionCache) saveLocked() error {
	data, err := json.Marshal(&inspectionCacheFile{
		Version: inspectionCacheVersion,
		Entries: c.entries,
	})
	if err != nil {
		return fmt.Errorf("marshalling inspection cache: %w", err)
	}

	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating inspection cache directory: %w", err)
	}

	t, err := renameio.TempFile(dir, c.path)
	if err != nil {
		return fmt.Errorf("creating temp file for inspection cache: %w", err)
	}
	defer t.Cleanup()

	if _, err := t.Write(data); err != nil {
		return fmt.Errorf("writing inspection cache: %w", err)
	}
	return t.CloseAtomicallyReplace()
}

// RegisterEvicter adds an evicter that will be consulted during eviction
// to determine which entries should be retained.
func (c *FileInspectionCache) RegisterEvicter(e CacheEvicter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evicters = append(c.evicters, e)
}

// StartEviction runs a background goroutine that periodically evicts entries
// older than minAge that no registered CacheEvicter wants to retain.
func (c *FileInspectionCache) StartEviction(ctx context.Context, interval, initialDelay time.Duration) {
	go func() {
		select {
		case <-time.After(initialDelay):
		case <-ctx.Done():
			return
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		c.evict()
		for {
			select {
			case <-ticker.C:
				c.evict()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (c *FileInspectionCache) evict() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) == 0 {
		return
	}

	now := time.Now()
	candidates := make([]string, 0, len(c.entries))
	for d, entry := range c.entries {
		if now.Sub(entry.CreatedAt) >= c.minAge {
			candidates = append(candidates, d)
		}
	}

	if len(candidates) == 0 {
		return
	}

	retained := make(map[string]bool)
	for _, e := range c.evicters {
		for _, d := range e.Retain(candidates) {
			retained[d] = true
		}
	}

	evicted := 0
	for _, d := range candidates {
		if !retained[d] {
			delete(c.entries, d)
			evicted++
		}
	}

	if evicted > 0 {
		klog.Infof("Inspection cache eviction: removed %d entries, %d retained", evicted, len(c.entries))
		if err := c.saveLocked(); err != nil {
			klog.Warningf("Failed to persist inspection cache after eviction: %v", err)
		}
	}
}
