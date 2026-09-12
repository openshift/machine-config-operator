package imageutils

// CacheEntryFilter decides whether a cache entry should be included in
// external persistence. Implementations must not mutate the input entry.
type CacheEntryFilter func(digest string, entry *InspectionCacheEntry) bool

// CacheEntryTransformer returns a (possibly reduced) copy of an
// InspectionCacheEntry for external persistence. Implementations must not
// mutate the input entry.
type CacheEntryTransformer func(digest string, entry *InspectionCacheEntry) *InspectionCacheEntry

// NewCacheFileTransformer returns a CacheEntryTransformer that applies a
// transformation function to a cached file matching the given path. Other
// files and labels are preserved.
func NewCacheFileTransformer(path string, transform func([]byte) ([]byte, error)) CacheEntryTransformer {
	return func(_ string, entry *InspectionCacheEntry) *InspectionCacheEntry {
		if entry.Files == nil {
			return entry
		}
		_, ok := entry.Files[path]
		if !ok {
			return entry
		}

		cp := entry.DeepCopy()
		transformed, err := transform(cp.Files[path])
		if err != nil {
			return entry
		}

		cp.Files[path] = transformed
		return cp
	}
}

// NewCacheEntryFilter returns a filter that accepts entries having at least
// one of the specified label keys.
func NewCacheEntryFilter(requiredLabelKeys ...string) CacheEntryFilter {
	keys := make(map[string]struct{}, len(requiredLabelKeys))
	for _, k := range requiredLabelKeys {
		keys[k] = struct{}{}
	}
	return func(_ string, entry *InspectionCacheEntry) bool {
		for k := range entry.Labels {
			if _, ok := keys[k]; ok {
				return true
			}
		}
		return false
	}
}
