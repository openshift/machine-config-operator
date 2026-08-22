package imageutils

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	annotations "github.com/openshift/api/annotations"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreinformersv1 "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

const (
	cacheConfigMapKey = "cache.json"
	configMapMaxBytes = 1024 * 1024 // 1 MiB
)

// ConfigMapCacheSyncer implements CacheSyncer by reading from a ConfigMap
// lister and writing via a kubeclient. An optional filter selects which entries
// to persist, and an optional transformer reduces their content before writing.
type ConfigMapCacheSyncer struct {
	cmLister    corev1listers.ConfigMapNamespaceLister
	kubeclient  clientset.Interface
	namespace   string
	cmName      string
	filter      CacheEntryFilter
	transformer CacheEntryTransformer
	hasSynced   cache.InformerSynced
	lastSaved   string
}

// NewConfigMapCacheSyncer creates a syncer that persists cache entries to the
// named ConfigMap. The filter, if non-nil, selects which entries to include.
// The transformer, if non-nil, is applied to each included entry before writing.
func NewConfigMapCacheSyncer(
	cmInformer coreinformersv1.ConfigMapInformer,
	kubeclient clientset.Interface,
	namespace, cmName string,
	filter CacheEntryFilter,
	transformer CacheEntryTransformer,
) *ConfigMapCacheSyncer {
	return &ConfigMapCacheSyncer{
		cmLister:    cmInformer.Lister().ConfigMaps(namespace),
		kubeclient:  kubeclient,
		namespace:   namespace,
		cmName:      cmName,
		filter:      filter,
		transformer: transformer,
		hasSynced:   cmInformer.Informer().HasSynced,
	}
}

// Start waits for the ConfigMap informer cache to sync, then launches
// the background sync loop.
func (s *ConfigMapCacheSyncer) Start(ctx context.Context, src SyncableCache, debounce time.Duration) {
	if !cache.WaitForCacheSync(ctx.Done(), s.hasSynced) {
		klog.Warning("ConfigMap informer cache sync timed out")
		return
	}
	go s.syncLoop(ctx, src, debounce)
}

func (s *ConfigMapCacheSyncer) syncLoop(ctx context.Context, src SyncableCache, debounce time.Duration) {
	ch := src.SyncNotify()
	for waitForNotify(ctx, ch) {
		if !debounceDrain(ctx, ch, debounce) {
			break
		}
		s.flush(ctx, src)
	}
	s.flush(context.Background(), src)
}

func (s *ConfigMapCacheSyncer) flush(ctx context.Context, src SyncableCache) {
	if err := s.save(ctx, src.Snapshot()); err != nil {
		klog.Warningf("Failed to sync inspection cache to external store: %v", err)
	}
}

// waitForNotify blocks until a sync notification arrives or the context is
// cancelled. Returns true if a notification was received.
func waitForNotify(ctx context.Context, ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

// debounceDrain drains further sync notifications until no new ones arrive
// for the given duration. Returns false if the context was cancelled.
func debounceDrain(ctx context.Context, ch <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ch:
			timer.Reset(d)
		case <-timer.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
}

func (s *ConfigMapCacheSyncer) Load(_ context.Context) (map[string]*InspectionCacheEntry, error) {
	cm, err := s.cmLister.Get(s.cmName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting inspection cache ConfigMap: %w", err)
	}

	raw, ok := cm.Data[cacheConfigMapKey]
	if !ok || raw == "" {
		return nil, nil
	}

	var file inspectionCacheFile
	if err := json.Unmarshal([]byte(raw), &file); err != nil || file.Version != inspectionCacheVersion {
		klog.Warningf("Ignoring inspection cache ConfigMap: invalid content")
		return nil, nil
	}
	return file.Entries, nil
}

func (s *ConfigMapCacheSyncer) save(ctx context.Context, entries map[string]*InspectionCacheEntry) error {
	toSave := make(map[string]*InspectionCacheEntry, len(entries))
	for digest, entry := range entries {
		if s.filter != nil && !s.filter(digest, entry) {
			continue
		}
		if s.transformer != nil {
			entry = s.transformer(digest, entry)
		}
		toSave[digest] = entry
	}

	data, err := json.Marshal(&inspectionCacheFile{
		Version: inspectionCacheVersion,
		Entries: toSave,
	})
	if err != nil {
		return fmt.Errorf("marshalling inspection cache for ConfigMap: %w", err)
	}

	serialized := string(data)
	if serialized == s.lastSaved {
		return nil
	}

	if len(data) > configMapMaxBytes {
		klog.Warningf("Inspection cache too large for ConfigMap (%d bytes), skipping sync", len(data))
		return nil
	}

	cmData := map[string]string{cacheConfigMapKey: string(data)}

	existing, err := s.kubeclient.CoreV1().ConfigMaps(s.namespace).Get(ctx, s.cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.cmName,
				Namespace: s.namespace,
				Annotations: map[string]string{
					annotations.OpenShiftComponent: "Machine Config Operator",
				},
			},
			Data: cmData,
		}
		if _, err := s.kubeclient.CoreV1().ConfigMaps(s.namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating inspection cache ConfigMap: %w", err)
		}
		s.lastSaved = serialized
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting inspection cache ConfigMap: %w", err)
	}

	existing.Data = cmData
	if _, err := s.kubeclient.CoreV1().ConfigMaps(s.namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating inspection cache ConfigMap: %w", err)
	}
	s.lastSaved = serialized
	return nil
}
