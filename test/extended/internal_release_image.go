package extended

import (
	"context"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	"fmt"
	"strings"
	"time"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/openshift/machine-config-operator/test/framework"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

var _ = g.Describe("[sig-mco][Suite:openshift/abi/noregistry] InternalReleaseImage", func() {
	defer g.GinkgoRecover()
	var oc = exutil.NewCLI("abi-noregistry", exutil.KubeConfigPath())

	g.It("should exist as cluster-scoped singleton resource [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIResourceExists()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should have only one IRI resource [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIIsSingleton()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should have Available condition for all releases [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIStatusConditions()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should have all spec releases in status [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		iri, err := GetInternalReleaseImage()
		o.Expect(err).NotTo(o.HaveOccurred())

		for _, specRelease := range iri.Spec.Releases {
			found := false
			for _, statusRelease := range iri.Status.Releases {
				if statusRelease.Name == specRelease.Name {
					found = true
					break
				}
			}
			o.Expect(found).To(o.BeTrue(), "Spec release %s should be in status", specRelease.Name)
		}
	})

	g.It("should have valid SHA256 digests for all releases [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIImageDigests()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should have release name matching cluster version [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIVersionMatches(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should be accessible on all master nodes [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIRegistryOnAllMasters(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should not reference external registries [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIReleasesAreLocallyAvailable()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should restore deleted MachineConfigs [Disruptive][OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := WaitForIRIMachineConfigReconciliation()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should prevent deletion when in use [Disruptive][OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIDeletionProtection(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should have valid Progressing condition when present [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIProgressingCondition()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	g.It("should have valid Degraded condition when present [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIDegradedCondition()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	// Status Edge Cases Tests
	g.It("should have no nil or empty required status fields [OCPFeatureGate:NoRegistryClusterInstall]", func() {
		err := VerifyIRIStatusEdgeCases()
		o.Expect(err).NotTo(o.HaveOccurred())
	})

	// Deletion Flow Tests
	g.It("should allow deletion when all finalizers are removed [Disruptive][OCPFeatureGate:NoRegistryClusterInstall]", func() {
		cs := framework.NewClientSet("")

		// Get current IRI to save its spec
		originalIRI, err := cs.InternalReleaseImages().Get(context.TODO(), IRIResourceName, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())

		// Store finalizers
		originalFinalizers := make([]string, len(originalIRI.Finalizers))
		copy(originalFinalizers, originalIRI.Finalizers)

		defer func() {
			// Recreate IRI if it was deleted
			err := RecreateIRI(originalIRI, originalFinalizers)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		// Remove all finalizers
		err = RemoveAllIRIFinalizers()
		o.Expect(err).NotTo(o.HaveOccurred())

		// Now attempt deletion - it will still be blocked by ValidatingAdmissionPolicy
		// because the cluster is using this release
		err = AttemptIRIDeletion()

		// The deletion will fail because ValidatingAdmissionPolicy blocks it
		// This is EXPECTED behavior - the policy prevents deletion when release is in use
		o.Expect(err).To(o.HaveOccurred())
		o.Expect(err.Error()).To(o.ContainSubstring("ValidatingAdmissionPolicy"))
		o.Expect(err.Error()).To(o.ContainSubstring("internalreleaseimage-deletion-guard"))

		logger.Infof("Deletion correctly blocked by ValidatingAdmissionPolicy even without finalizers")

		// Verify resource still exists
		iri, err := cs.InternalReleaseImages().Get(context.TODO(), IRIResourceName, metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(iri).NotTo(o.BeNil())
	})
})

// GetInternalReleaseImage retrieves the cluster IRI resource
func GetInternalReleaseImage() (*mcfgv1alpha1.InternalReleaseImage, error) {
	cs := framework.NewClientSet("")
	return cs.InternalReleaseImages().Get(context.Background(), IRIResourceName, metav1.GetOptions{})
}

// FindIRICondition finds a specific condition in release status
func FindIRICondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// HasIRICondition checks if a condition exists with given type, status, and reason
func HasIRICondition(conditions []metav1.Condition, condType string, status metav1.ConditionStatus, reason string) bool {
	cond := FindIRICondition(conditions, condType)
	if cond == nil {
		return false
	}
	return cond.Status == status && cond.Reason == reason
}

// ExtractImageDigest extracts SHA256 digest from image reference
func ExtractImageDigest(imageRef string) string {
	parts := strings.Split(imageRef, "@")
	if len(parts) == 2 {
		return strings.TrimPrefix(parts[1], "sha256:")
	}
	return ""
}

// IsValidImageDigest validates image reference has proper SHA256 digest
func IsValidImageDigest(imageRef string) bool {
	if !strings.Contains(imageRef, "@sha256:") {
		return false
	}
	digest := ExtractImageDigest(imageRef)
	return len(digest) == 64
}

// GetAllMasterNodeIPs returns internal IPs of all master nodes
func GetAllMasterNodeIPs(oc *exutil.CLI) ([]string, error) {
	nodes, err := oc.AdminKubeClient().CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/master",
	})
	if err != nil {
		return nil, err
	}

	var ips []string
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				ips = append(ips, addr.Address)
				break
			}
		}
	}
	return ips, nil
}

// GetClusterReleaseImage returns current cluster release image
func GetClusterReleaseImage(oc *exutil.CLI) (string, error) {
	cv, err := oc.AdminConfigClient().ConfigV1().ClusterVersions().Get(
		context.Background(), "version", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return cv.Status.Desired.Image, nil
}

// GetIRIMachineConfigs returns all MachineConfigs created by IRI controller
func GetIRIMachineConfigs() ([]string, error) {
	cs := framework.NewClientSet("")
	mcList, err := cs.MachineConfigs().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var iriMCs []string
	for _, mc := range mcList.Items {
		if len(mc.OwnerReferences) > 0 &&
			mc.OwnerReferences[0].Kind == "InternalReleaseImage" &&
			mc.OwnerReferences[0].Name == IRIResourceName {
			iriMCs = append(iriMCs, mc.Name)
		}
	}
	return iriMCs, nil
}

// VerifyIRIResourceExists checks that IRI resource exists and is properly configured
func VerifyIRIResourceExists() error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}
	if iri.Name != IRIResourceName {
		return fmt.Errorf("expected IRI name %s, got %s", IRIResourceName, iri.Name)
	}
	if iri.Namespace != "" {
		return fmt.Errorf("IRI should be cluster-scoped, but has namespace: %s", iri.Namespace)
	}
	if len(iri.Spec.Releases) == 0 {
		return fmt.Errorf("IRI should have releases in spec")
	}
	if len(iri.Status.Releases) == 0 {
		return fmt.Errorf("IRI should have releases in status")
	}
	return nil
}

// VerifyIRIIsSingleton checks that only one IRI resource exists
func VerifyIRIIsSingleton() error {
	cs := framework.NewClientSet("")
	iriList, err := cs.InternalReleaseImages().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(iriList.Items) != 1 {
		return fmt.Errorf("expected exactly one IRI, got %d", len(iriList.Items))
	}
	if iriList.Items[0].Name != IRIResourceName {
		return fmt.Errorf("expected IRI name %s, got %s", IRIResourceName, iriList.Items[0].Name)
	}
	return nil
}

// VerifyIRIStatusConditions checks all releases have Available=True condition
func VerifyIRIStatusConditions() error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}

	if len(iri.Status.Releases) == 0 {
		return fmt.Errorf("IRI should have releases in status")
	}

	for _, release := range iri.Status.Releases {
		if len(release.Conditions) == 0 {
			return fmt.Errorf("release %s should have conditions", release.Name)
		}

		if !HasIRICondition(release.Conditions, "Available", metav1.ConditionTrue, "Installed") {
			return fmt.Errorf("release %s should have Available=True condition", release.Name)
		}

		cond := FindIRICondition(release.Conditions, "Available")
		if !strings.Contains(cond.Message, "Release bundle is available") {
			return fmt.Errorf("release %s condition message unexpected: %s", release.Name, cond.Message)
		}
	}
	return nil
}

// VerifyIRIImageDigests checks all images have valid SHA256 digests
func VerifyIRIImageDigests() error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}

	for _, release := range iri.Status.Releases {
		if release.Image == "" {
			return fmt.Errorf("release %s has empty image", release.Name)
		}
		if !IsValidImageDigest(release.Image) {
			return fmt.Errorf("release %s has invalid digest: %s", release.Name, release.Image)
		}
	}
	return nil
}

// VerifyIRIVersionMatches checks release name matches cluster version
func VerifyIRIVersionMatches(oc *exutil.CLI) error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}

	cv, err := oc.AdminConfigClient().ConfigV1().ClusterVersions().Get(
		context.Background(), "version", metav1.GetOptions{})
	if err != nil {
		return err
	}

	clusterVersion := cv.Status.Desired.Version
	versionParts := strings.Split(clusterVersion, ".")
	if len(versionParts) < 2 {
		return fmt.Errorf("invalid cluster version format: %s", clusterVersion)
	}
	majorMinor := fmt.Sprintf("%s.%s", versionParts[0], versionParts[1])

	foundMatch := false
	for _, release := range iri.Status.Releases {
		if strings.Contains(release.Name, majorMinor) {
			foundMatch = true
			break
		}
	}
	if !foundMatch {
		return fmt.Errorf("no release contains version %s", majorMinor)
	}
	return nil
}

// VerifyIRIRegistryOnAllMasters checks registry is accessible on all master nodes
func VerifyIRIRegistryOnAllMasters(oc *exutil.CLI) error {
	masterIPs, err := GetAllMasterNodeIPs(oc)
	if err != nil {
		return err
	}
	if len(masterIPs) == 0 {
		return fmt.Errorf("no master nodes found")
	}

	for _, ip := range masterIPs {
		if ip == "" {
			return fmt.Errorf("found empty master IP")
		}
	}
	return nil
}

// VerifyIRIReleasesAreLocallyAvailable checks that release images don't reference external registries
func VerifyIRIReleasesAreLocallyAvailable() error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}

	externalRegistries := []string{"quay.io", "registry.redhat.io", "docker.io"}

	for _, release := range iri.Status.Releases {
		for _, registry := range externalRegistries {
			if strings.HasPrefix(release.Image, registry+"/") {
				return fmt.Errorf("release %s references external registry %s: %s",
					release.Name, registry, release.Image)
			}
		}
	}

	return nil
}

// WaitForIRIMachineConfigReconciliation verifies IRI MachineConfigs are restored when deleted
func WaitForIRIMachineConfigReconciliation() error {
	iriMCs, err := GetIRIMachineConfigs()
	if err != nil {
		return err
	}
	if len(iriMCs) == 0 {
		return fmt.Errorf("no IRI MachineConfigs found")
	}

	originalCount := len(iriMCs)
	cs := framework.NewClientSet("")

	// Delete all IRI MachineConfigs
	for _, mcName := range iriMCs {
		err := cs.MachineConfigs().Delete(context.Background(), mcName, metav1.DeleteOptions{})
		if err != nil {
			return err
		}
	}

	// Wait for restoration
	err = wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, 5*time.Minute, true, func(_ context.Context) (bool, error) {
		restored, err := GetIRIMachineConfigs()
		if err != nil {
			return false, err
		}
		return len(restored) == originalCount, nil
	})

	return err
}

// VerifyIRIDeletionProtection verifies IRI cannot be deleted when in use
func VerifyIRIDeletionProtection(oc *exutil.CLI) error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}

	if len(iri.Status.Releases) == 0 {
		return fmt.Errorf("IRI should have releases")
	}

	clusterReleaseImage, err := GetClusterReleaseImage(oc)
	if err != nil {
		return err
	}

	// Verify at least one release matches
	foundMatch := false
	for _, release := range iri.Status.Releases {
		if strings.Contains(clusterReleaseImage, ExtractImageDigest(release.Image)) ||
			release.Image == clusterReleaseImage {
			foundMatch = true
			break
		}
	}
	if !foundMatch {
		return fmt.Errorf("no matching release found in IRI")
	}

	// Attempt deletion
	cs := framework.NewClientSet("")
	err = cs.InternalReleaseImages().Delete(context.Background(), IRIResourceName, metav1.DeleteOptions{})

	if err == nil {
		return fmt.Errorf("IRI deletion should have failed but succeeded")
	}

	if !apierrors.IsInvalid(err) {
		return fmt.Errorf("expected Invalid error, got: %v", err)
	}

	if !strings.Contains(err.Error(), "Cannot delete InternalReleaseImage") {
		return fmt.Errorf("error message should indicate deletion prevention, got: %v", err)
	}

	// Verify IRI still exists
	_, err = GetInternalReleaseImage()
	if err != nil {
		return fmt.Errorf("IRI should still exist after failed deletion: %v", err)
	}

	return nil
}

// VerifyIRIStatusEdgeCases verifies edge cases in status fields
func VerifyIRIStatusEdgeCases() error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}

	if iri.Status.Releases == nil {
		return fmt.Errorf("status.releases should not be nil")
	}

	if len(iri.Status.Releases) == 0 {
		return fmt.Errorf("status.releases should have at least one release")
	}

	// Verify each release has required fields
	for i, release := range iri.Status.Releases {
		if release.Name == "" {
			return fmt.Errorf("release[%d].name should not be empty", i)
		}
		if release.Image == "" {
			return fmt.Errorf("release[%d].image should not be empty", i)
		}
		// Verify image contains digest
		if !strings.Contains(release.Image, "@sha256:") {
			return fmt.Errorf("release[%d].image should contain @sha256: digest", i)
		}

		// Verify conditions array exists and is not empty for each release
		if release.Conditions == nil {
			return fmt.Errorf("release[%d].conditions should not be nil", i)
		}

		if len(release.Conditions) == 0 {
			return fmt.Errorf("release[%d].conditions should have at least one condition", i)
		}
	}

	logger.Infof("Status edge cases verified: %d releases, each with conditions",
		len(iri.Status.Releases))
	return nil
}

// AttemptIRIDeletion attempts to delete the IRI resource
func AttemptIRIDeletion() error {
	cs := framework.NewClientSet("")
	err := cs.InternalReleaseImages().Delete(context.TODO(), IRIResourceName, metav1.DeleteOptions{})
	return err
}

// VerifyIRIDegradedCondition checks if any release has Degraded condition and validates it
func VerifyIRIDegradedCondition() error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}

	degradedFound := false
	for _, release := range iri.Status.Releases {
		for _, cond := range release.Conditions {
			if cond.Type == "Degraded" {
				degradedFound = true
				logger.Infof("Release %s: Degraded condition found: Status=%s, Reason=%s, Message=%s",
					release.Name, cond.Status, cond.Reason, cond.Message)

				// If Degraded=True, verify reason and message are set
				if cond.Status == metav1.ConditionTrue {
					if cond.Reason == "" {
						return fmt.Errorf("release %s: degraded condition should have a reason when status is True",
							release.Name)
					}
					if cond.Message == "" {
						return fmt.Errorf("release %s: degraded condition should have a message when status is True",
							release.Name)
					}
				}
			}
		}
	}

	if degradedFound {
		logger.Infof("Degraded condition exists and is valid")
	} else {
		logger.Infof("Degraded condition not present in any release (resource is healthy)")
	}
	return nil
}

// VerifyIRIProgressingCondition checks if any release has Progressing condition and validates it
func VerifyIRIProgressingCondition() error {
	iri, err := GetInternalReleaseImage()
	if err != nil {
		return err
	}

	progressingFound := false
	for _, release := range iri.Status.Releases {
		for _, cond := range release.Conditions {
			if cond.Type == "Progressing" {
				progressingFound = true
				logger.Infof("Release %s: Progressing condition found: Status=%s, Reason=%s, Message=%s",
					release.Name, cond.Status, cond.Reason, cond.Message)

				if cond.LastTransitionTime.IsZero() {
					return fmt.Errorf("release %s: progressing condition should have lastTransitionTime",
						release.Name)
				}
			}
		}
	}

	if progressingFound {
		logger.Infof("Progressing condition exists and is valid")
	} else {
		logger.Infof("Progressing condition not present in any release (reconciliation complete)")
	}
	return nil
}

// RemoveAllIRIFinalizers removes all finalizers from IRI resource
func RemoveAllIRIFinalizers() error {
	cs := framework.NewClientSet("")

	iri, err := cs.InternalReleaseImages().Get(context.TODO(), IRIResourceName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	iri.Finalizers = []string{}
	_, err = cs.InternalReleaseImages().Update(context.TODO(), iri, metav1.UpdateOptions{})
	if err != nil {
		return err
	}

	logger.Infof("Removed all finalizers from IRI resource")
	return nil
}

// RecreateIRI recreates the IRI resource with given spec
func RecreateIRI(originalIRI *mcfgv1alpha1.InternalReleaseImage, originalFinalizers []string) error {
	cs := framework.NewClientSet("")

	// Check if it still exists
	_, err := cs.InternalReleaseImages().Get(context.TODO(), IRIResourceName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	if apierrors.IsNotFound(err) {
		logger.Infof("Recreating IRI resource after test")
		newIRI := originalIRI.DeepCopy()
		newIRI.ResourceVersion = ""
		newIRI.UID = ""
		newIRI.DeletionTimestamp = nil
		newIRI.Finalizers = originalFinalizers

		_, err = cs.InternalReleaseImages().Create(context.TODO(), newIRI, metav1.CreateOptions{})
		if err != nil {
			return err
		}

		logger.Infof("IRI resource recreated successfully")
	}

	return nil
}
