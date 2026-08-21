package bootimage

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coreos/stream-metadata-go/stream"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
)

func TestReconcileVSphereProviderSpec(t *testing.T) {
	const release = "417.94.20250101"

	newStream := func(t *testing.T) *stream.Stream {
		t.Helper()
		return newTestOVAStream(t, testArch, release)
	}

	t.Run("VSphere platform spec is nil: no-op", func(t *testing.T) {
		infra := &osconfigv1.Infrastructure{}
		ps := &machinev1beta1.VSphereMachineProviderSpec{}
		patchRequired, reconcileSkipped, newPS, _, err := reconcileVSphereProviderSpec(newStream(t), testArch, infra, ps, "ms", fake.NewClientset())
		if err != nil {
			t.Fatalf("reconcileVSphereProviderSpec() unexpected error: %v", err)
		}
		if patchRequired || reconcileSkipped || newPS != nil {
			t.Errorf("reconcileVSphereProviderSpec() = (%v, %v, %v), want (false, false, nil)", patchRequired, reconcileSkipped, newPS)
		}
	})

	t.Run("missing vmware artifact for arch: error", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		ps := buildVSphereProviderSpec(vcenters[0], fd, "infra1-rhcos-fd0")
		emptyStream := &stream.Stream{Architectures: map[string]stream.Arch{testArch: {}}}

		_, _, _, _, err := reconcileVSphereProviderSpec(emptyStream, testArch, infra, ps, "ms", fake.NewClientset())
		if err == nil || !strings.Contains(err.Error(), "vmware") {
			t.Fatalf("reconcileVSphereProviderSpec() expected a missing-artifact error, got: %v", err)
		}
	})

	t.Run("missing vsphere-creds secret: error", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		ps := buildVSphereProviderSpec(vcenters[0], fd, "infra1-rhcos-fd0")

		_, _, _, _, err := reconcileVSphereProviderSpec(newStream(t), testArch, infra, ps, "ms", fake.NewClientset())
		if err == nil || !strings.Contains(err.Error(), "failed to fetch vsphere-creds Secret") {
			t.Fatalf("reconcileVSphereProviderSpec() expected a missing-secret error, got: %v", err)
		}
	})

	t.Run("malformed vsphere-creds secret: error", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		ps := buildVSphereProviderSpec(vcenters[0], fd, "infra1-rhcos-fd0")

		// A secret exists, but has no credentials for this vCenter's server - reading a missing
		// key from Secret.Data yields "", and vcsim (like real vCenter) rejects an empty-password
		// login outright.
		emptySecret := &corev1.Secret{}
		emptySecret.Name = "vsphere-creds"
		emptySecret.Namespace = "kube-system"
		secretClient := fake.NewClientset(emptySecret)

		vc := vcenters[0]
		createTestVM(t, vc, fd, "infra1-rhcos-fd0")
		setVMProductVersion(t, vc, "infra1-rhcos-fd0", release)

		_, _, _, _, err := reconcileVSphereProviderSpec(newStream(t), testArch, infra, ps, "ms", secretClient)
		if err == nil || !strings.Contains(err.Error(), "getClientsFromServerURL") {
			t.Fatalf("reconcileVSphereProviderSpec() expected a login error, got: %v", err)
		}
	})

	t.Run("success, version already matches: no-op", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secretClient := fake.NewClientset(buildVSphereCredsSecret(vcenters))

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		createTestVM(t, vcenters[0], fd, name)
		setVMProductVersion(t, vcenters[0], name, release)

		ps := buildVSphereProviderSpec(vcenters[0], fd, name)
		vcenters[0].activate()

		// reconcileVSphereProviderSpec always returns a DeepCopy of providerSpec regardless of
		// patchRequired - it's reconcilePlatform (the generic wrapper) that nils it out when no
		// patch is needed - so only patchRequired/reconcileSkipped/err are asserted here.
		patchRequired, reconcileSkipped, newPS, _, err := reconcileVSphereProviderSpec(newStream(t), testArch, infra, ps, "ms", secretClient)
		if err != nil {
			t.Fatalf("reconcileVSphereProviderSpec() unexpected error: %v", err)
		}
		if patchRequired || reconcileSkipped {
			t.Errorf("reconcileVSphereProviderSpec() = (%v, %v), want (false, false)", patchRequired, reconcileSkipped)
		}
		if newPS.Template != name {
			t.Errorf("reconcileVSphereProviderSpec() unexpectedly modified providerSpec.Template: got %q, want unchanged %q", newPS.Template, name)
		}
	})
}
