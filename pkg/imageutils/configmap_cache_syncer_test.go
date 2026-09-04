package imageutils

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

const testNamespace = "openshift-machine-config-operator"
const testCMName = "test-inspection-cache"

func newFakeSyncer(t *testing.T, configMaps ...*corev1.ConfigMap) (*ConfigMapCacheSyncer, *fake.Clientset) {
	t.Helper()

	fakeClient := fake.NewSimpleClientset()
	for _, cm := range configMaps {
		_, err := fakeClient.CoreV1().ConfigMaps(cm.Namespace).Create(context.Background(), cm, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	stopCh := make(chan struct{})
	t.Cleanup(func() { close(stopCh) })

	factory := informers.NewSharedInformerFactory(fakeClient, 0)
	syncer := NewConfigMapCacheSyncer(factory.Core().V1().ConfigMaps(), fakeClient, testNamespace, testCMName, nil, nil)

	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)

	return syncer, fakeClient
}

func TestConfigMapCacheSyncer_LoadNotFound(t *testing.T) {
	syncer, _ := newFakeSyncer(t)
	entries, err := syncer.Load(context.Background())
	require.NoError(t, err)
	assert.Nil(t, entries)
}

func TestConfigMapCacheSyncer_LoadFromExisting(t *testing.T) {
	cacheData := &inspectionCacheFile{
		Version: inspectionCacheVersion,
		Entries: map[string]*InspectionCacheEntry{
			"sha256:aaa": {Labels: map[string]string{"k": "v"}},
		},
	}
	raw, err := json.Marshal(cacheData)
	require.NoError(t, err)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testCMName, Namespace: testNamespace},
		Data:       map[string]string{cacheConfigMapKey: string(raw)},
	}

	syncer, _ := newFakeSyncer(t, cm)
	entries, err := syncer.Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, entries)
	assert.Equal(t, "v", entries["sha256:aaa"].Labels["k"])
}

func TestConfigMapCacheSyncer_SaveCreatesConfigMap(t *testing.T) {
	syncer, client := newFakeSyncer(t)

	entries := map[string]*InspectionCacheEntry{
		"sha256:aaa": {Labels: map[string]string{"k": "v"}},
	}

	err := syncer.save(context.Background(), entries)
	require.NoError(t, err)

	cm, err := client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), testCMName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, cm.Data[cacheConfigMapKey], "sha256:aaa")
}

func TestConfigMapCacheSyncer_SaveUpdatesExisting(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testCMName, Namespace: testNamespace},
		Data:       map[string]string{cacheConfigMapKey: `{"version":1,"entries":{}}`},
	}

	syncer, client := newFakeSyncer(t, cm)

	entries := map[string]*InspectionCacheEntry{
		"sha256:bbb": {Labels: map[string]string{"new": "entry"}},
	}

	err := syncer.save(context.Background(), entries)
	require.NoError(t, err)

	updated, err := client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), testCMName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, updated.Data[cacheConfigMapKey], "sha256:bbb")
}

func TestConfigMapCacheSyncer_SaveAppliesFilterAndTransformer(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()

	stopCh := make(chan struct{})
	t.Cleanup(func() { close(stopCh) })

	factory := informers.NewSharedInformerFactory(fakeClient, 0)
	filter := NewCacheEntryFilter("io.openshift.release")
	syncer := NewConfigMapCacheSyncer(factory.Core().V1().ConfigMaps(), fakeClient, testNamespace, testCMName, filter, nil)

	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)

	entries := map[string]*InspectionCacheEntry{
		"sha256:release": {Labels: map[string]string{"io.openshift.release": "5.0.0"}},
		"sha256:random":  {Labels: map[string]string{"vendor": "somebody"}},
	}

	err := syncer.save(context.Background(), entries)
	require.NoError(t, err)

	cm, err := fakeClient.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), testCMName, metav1.GetOptions{})
	require.NoError(t, err)

	assert.Contains(t, cm.Data[cacheConfigMapKey], "sha256:release")
	assert.NotContains(t, cm.Data[cacheConfigMapKey], "sha256:random")
}

func TestConfigMapCacheSyncer_SaveSkipsOversize(t *testing.T) {
	syncer, client := newFakeSyncer(t)

	bigValue := make([]byte, configMapMaxBytes+1)
	entries := map[string]*InspectionCacheEntry{
		"sha256:big": {Files: map[string][]byte{"/big": bigValue}},
	}

	err := syncer.save(context.Background(), entries)
	require.NoError(t, err)

	_, err = client.CoreV1().ConfigMaps(testNamespace).Get(context.Background(), testCMName, metav1.GetOptions{})
	assert.Error(t, err, "ConfigMap should not be created when data exceeds limit")
}

func TestConfigMapCacheSyncer_SaveSkipsDuplicate(t *testing.T) {
	syncer, _ := newFakeSyncer(t)

	entries := map[string]*InspectionCacheEntry{
		"sha256:aaa": {Labels: map[string]string{"k": "v"}},
	}

	require.NoError(t, syncer.save(context.Background(), entries))
	require.NoError(t, syncer.save(context.Background(), entries))
}
