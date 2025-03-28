package daemon

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOCLHelpers(t *testing.T) {
	testCases := []struct {
		name  string
		image string
	}{
		{
			name:  "With image",
			image: "image-pullspec",
		},
		{
			name:  "No image",
			image: "",
		},
	}

	oclAnnotations := []string{originalOSImageURLAnnoKey, oclOSImageURLAnnoKey}

	newMachineConfig := func() *mcfgv1.MachineConfig {
		return &mcfgv1.MachineConfig{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
			Spec: mcfgv1.MachineConfigSpec{
				OSImageURL: "mc-osimageurl",
			},
		}
	}

	for _, testCase := range testCases {
		t.Run("Simple Roundtrip "+testCase.name, func(t *testing.T) {
			mc := newMachineConfig()
			roundtrip, img := extractOCLImageFromMachineConfig(embedOCLImageInMachineConfig(testCase.image, mc))
			assert.Equal(t, testCase.image, img)
			assert.Equal(t, mc.Spec.OSImageURL, roundtrip.Spec.OSImageURL)
			assertNotHasAnnotations(t, roundtrip.ObjectMeta, oclAnnotations)
		})

		t.Run("Extended "+testCase.name, func(t *testing.T) {
			mc := newMachineConfig()
			embedded := embedOCLImageInMachineConfig(testCase.image, mc)
			extracted, extractedImg := extractOCLImageFromMachineConfig(embedded)

			if testCase.image != "" {
				assert.Equal(t, testCase.image, embedded.Spec.OSImageURL)
				assert.NotEqual(t, testCase.image, mc.Spec.OSImageURL)
				assert.Equal(t, mc.Spec.OSImageURL, extracted.Spec.OSImageURL)
				assert.Equal(t, testCase.image, extractedImg)
				assertHasAnnotations(t, embedded.ObjectMeta, map[string]string{
					originalOSImageURLAnnoKey: "mc-osimageurl",
					oclOSImageURLAnnoKey:      testCase.image,
				})
				assertNotHasAnnotations(t, extracted.ObjectMeta, oclAnnotations)
			} else {
				assert.NotEqual(t, testCase.image, embedded.Spec.OSImageURL)
				assert.Equal(t, mc.Spec.OSImageURL, embedded.Spec.OSImageURL)
				assert.Equal(t, mc.Spec.OSImageURL, extracted.Spec.OSImageURL)
				assert.Equal(t, "", extractedImg)
				assertNotHasAnnotations(t, embedded.ObjectMeta, oclAnnotations)
				assertNotHasAnnotations(t, extracted.ObjectMeta, oclAnnotations)
			}

			assert.Equal(t, "mc-osimageurl", mc.Spec.OSImageURL)
		})
	}

	t.Run("OnDiskConfig", func(t *testing.T) {
		mc := newMachineConfig()

		odc := newOnDiskConfigFromMachineConfig(mc)
		assert.Equal(t, odc.currentConfig, mc)
		assert.Equal(t, odc.currentImage, "")

		embedded := embedOCLImageInMachineConfig("image-pullspec", mc)
		odc = newOnDiskConfigFromMachineConfig(embedded)
		assert.Equal(t, odc.currentConfig, mc)
		assert.Equal(t, odc.currentImage, "image-pullspec")
	})
}

func assertHasAnnotations(t *testing.T, obj metav1.ObjectMeta, annos map[string]string) {
	t.Helper()
	for key, value := range annos {
		assert.True(t, metav1.HasAnnotation(obj, key), "missing expected annotation %q", key)
		assert.Equal(t, value, obj.Annotations[key], "annotation key %q value %q not equal or missing", key, value)
	}
}

func assertNotHasAnnotations(t *testing.T, obj metav1.ObjectMeta, annoKeys []string) {
	t.Helper()
	for _, key := range annoKeys {
		assert.False(t, metav1.HasAnnotation(obj, key), "found annotation %q unexpectedly", key)
	}
}
