package bootimage

import (
	"testing"

	"github.com/coreos/stream-metadata-go/stream"
	"github.com/coreos/stream-metadata-go/stream/rhcos"
	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHotLoop(t *testing.T) {
	cases := []struct {
		name                  string
		machineset            *machinev1beta1.MachineSet
		generateBootImageFunc func(string) string
		updateCount           int
		expectHotLoop         bool
	}{
		{
			name:                  "Hot loop detected due to multiple updates to same value",
			machineset:            getMachineSet("machine-set-1", "boot-image-1"),
			updateCount:           HotLoopLimit + 1,
			generateBootImageFunc: func(s string) string { return s },
			expectHotLoop:         true,
		},
		{
			name:                  "Hot loop not detected due to update count being lower than threshold",
			machineset:            getMachineSet("machine-set-1", "boot-image-1"),
			generateBootImageFunc: func(s string) string { return s },
			updateCount:           HotLoopLimit - 1,
			expectHotLoop:         false,
		},
		{
			name:                  "Hot loop not detected due to target boot image changing",
			machineset:            getMachineSet("machine-set-1", "boot-image-1"),
			generateBootImageFunc: func(s string) string { return s + "-1" },
			updateCount:           HotLoopLimit + 1,
			expectHotLoop:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := &Controller{
				mapiBootImageState: map[string]BootImageState{},
			}
			// checkMAPIMachineSetHotLoop is only called in the controller when a patch is required,
			// so every call to it is assuming a boot image update is going to take place.

			// No hot loops should be detected in the first (updateCount - 1) calls
			var hotLoopDetected bool
			for range tc.updateCount - 1 {
				hotLoopDetected = ctrl.checkMAPIMachineSetHotLoop(tc.machineset)
				assert.Equal(t, false, hotLoopDetected)
				// Change target boot image for next iteration
				setMachineSetBootImage(tc.machineset, tc.generateBootImageFunc)
			}
			// Check for hot loop on the last iteration
			hotLoopDetected = ctrl.checkMAPIMachineSetHotLoop(tc.machineset)
			assert.Equal(t, tc.expectHotLoop, hotLoopDetected)
		})
	}
}

// Returns a machineset with a given boot image
func getMachineSet(name, bootImage string) *machinev1beta1.MachineSet {
	return &machinev1beta1.MachineSet{
		ObjectMeta: v1.ObjectMeta{
			Name: name,
		},
		Spec: machinev1beta1.MachineSetSpec{
			Template: machinev1beta1.MachineTemplateSpec{
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: machinev1beta1.ProviderSpec{
						Value: &runtime.RawExtension{
							Raw: []byte(bootImage),
						},
					},
				},
			},
		},
	}
}

// Sets the boot image in a machineset based on the generate function passed in
func setMachineSetBootImage(machineset *machinev1beta1.MachineSet, generateBootImageFunc func(string) string) {
	currentBootImage := string(machineset.Spec.Template.Spec.ProviderSpec.Value.Raw)
	machineset.Spec.Template.Spec.ProviderSpec.Value.Raw = []byte(generateBootImageFunc(currentBootImage))
}

func TestGetArchFromMachineSet(t *testing.T) {
	cases := []struct {
		name         string
		annotations  map[string]string
		expectedArch string
		expectError  bool
	}{
		{
			name: "Single architecture label",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "kubernetes.io/arch=amd64",
			},
			expectedArch: "x86_64",
			expectError:  false,
		},
		{
			name: "Multiple labels with architecture first",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "kubernetes.io/arch=amd64,topology.ebs.csi.aws.com/zone=eu-central-1a",
			},
			expectedArch: "x86_64",
			expectError:  false,
		},
		{
			name: "Multiple labels with architecture last",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "topology.ebs.csi.aws.com/zone=eu-central-1a,kubernetes.io/arch=arm64",
			},
			expectedArch: "aarch64",
			expectError:  false,
		},
		{
			name: "Multiple labels with architecture in middle",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "topology.ebs.csi.aws.com/zone=eu-central-1a,kubernetes.io/arch=s390x,node.kubernetes.io/instance-type=m5.large",
			},
			expectedArch: "s390x",
			expectError:  false,
		},
		{
			name: "Multiple labels with spaces",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: " topology.ebs.csi.aws.com/zone=eu-central-1a , kubernetes.io/arch=ppc64le , node.kubernetes.io/instance-type=m5.large ",
			},
			expectedArch: "ppc64le",
			expectError:  false,
		},
		{
			name: "Invalid architecture",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "kubernetes.io/arch=invalid-arch",
			},
			expectError: true,
		},
		{
			name: "No architecture label",
			annotations: map[string]string{
				MachineSetArchAnnotationKey: "topology.ebs.csi.aws.com/zone=eu-central-1a,node.kubernetes.io/instance-type=m5.large",
			},
			expectError: true,
		},
		{
			name:         "No annotation",
			annotations:  map[string]string{},
			expectedArch: "", // Will default to control plane arch, but we can't test that easily
			expectError:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			machineSet := &machinev1beta1.MachineSet{
				ObjectMeta: v1.ObjectMeta{
					Name:        "test-machineset",
					Annotations: tc.annotations,
				},
			}

			arch, err := getArchFromMachineSet(machineSet)

			if tc.expectError {
				assert.Error(t, err, "Expected error for test case: %s", tc.name)
			} else {
				assert.NoError(t, err, "Unexpected error for test case: %s", tc.name)
				if tc.expectedArch != "" {
					assert.Equal(t, tc.expectedArch, arch, "Architecture mismatch for test case: %s", tc.name)
				}
			}
		})
	}
}

// This tests reconcileAzureProviderSpec against the various stream variants.
// See comments in reconcileAzureProviderSpec() for additional context.
func TestReconcileAzureProviderSpec(t *testing.T) {
	streamData := &stream.Stream{
		Architectures: map[string]stream.Arch{
			"x86_64": {
				RHELCoreOSExtensions: &rhcos.Extensions{
					Marketplace: &rhcos.Marketplace{
						Azure: &rhcos.AzureMarketplace{
							NoPurchasePlan: &rhcos.AzureMarketplaceImages{
								Gen1: &rhcos.AzureMarketplaceImage{
									Offer:     "aro4",
									Publisher: "azureopenshift",
									SKU:       "aro_419",
									Version:   "419.94.20250101",
								},
								Gen2: &rhcos.AzureMarketplaceImage{
									Offer:     "aro4",
									Publisher: "azureopenshift",
									SKU:       "419-v2",
									Version:   "419.94.20250101",
								},
							},
							OCP: &rhcos.AzureMarketplaceImages{
								Gen1: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat",
									Offer:     "rh-ocp-worker",
									SKU:       "rh-ocp-worker-gen1",
									Version:   "4.18.2025031114",
								},
								Gen2: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat",
									Offer:     "rh-ocp-worker",
									SKU:       "rh-ocp-worker",
									Version:   "4.18.2025031114",
								},
							},
							OPP: &rhcos.AzureMarketplaceImages{
								Gen1: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat",
									Offer:     "rh-opp-worker",
									SKU:       "rh-opp-worker-gen1",
									Version:   "4.18.2025031114",
								},
								Gen2: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat",
									Offer:     "rh-opp-worker",
									SKU:       "rh-opp-worker",
									Version:   "4.18.2025031114",
								},
							},
							OKE: &rhcos.AzureMarketplaceImages{
								Gen1: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat",
									Offer:     "rh-oke-worker",
									SKU:       "rh-oke-worker-gen1",
									Version:   "4.18.2025031114",
								},
								Gen2: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat",
									Offer:     "rh-oke-worker",
									SKU:       "rh-oke-worker",
									Version:   "4.18.2025031114",
								},
							},
							OCPEMEA: &rhcos.AzureMarketplaceImages{
								Gen1: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat-limited",
									Offer:     "rh-ocp-worker",
									SKU:       "rh-ocp-worker-gen1",
									Version:   "4.18.2025031114",
								},
								Gen2: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat-limited",
									Offer:     "rh-ocp-worker",
									SKU:       "rh-ocp-worker",
									Version:   "4.18.2025031114",
								},
							},
							OPPEMEA: &rhcos.AzureMarketplaceImages{
								Gen1: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat-limited",
									Offer:     "rh-opp-worker",
									SKU:       "rh-opp-worker-gen1",
									Version:   "4.18.2025031114",
								},
								Gen2: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat-limited",
									Offer:     "rh-opp-worker",
									SKU:       "rh-opp-worker",
									Version:   "4.18.2025031114",
								},
							},
							OKEEMEA: &rhcos.AzureMarketplaceImages{
								Gen1: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat-limited",
									Offer:     "rh-oke-worker",
									SKU:       "rh-oke-worker-gen1",
									Version:   "4.18.2025031114",
								},
								Gen2: &rhcos.AzureMarketplaceImage{
									Publisher: "redhat-limited",
									Offer:     "rh-oke-worker",
									SKU:       "rh-oke-worker",
									Version:   "4.18.2025031114",
								},
							},
						},
					},
				},
			},
			"aarch64": {
				RHELCoreOSExtensions: &rhcos.Extensions{
					Marketplace: &rhcos.Marketplace{
						Azure: &rhcos.AzureMarketplace{
							NoPurchasePlan: &rhcos.AzureMarketplaceImages{
								Gen2: &rhcos.AzureMarketplaceImage{
									Offer:     "aro4",
									Publisher: "azureopenshift",
									SKU:       "419-arm",
									Version:   "419.94.20250101",
								},
							},
						},
					},
				},
			},
		},
	}

	// Create a fake secret with ignition data
	testSecret := &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "openshift-machine-api",
		},
		Data: map[string][]byte{
			"userData": []byte(`{"ignition":{"version":"3.4.0"},"storage":{"files":[]},"systemd":{},"passwd":{}}`),
		},
	}

	fakeClient := fake.NewSimpleClientset(testSecret)

	tests := []struct {
		name            string
		arch            string
		currentImage    machinev1beta1.Image
		expectedImage   machinev1beta1.Image
		expectPatch     bool
		expectSkip      bool
		streamData      *stream.Stream                         // Custom stream data for specific tests
		securityProfile *machinev1beta1.SecurityProfile // Custom security profile for specific tests
	}{
		{
			name: "Legacy Gen1 upload image transitions to marketplace Gen1",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				ResourceID: "test-gen1-image",
			},
			expectedImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "aro_419",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectPatch: true,
		},
		{
			name: "Legacy Gen2 upload image transitions to marketplace Gen2",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				ResourceID: "test-gen2-image",
			},
			expectedImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectPatch: true,
		},
		{
			name: "Marketplace Gen1 image updates to newer version",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "aro_418",
				Version:    "418.94.20241201",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectedImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "aro_419",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectPatch: true,
		},
		{
			name: "Marketplace Gen2 image updates to newer version",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "418-v2",
				Version:    "418.94.20241201",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectedImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectPatch: true,
		},
		{
			name: "ARM64 always uses Gen2",
			arch: "aarch64",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "aro_418", // this can't realistically happen, but a Gen1 SKU but should be forced to arm SKU
				Version:    "418.94.20241201",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectedImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-arm", // Should be Gen2 SKU
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectPatch: true,
		},
		{
			name: "No update needed - image already current",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectedImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectPatch: false,
		},
		{
			name: "Skip unsupported architecture ppc64le",
			arch: "ppc64le",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectSkip: true,
		},
		{
			name: "Skip unsupported architecture s390x",
			arch: "s390x",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectSkip: true,
		},
		{
			name: "Paid OCP Gen1 image updates to newer version",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Publisher:  "redhat",
				Offer:      "rh-ocp-worker",
				ResourceID: "",
				SKU:        "rh-ocp-worker-gen1",
				Version:    "4.17.2024121214",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectedImage: machinev1beta1.Image{
				Publisher:  "redhat",
				Offer:      "rh-ocp-worker",
				ResourceID: "",
				SKU:        "rh-ocp-worker-gen1",
				Version:    "4.18.2025031114",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectPatch: true,
		},
		{
			name: "Paid OCP Gen2 image updates to newer version",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Publisher:  "redhat",
				Offer:      "rh-ocp-worker",
				ResourceID: "",
				SKU:        "rh-ocp-worker",
				Version:    "4.17.2024121214",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectedImage: machinev1beta1.Image{
				Publisher:  "redhat",
				Offer:      "rh-ocp-worker",
				ResourceID: "",
				SKU:        "rh-ocp-worker",
				Version:    "4.18.2025031114",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectPatch: true,
		},
		{
			name: "Paid OPP EMEA Gen1 image updates to newer version",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Publisher:  "redhat-limited",
				Offer:      "rh-opp-worker",
				ResourceID: "",
				SKU:        "rh-opp-worker-gen1",
				Version:    "4.17.2024121214",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectedImage: machinev1beta1.Image{
				Publisher:  "redhat-limited",
				Offer:      "rh-opp-worker",
				ResourceID: "",
				SKU:        "rh-opp-worker-gen1",
				Version:    "4.18.2025031114",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectPatch: true,
		},
		{
			name: "Paid OKE EMEA Gen2 image updates to newer version",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Publisher:  "redhat-limited",
				Offer:      "rh-oke-worker",
				ResourceID: "",
				SKU:        "rh-oke-worker",
				Version:    "4.17.2024121214",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectedImage: machinev1beta1.Image{
				Publisher:  "redhat-limited",
				Offer:      "rh-oke-worker",
				ResourceID: "",
				SKU:        "rh-oke-worker",
				Version:    "4.18.2025031114",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectPatch: true,
		},
		{
			name: "Paid OCP image already current - no update needed",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Publisher:  "redhat",
				Offer:      "rh-ocp-worker",
				ResourceID: "",
				SKU:        "rh-ocp-worker",
				Version:    "4.18.2025031114",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectedImage: machinev1beta1.Image{
				Publisher:  "redhat",
				Offer:      "rh-ocp-worker",
				ResourceID: "",
				SKU:        "rh-ocp-worker",
				Version:    "4.18.2025031114",
				Type:       machinev1beta1.AzureImageTypeMarketplaceWithPlan,
			},
			expectPatch: false,
		},
		{
			name: "Error when marketplace extensions are nil",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Publisher:  "azureopenshift",
				Offer:      "aro4",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectSkip: true,
			streamData: &stream.Stream{
				Architectures: map[string]stream.Arch{
					"x86_64": {
						RHELCoreOSExtensions: &rhcos.Extensions{
							Marketplace: nil, // Marketplace is nil
						},
					},
				},
			},
		},
		{
			name: "Error when Azure marketplace is nil",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Publisher:  "azureopenshift",
				Offer:      "aro4",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectSkip: true,
			streamData: &stream.Stream{
				Architectures: map[string]stream.Arch{
					"x86_64": {
						RHELCoreOSExtensions: &rhcos.Extensions{
							Marketplace: &rhcos.Marketplace{
								Azure: nil, // Azure marketplace is nil
							},
						},
					},
				},
			},
		},
		{
			name: "Skip machineset with ConfidentialVM SecurityType",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectSkip: true,
			securityProfile: &machinev1beta1.SecurityProfile{
				Settings: machinev1beta1.SecuritySettings{
					SecurityType: "ConfidentialVM",
				},
			},
		},
		{
			name: "Skip machineset with TrustedLaunch SecurityType",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectSkip: true,
			securityProfile: &machinev1beta1.SecurityProfile{
				Settings: machinev1beta1.SecuritySettings{
					SecurityType: "TrustedLaunch",
				},
			},
		},
		{
			name: "Process machineset with SecurityProfile but empty SecurityType",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "418-v2",
				Version:    "418.94.20241201",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectedImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectPatch: true,
			securityProfile: &machinev1beta1.SecurityProfile{
				Settings: machinev1beta1.SecuritySettings{
					SecurityType: "", // Empty SecurityType should not be skipped
				},
			},
		},
		{
			name: "Process machineset with nil SecurityProfile",
			arch: "x86_64",
			currentImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "418-v2",
				Version:    "418.94.20241201",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectedImage: machinev1beta1.Image{
				Offer:      "aro4",
				Publisher:  "azureopenshift",
				ResourceID: "",
				SKU:        "419-v2",
				Version:    "419.94.20250101",
				Type:       machinev1beta1.AzureImageTypeMarketplaceNoPlan,
			},
			expectPatch:     true,
			securityProfile: nil, // Nil SecurityProfile should not be skipped
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create provider spec with current image
			providerSpec := &machinev1beta1.AzureMachineProviderSpec{
				Image: tt.currentImage,
				UserDataSecret: &corev1.SecretReference{
					Name: "test-secret",
				},
				SecurityProfile: tt.securityProfile,
			}

			// Create a mock infrastructure object
			infra := &osconfigv1.Infrastructure{}

			// Use custom stream data if provided, otherwise use default
			testStreamData := streamData
			if tt.streamData != nil {
				testStreamData = tt.streamData
			}

			patchRequired, updatedProviderSpec, err := reconcileAzureProviderSpec(
				testStreamData,
				tt.arch,
				infra,
				providerSpec,
				"test-machineset",
				fakeClient,
			)

			require.NoError(t, err)

			if tt.expectSkip {
				assert.False(t, patchRequired, "Expected no patch for skipped case")
				return
			}

			assert.Equal(t, tt.expectPatch, patchRequired, "Patch required mismatch")

			if tt.expectPatch {
				require.NotNil(t, updatedProviderSpec, "Updated provider spec should not be nil when patch is required")
				assert.Equal(t, tt.expectedImage, updatedProviderSpec.Image, "Updated image mismatch")
			}
		})
	}
}
