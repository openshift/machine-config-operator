package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openshift/machine-config-operator/pkg/controller/build/imagebuilder"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Assisted-By: Claude Sonnet 4.5
func TestApplyDigestConfigMap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	configMapName := "test-digest-configmap"
	digest := "sha256:1234567890abcdef"

	t.Run("creates new ConfigMap successfully", func(t *testing.T) {
		t.Parallel()

		client := fake.NewSimpleClientset()
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
				Labels: map[string]string{
					"test-label": "test-value",
				},
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: digest,
			},
		}

		err := applyDigestConfigMap(ctx, client, cm)
		require.NoError(t, err)

		// Verify ConfigMap was created
		created, err := client.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, configMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, configMapName, created.Name)
		assert.Equal(t, ctrlcommon.MCONamespace, created.Namespace)
		assert.Equal(t, digest, created.Data[imagebuilder.DigestConfigMapKey])
		assert.Equal(t, "test-value", created.Labels["test-label"])
	})

	t.Run("updates existing ConfigMap successfully", func(t *testing.T) {
		t.Parallel()

		// Create initial ConfigMap with old data
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
				Labels: map[string]string{
					"old-label": "old-value",
				},
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: "sha256:olddigest",
			},
		}

		client := fake.NewSimpleClientset(existingCM)
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
				Labels: map[string]string{
					"new-label": "new-value",
				},
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: digest,
			},
		}

		err := applyDigestConfigMap(ctx, client, cm)
		require.NoError(t, err)

		// Verify ConfigMap was updated
		updated, err := client.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, configMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, digest, updated.Data[imagebuilder.DigestConfigMapKey])
		assert.Equal(t, "new-value", updated.Labels["new-label"])
		assert.NotContains(t, updated.Labels, "old-label")
	})

	t.Run("handles Get error after AlreadyExists", func(t *testing.T) {
		t.Parallel()

		client := fake.NewSimpleClientset()
		// Add a reactor to return an error when Get is called after Create fails
		client.PrependReactor("get", "configmaps", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, apierrors.NewInternalError(assert.AnError)
		})

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
				Labels:    map[string]string{},
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: digest,
			},
		}

		// First create to trigger AlreadyExists on second call
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: "sha256:olddigest",
			},
		}
		_, err := client.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(ctx, existingCM, metav1.CreateOptions{})
		require.NoError(t, err)

		// Now try to apply, which should fail on Get
		err = applyDigestConfigMap(ctx, client, cm)
		require.Error(t, err)
		assert.True(t, apierrors.IsInternalError(err))
	})

	t.Run("handles Update error", func(t *testing.T) {
		t.Parallel()

		// Create initial ConfigMap
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: "sha256:olddigest",
			},
		}

		client := fake.NewSimpleClientset(existingCM)
		// Add a reactor to return an error when Update is called
		client.PrependReactor("update", "configmaps", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, apierrors.NewInternalError(assert.AnError)
		})

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
				Labels:    map[string]string{},
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: digest,
			},
		}

		err := applyDigestConfigMap(ctx, client, cm)
		require.Error(t, err)
		assert.True(t, apierrors.IsInternalError(err))
	})

	t.Run("idempotent: multiple applies succeed", func(t *testing.T) {
		t.Parallel()

		client := fake.NewSimpleClientset()
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
				Labels: map[string]string{
					"test-label": "test-value",
				},
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: digest,
			},
		}

		// First apply
		err := applyDigestConfigMap(ctx, client, cm)
		require.NoError(t, err)

		// Second apply should succeed (update)
		err = applyDigestConfigMap(ctx, client, cm)
		require.NoError(t, err)

		// Third apply should succeed (update)
		err = applyDigestConfigMap(ctx, client, cm)
		require.NoError(t, err)

		// Verify ConfigMap still has correct data
		result, err := client.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, configMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, digest, result.Data[imagebuilder.DigestConfigMapKey])
		assert.Equal(t, "test-value", result.Labels["test-label"])
	})
}

// Assisted-By: Claude Sonnet 4.5
func TestApplyDigestConfigMapFromFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	configMapName := "test-digest-configmap"
	digest := "sha256:1234567890abcdef"

	t.Run("creates ConfigMap from valid digest file", func(t *testing.T) {
		t.Parallel()

		// Create a temporary digest file
		tmpDir := t.TempDir()
		digestFile := filepath.Join(tmpDir, "digest")
		err := os.WriteFile(digestFile, []byte(digest+"\n"), 0644)
		require.NoError(t, err)

		client := fake.NewSimpleClientset()
		opts := DigestConfigMapOpts{
			ConfigMapName: configMapName,
			Namespace:     ctrlcommon.MCONamespace,
			DigestFile:    digestFile,
			Labels:        "test-label=test-value",
		}
		err = ApplyDigestConfigMapFromFile(ctx, client, opts)
		require.NoError(t, err)

		// Verify ConfigMap was created
		cm, err := client.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, configMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, digest, cm.Data[imagebuilder.DigestConfigMapKey])
		assert.Equal(t, "test-value", cm.Labels["test-label"])
	})

	t.Run("handles empty digest file", func(t *testing.T) {
		t.Parallel()

		// Create an empty digest file
		tmpDir := t.TempDir()
		digestFile := filepath.Join(tmpDir, "digest")
		err := os.WriteFile(digestFile, []byte("   \n  "), 0644)
		require.NoError(t, err)

		client := fake.NewSimpleClientset()
		opts := DigestConfigMapOpts{
			ConfigMapName: configMapName,
			Namespace:     ctrlcommon.MCONamespace,
			DigestFile:    digestFile,
			Labels:        "",
		}
		err = ApplyDigestConfigMapFromFile(ctx, client, opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is empty")
	})

	t.Run("handles missing digest file", func(t *testing.T) {
		t.Parallel()

		client := fake.NewSimpleClientset()
		opts := DigestConfigMapOpts{
			ConfigMapName: configMapName,
			Namespace:     ctrlcommon.MCONamespace,
			DigestFile:    "/nonexistent/digest",
			Labels:        "",
		}
		err := ApplyDigestConfigMapFromFile(ctx, client, opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read digestfile")
	})

	t.Run("handles invalid labels", func(t *testing.T) {
		t.Parallel()

		// Create a temporary digest file
		tmpDir := t.TempDir()
		digestFile := filepath.Join(tmpDir, "digest")
		err := os.WriteFile(digestFile, []byte(digest), 0644)
		require.NoError(t, err)

		client := fake.NewSimpleClientset()
		opts := DigestConfigMapOpts{
			ConfigMapName: configMapName,
			Namespace:     ctrlcommon.MCONamespace,
			DigestFile:    digestFile,
			Labels:        "invalid-label-format",
		}
		err = ApplyDigestConfigMapFromFile(ctx, client, opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse labels")
	})

	t.Run("trims whitespace from digest", func(t *testing.T) {
		t.Parallel()

		// Create a digest file with whitespace
		tmpDir := t.TempDir()
		digestFile := filepath.Join(tmpDir, "digest")
		err := os.WriteFile(digestFile, []byte("  \n"+digest+"\n  \n"), 0644)
		require.NoError(t, err)

		client := fake.NewSimpleClientset()
		opts := DigestConfigMapOpts{
			ConfigMapName: configMapName,
			Namespace:     ctrlcommon.MCONamespace,
			DigestFile:    digestFile,
			Labels:        "",
		}
		err = ApplyDigestConfigMapFromFile(ctx, client, opts)
		require.NoError(t, err)

		// Verify ConfigMap was created with trimmed digest
		cm, err := client.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, configMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, digest, cm.Data[imagebuilder.DigestConfigMapKey])
	})

	t.Run("updates existing ConfigMap from file", func(t *testing.T) {
		t.Parallel()

		// Create initial ConfigMap
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: ctrlcommon.MCONamespace,
				Labels: map[string]string{
					"old-label": "old-value",
				},
			},
			Data: map[string]string{
				imagebuilder.DigestConfigMapKey: "sha256:olddigest",
			},
		}

		client := fake.NewSimpleClientset(existingCM)

		// Create a temporary digest file
		tmpDir := t.TempDir()
		digestFile := filepath.Join(tmpDir, "digest")
		err := os.WriteFile(digestFile, []byte(digest), 0644)
		require.NoError(t, err)

		opts := DigestConfigMapOpts{
			ConfigMapName: configMapName,
			Namespace:     ctrlcommon.MCONamespace,
			DigestFile:    digestFile,
			Labels:        "new-label=new-value",
		}
		err = ApplyDigestConfigMapFromFile(ctx, client, opts)
		require.NoError(t, err)

		// Verify ConfigMap was updated
		cm, err := client.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, configMapName, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, digest, cm.Data[imagebuilder.DigestConfigMapKey])
		assert.Equal(t, "new-value", cm.Labels["new-label"])
		assert.NotContains(t, cm.Labels, "old-label")
	})
}
