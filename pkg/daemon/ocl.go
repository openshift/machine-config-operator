package daemon

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	originalOSImageURLAnnoKey string = "machineconfiguration.openshift.io/original-osimageurl"
	oclOSImageURLAnnoKey      string = "machineconfiguration.openshift.io/ocl-osimageurl"
)

// If the provided image is empty, then the OSImageURL value on the
// MachineConfig should take precedence. Otherwise, if the provided image is
// set, then it should take precedence over the OSImageURL value. This is only
// used for OCL OS updates and should not be used for anything else.
func embedOCLImageInMachineConfig(img string, mc *mcfgv1.MachineConfig) *mcfgv1.MachineConfig {
	// We don't want to mutate the lister cache, so we make a copy of any
	// MachineConfigs we get.
	copied := mc.DeepCopy()

	if img == "" {
		klog.Infof("embedOCLImageInMachineConfig: no image provided, skipping embedding for MC %q", copied.Name)
		return copied
	}

	klog.Infof("embedOCLImageInMachineConfig: embedding OCL image %q into MC %q", img, copied.Name)
	klog.Infof("embedOCLImageInMachineConfig: original OSImageURL = %q", copied.Spec.OSImageURL)

	// Store the original OS image URL in an annotation value.
	metav1.SetMetaDataAnnotation(&copied.ObjectMeta, originalOSImageURLAnnoKey, copied.Spec.OSImageURL)
	klog.Infof("embedOCLImageInMachineConfig: set annotation %q = %q", originalOSImageURLAnnoKey, copied.Spec.OSImageURL)

	// Store the OCL image URL in an annotation value.
	metav1.SetMetaDataAnnotation(&copied.ObjectMeta, oclOSImageURLAnnoKey, img)
	klog.Infof("embedOCLImageInMachineConfig: set annotation %q = %q", oclOSImageURLAnnoKey, img)

	// Override the OSImageURL field with the provided image.
	copied.Spec.OSImageURL = img
	klog.Infof("embedOCLImageInMachineConfig: OSImageURL now set to OCL image %q", img)

	return copied
}

// Extracts the OCL image, if it exists, from the given MachineConfig.
func extractOCLImageFromMachineConfig(mc *mcfgv1.MachineConfig) (*mcfgv1.MachineConfig, string) {
	// We don't want to mutate the lister cache, so we make a copy of any
	// MachineConfigs we get.
	copied := mc.DeepCopy()
	klog.Infof("extractOCLImageFromMachineConfig: original OSImageURL = %s", copied.Spec.OSImageURL)

	// If we don't have OCL annotation keys, there is nothing to extract, so just
	// return the copy.
	if !metav1.HasAnnotation(copied.ObjectMeta, originalOSImageURLAnnoKey) && !metav1.HasAnnotation(copied.ObjectMeta, oclOSImageURLAnnoKey) {
		klog.Infof("extractOCLImageFromMachineConfig: no OCL annotations present on MC %q", copied.Name)

		return copied, ""
	}

	// Fetches the value from the annotation and deletes the annotation key.
	pop := func(key string) string {
		val := copied.Annotations[key]
		klog.Infof("extractOCLImageFromMachineConfig: popping annotation %q = %q", key, val)

		delete(copied.Annotations, key)
		return val
	}

	originalOSImageURL := pop(originalOSImageURLAnnoKey)
	copied.Spec.OSImageURL = originalOSImageURL
	klog.Infof("extractOCLImageFromMachineConfig: restored original OSImageURL = %s", originalOSImageURL)

	oclImage := pop(oclOSImageURLAnnoKey)
	klog.Infof("extractOCLImageFromMachineConfig: extracted OCL image = %s", oclImage)

	// Get the OCL OS image URL value that was on the MachineConfig from the annotation.
	return copied, oclImage
}

// Extracts the OCL image from a MachineConfig into an onDiskConfig.
func newOnDiskConfigFromMachineConfig(mc *mcfgv1.MachineConfig) *onDiskConfig {
	klog.Infof("[newOnDiskConfigFromMachineConfig]: incoming MC %q, Spec.OSImageURL=%q", mc.Name, mc.Spec.OSImageURL)
	decanonicalized, img := extractOCLImageFromMachineConfig(mc)
	klog.Infof("[newOnDiskConfigFromMachineConfig]: decanonicalized MC %q, Spec.OSImageURL=%q, extracted img=%q", decanonicalized.Name, decanonicalized.Spec.OSImageURL, img)
	return &onDiskConfig{
		currentImage:  img,
		currentConfig: decanonicalized,
	}
}
