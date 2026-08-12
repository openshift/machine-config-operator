package bootimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/coreos/stream-metadata-go/stream"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"

	osconfigv1 "github.com/openshift/api/config/v1"

)

// newTestOVAStream serves buildMinimalOVA's synthetic fixture over HTTP and returns a
// stream.Stream whose vmware/ova artifact points at it, matching what
// createNewVMTemplate expects from streamData.QueryDisk(arch, "vmware", "ova"). The server is
// torn down via t.Cleanup.
func newTestOVAStream(t *testing.T, arch, release string) *stream.Stream {
	t.Helper()

	ovaPath := buildMinimalOVA(t)
	content, err := os.ReadFile(ovaPath)
	if err != nil {
		t.Fatalf("failed to read synthetic ova fixture: %v", err)
	}
	sum := sha256.Sum256(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)

	return &stream.Stream{
		Architectures: map[string]stream.Arch{
			arch: {
				Artifacts: map[string]stream.PlatformArtifacts{
					"vmware": {
						Release: release,
						Formats: map[string]stream.ImageFormat{
							"ova": {
								Disk: &stream.Artifact{
									Location: srv.URL + "/mco-test.ova",
									Sha256:   hex.EncodeToString(sum[:]),
								},
							},
						},
					},
				},
			},
		},
	}
}

// setVMProductVersion mutates a simulated VM's Summary.Config.Product.Version directly (bypassing
// the OVF import path, which never sets product info) so tests can control the "existing
// template's RHCOS version" that createNewVMTemplate compares against the desired release.
// version == "" leaves Product set but empty (VAppProductInfo.Version defaults to "").
func setVMProductVersion(t *testing.T, vc *simulatedVCenter, vmName, version string) {
	t.Helper()
	ctx := context.Background()

	vm, err := vc.Finder.VirtualMachine(ctx, vmName)
	if err != nil {
		t.Fatalf("setVMProductVersion: failed to find VM %q: %v", vmName, err)
	}
	simVM, ok := vc.Registry.Get(vm.Reference()).(*simulator.VirtualMachine)
	if !ok {
		t.Fatalf("setVMProductVersion: %q is not a simulator.VirtualMachine", vmName)
	}
	simVM.Summary.Config.Product = &types.VAppProductInfo{Version: version}
}

// clearVMProduct nils out a simulated VM's Summary.Config.Product entirely.
func clearVMProduct(t *testing.T, vc *simulatedVCenter, vmName string) {
	t.Helper()
	ctx := context.Background()

	vm, err := vc.Finder.VirtualMachine(ctx, vmName)
	if err != nil {
		t.Fatalf("clearVMProduct: failed to find VM %q: %v", vmName, err)
	}
	simVM, ok := vc.Registry.Get(vm.Reference()).(*simulator.VirtualMachine)
	if !ok {
		t.Fatalf("clearVMProduct: %q is not a simulator.VirtualMachine", vmName)
	}
	simVM.Summary.Config.Product = nil
}

const testArch = "x86_64"

func TestCreateNewVMTemplate(t *testing.T) {
	const release = "417.94.20250101"

	t.Run("multi-vCenter, multi-failure-domain: only the matching vCenter+FD is touched", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 2)
		fd0 := buildFailureDomain(vcenters[0], "fd0")
		fd1 := buildFailureDomain(vcenters[1], "fd1")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd0, fd1})
		secret := buildVSphereCredsSecret(vcenters)

		name0 := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd0.Name)
		createTestVM(t, vcenters[0], fd0, name0)
		setVMProductVersion(t, vcenters[0], name0, release)

		ps := buildVSphereProviderSpec(vcenters[0], fd0, name0)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		gotName, patchRequired, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err != nil {
			t.Fatalf("createNewVMTemplate() unexpected error: %v", err)
		}
		if patchRequired {
			t.Errorf("createNewVMTemplate() patchRequired = true, want false (version already matches, template name already correct)")
		}
		if gotName != "" {
			t.Errorf("createNewVMTemplate() name = %q, want empty (no-op path returns no name)", gotName)
		}

		// vcenters[1] must never have been touched: no VM should exist there at all, since
		// fd1's Server never matches vcenters[0]'s server and vcenters[0]'s Server never
		// matches fd1 either - the loop should skip it without ever calling
		// getClientsFromServerURL for it. Re-activate vcenters[1] first: govmomi v0.45.1's
		// simulator routes every vim25 SOAP request through the package-level simulator.Map
		// global regardless of which server received it (see activate()'s doc comment), so
		// without this the query below would silently run against vcenters[0]'s registry.
		vcenters[1].activate()
		if _, err := vcenters[1].Finder.VirtualMachine(context.Background(), name0); err == nil {
			t.Errorf("createNewVMTemplate() touched vcenters[1], which does not own fd0")
		}
	})

	t.Run("existing template version mismatch triggers a real OVA rebuild", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		createTestVM(t, vcenters[0], fd, name)
		setVMProductVersion(t, vcenters[0], name, "416.94.20240101") // stale version
		// createNewVMTemplateWithNameForFailureDomain tags the freshly-imported VM using
		// infra.Status.InfrastructureName as govmomi's AttachTag "tagID" argument. govmomi's
		// vapi/tags.Manager treats any non-"urn:"-prefixed string as a tag *name* and resolves it
		// to the real tag ID via a lookup (tags.isName/tagID) - matching how
		// openshift-installer's createClusterTagID creates a tag literally named after the infra
		// ID at cluster install time. Without this fixture the real cluster precondition is
		// missing and attachTag would fail with "tag not found", not because vcsim can't do it.
		createInfraTag(t, vcenters[0], infra.Status.InfrastructureName)

		ps := buildVSphereProviderSpec(vcenters[0], fd, name)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		gotName, patchRequired, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err != nil {
			t.Fatalf("createNewVMTemplate() unexpected error: %v", err)
		}
		if !patchRequired {
			t.Errorf("createNewVMTemplate() patchRequired = false, want true (version mismatch should trigger a rebuild)")
		}
		if gotName != name {
			t.Errorf("createNewVMTemplate() name = %q, want %q", gotName, name)
		}

		if _, err := vcenters[0].Finder.VirtualMachine(context.Background(), name); err != nil {
			t.Errorf("createNewVMTemplate() did not leave a VM named %q behind: %v", name, err)
		}
		tempName := AtomicTempName("mco-tmp", name)
		if _, err := vcenters[0].Finder.VirtualMachine(context.Background(), tempName); err == nil {
			t.Errorf("createNewVMTemplate() left a stale temp VM behind under %q", tempName)
		}
	})

	t.Run("providerSpec.Template diverges from computed name, version already matches: rename only, no vSphere calls", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		existingVM := createTestVM(t, vcenters[0], fd, name)
		setVMProductVersion(t, vcenters[0], name, release)

		// This is the PR #6234 bug scenario: providerSpec.Template is a custom/non-standard
		// name, not the name MCO would compute for this failure domain.
		ps := buildVSphereProviderSpec(vcenters[0], fd, "some-custom-template-name")
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		gotName, patchRequired, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err != nil {
			t.Fatalf("createNewVMTemplate() unexpected error: %v", err)
		}
		if !patchRequired || gotName != name {
			t.Errorf("createNewVMTemplate() = (%q, %v), want (%q, true) to reconcile providerSpec.Template only", gotName, patchRequired, name)
		}

		found, err := vcenters[0].Finder.VirtualMachine(context.Background(), name)
		if err != nil {
			t.Fatalf("expected VM %q to still exist: %v", name, err)
		}
		if found.Reference().Value != existingVM.Reference().Value {
			t.Errorf("createNewVMTemplate() replaced the existing VM when it should not have touched vSphere at all")
		}
	})

	t.Run("providerSpec.Template is a custom name pointing at a current VM: preserved without patch", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		createTestVM(t, vcenters[0], fd, name)
		setVMProductVersion(t, vcenters[0], name, release)

		customName := "my-custom-rhcos-template"
		createTestVM(t, vcenters[0], fd, customName)
		setVMProductVersion(t, vcenters[0], customName, release)

		ps := buildVSphereProviderSpec(vcenters[0], fd, customName)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		gotName, patchRequired, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err != nil {
			t.Fatalf("createNewVMTemplate() unexpected error: %v", err)
		}
		if patchRequired {
			t.Errorf("createNewVMTemplate() patchRequired = true, want false (custom template %q is already at release %s)", customName, release)
		}
		if gotName != "" {
			t.Errorf("createNewVMTemplate() name = %q, want empty (no-op when custom template is current)", gotName)
		}
	})

	t.Run("providerSpec.Template is outdated but computed name already has a current template: switches back without rebuilding, no collision", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		existingComputedVM := createTestVM(t, vcenters[0], fd, name)
		setVMProductVersion(t, vcenters[0], name, release)

		customName := "my-outdated-custom-template"
		createTestVM(t, vcenters[0], fd, customName)
		setVMProductVersion(t, vcenters[0], customName, "416.94.20240101")

		ps := buildVSphereProviderSpec(vcenters[0], fd, customName)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		gotName, patchRequired, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err != nil {
			t.Fatalf("createNewVMTemplate() unexpected error: %v", err)
		}
		if !patchRequired || gotName != name {
			t.Errorf("createNewVMTemplate() = (%q, %v), want (%q, true) (should switch providerSpec.Template back to the already-current computed name)", gotName, patchRequired, name)
		}

		// The computed name's template must be the same VM as before the call: since it was
		// already current, createNewVMTemplate should just point providerSpec.Template back at it
		// rather than colliding with it via a rebuild.
		found, err := vcenters[0].Finder.VirtualMachine(context.Background(), name)
		if err != nil {
			t.Fatalf("expected VM %q to still exist: %v", name, err)
		}
		if found.Reference().Value != existingComputedVM.Reference().Value {
			t.Errorf("createNewVMTemplate() replaced the existing current template at %q instead of switching back to it", name)
		}

		// The outdated custom-named VM must be left alone — the fix only stops using it as
		// providerSpec.Template, it does not touch or destroy it.
		if _, err := vcenters[0].Finder.VirtualMachine(context.Background(), customName); err != nil {
			t.Errorf("createNewVMTemplate() should not have touched the outdated custom template %q: %v", customName, err)
		}

		// No atomic swap should have been attempted for the computed name.
		for _, prefix := range []string{"mco-tmp", "mco-old"} {
			tempName := AtomicTempName(prefix, name)
			if _, err := vcenters[0].Finder.VirtualMachine(context.Background(), tempName); err == nil {
				t.Errorf("createNewVMTemplate() should not have created a %q temp VM %q; no rebuild should have occurred", prefix, tempName)
			}
		}
	})

	t.Run("providerSpec.Template is outdated and computed name has no current template: rebuilds and converges to computed name", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)

		customName := "my-outdated-custom-template"
		createTestVM(t, vcenters[0], fd, customName)
		setVMProductVersion(t, vcenters[0], customName, "416.94.20240101")
		// The version mismatch drives createNewVMTemplate into the real OVA rebuild path, which
		// tags the freshly-imported VM using infra.Status.InfrastructureName as the AttachTag
		// "tagID" argument; see the identical fixture on the "existing template version mismatch"
		// subtest above for why the tag must actually exist in the simulator.
		createInfraTag(t, vcenters[0], infra.Status.InfrastructureName)

		ps := buildVSphereProviderSpec(vcenters[0], fd, customName)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		gotName, patchRequired, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err != nil {
			t.Fatalf("createNewVMTemplate() unexpected error: %v", err)
		}
		if !patchRequired || gotName != name {
			t.Errorf("createNewVMTemplate() = (%q, %v), want (%q, true) (outdated custom template should be rebuilt into the computed name)", gotName, patchRequired, name)
		}

		if _, err := vcenters[0].Finder.VirtualMachine(context.Background(), name); err != nil {
			t.Errorf("createNewVMTemplate() did not leave a VM named %q behind: %v", name, err)
		}
		tempName := AtomicTempName("mco-tmp", name)
		if _, err := vcenters[0].Finder.VirtualMachine(context.Background(), tempName); err == nil {
			t.Errorf("createNewVMTemplate() left a stale temp VM behind under %q", tempName)
		}
	})

	t.Run("template not found but mco-old rollback VM present: recovers from a mid-swap crash", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		oldTempName := AtomicTempName("mco-old", name)
		// Simulate a crash between the two renames of a prior atomic swap: the rollback VM
		// exists under its temp name, but nothing exists yet under the production name.
		createTestVM(t, vcenters[0], fd, oldTempName)
		setVMProductVersion(t, vcenters[0], oldTempName, release)

		ps := buildVSphereProviderSpec(vcenters[0], fd, name)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		_, _, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err != nil {
			t.Fatalf("createNewVMTemplate() unexpected error: %v", err)
		}

		if _, err := vcenters[0].Finder.VirtualMachine(context.Background(), name); err != nil {
			t.Errorf("createNewVMTemplate() did not recover the rollback VM under %q: %v", name, err)
		}
		if _, err := vcenters[0].Finder.VirtualMachine(context.Background(), oldTempName); err == nil {
			t.Errorf("createNewVMTemplate() left the rollback VM behind under %q", oldTempName)
		}
	})

	t.Run("template not found and no rollback VM present: error", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		ps := buildVSphereProviderSpec(vcenters[0], fd, name)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		_, _, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err == nil || !strings.Contains(err.Error(), "failed to determine disk provisioning type") {
			t.Fatalf("createNewVMTemplate() expected a disk-provisioning-type error, got: %v", err)
		}
	})

	t.Run("existing template has no product info: error", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		createTestVM(t, vcenters[0], fd, name)
		clearVMProduct(t, vcenters[0], name)

		ps := buildVSphereProviderSpec(vcenters[0], fd, name)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		_, _, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err == nil || !strings.Contains(err.Error(), "unable to determine RHCOS version") {
			t.Fatalf("createNewVMTemplate() expected an RHCOS-version error, got: %v", err)
		}
	})

	t.Run("existing template has empty product version: error", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		fd := buildFailureDomain(vcenters[0], "fd0")
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		createTestVM(t, vcenters[0], fd, name)
		setVMProductVersion(t, vcenters[0], name, "")

		ps := buildVSphereProviderSpec(vcenters[0], fd, name)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		_, _, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err == nil || !strings.Contains(err.Error(), "unable to determine RHCOS version") {
			t.Fatalf("createNewVMTemplate() expected an RHCOS-version error, got: %v", err)
		}
	})

	t.Run("computed name exceeds 80 characters: error before touching vSphere", func(t *testing.T) {
		vcenters := newSimulatedVCenters(t, 1)
		longFDName := strings.Repeat("x", 80)
		fd := buildFailureDomain(vcenters[0], longFDName)
		infra := buildVSphereInfra("infra1", vcenters, []osconfigv1.VSpherePlatformFailureDomainSpec{fd})
		secret := buildVSphereCredsSecret(vcenters)

		name := fmt.Sprintf("%s-rhcos-%s", infra.Status.InfrastructureName, fd.Name)
		if len(name) <= 80 {
			t.Fatalf("test setup bug: computed name %q is not over 80 characters", name)
		}
		// No VM created under `name` at all: findAllRequiredResources's rollback-recovery path
		// would fail first if we didn't have a version mismatch to reach the length check, so
		// instead create the VM under the (too-long) name directly to reach the version-compare
		// and length-check code first.
		createTestVM(t, vcenters[0], fd, name)
		setVMProductVersion(t, vcenters[0], name, "416.94.20240101") // force a version mismatch

		ps := buildVSphereProviderSpec(vcenters[0], fd, name)
		streamData := newTestOVAStream(t, testArch, release)

		vcenters[0].activate()
		_, _, err := createNewVMTemplate(streamData, ps, infra, secret, buildStubIgnitionKubeClient(t), testArch, release)
		if err == nil || !strings.Contains(err.Error(), "exceeds the permitted limit") {
			t.Fatalf("createNewVMTemplate() expected an 80-character-limit error, got: %v", err)
		}
	})
}
