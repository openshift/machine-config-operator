package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"sigs.k8s.io/yaml"

	apicfgv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcoResourceRead "github.com/openshift/machine-config-operator/lib/resourceread"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
)

func TestParseManifests(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []manifest
	}{{
		name: "ingress",
		raw: `
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: test-ingress
  namespace: test-namespace
spec:
  rules:
  - http:
      paths:
      - path: /testpath
        backend:
          serviceName: test
          servicePort: 80
`,
		want: []manifest{{
			Raw: []byte(`{"apiVersion":"extensions/v1beta1","kind":"Ingress","metadata":{"name":"test-ingress","namespace":"test-namespace"},"spec":{"rules":[{"http":{"paths":[{"backend":{"serviceName":"test","servicePort":80},"path":"/testpath"}]}}]}}`),
		}},
	}, {
		name: "feature gate",
		raw: `
apiVersion: config.openshift.io/v1
kind: FeatureGate
metadata:
  name: cluster
spec:
  featureSet: TechPreviewNoUpgrade
`,
		want: []manifest{{
			Raw: []byte(`{"apiVersion":"config.openshift.io/v1","kind":"FeatureGate","metadata":{"name":"cluster"},"spec":{"featureSet":"TechPreviewNoUpgrade"}}`),
		}},
	}, {
		name: "two-resources",
		raw: `
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: test-ingress
  namespace: test-namespace
spec:
  rules:
  - http:
      paths:
      - path: /testpath
        backend:
          serviceName: test
          servicePort: 80
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: a-config
  namespace: default
data:
  color: "red"
  multi-line: |
    hello world
    how are you?
`,
		want: []manifest{{
			Raw: []byte(`{"apiVersion":"extensions/v1beta1","kind":"Ingress","metadata":{"name":"test-ingress","namespace":"test-namespace"},"spec":{"rules":[{"http":{"paths":[{"backend":{"serviceName":"test","servicePort":80},"path":"/testpath"}]}}]}}`),
		}, {
			Raw: []byte(`{"apiVersion":"v1","data":{"color":"red","multi-line":"hello world\nhow are you?\n"},"kind":"ConfigMap","metadata":{"name":"a-config","namespace":"default"}}`),
		}},
	}, {
		name: "two-resources-with-empty",
		raw: `
---
apiVersion: extensions/v1beta1
kind: Ingress
metadata:
  name: test-ingress
  namespace: test-namespace
spec:
  rules:
  - http:
      paths:
      - path: /testpath
        backend:
          serviceName: test
          servicePort: 80
---
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: a-config
  namespace: default
data:
  color: "red"
  multi-line: |
    hello world
    how are you?
---
`,
		want: []manifest{{
			Raw: []byte(`{"apiVersion":"extensions/v1beta1","kind":"Ingress","metadata":{"name":"test-ingress","namespace":"test-namespace"},"spec":{"rules":[{"http":{"paths":[{"backend":{"serviceName":"test","servicePort":80},"path":"/testpath"}]}}]}}`),
		}, {
			Raw: []byte(`{"apiVersion":"v1","data":{"color":"red","multi-line":"hello world\nhow are you?\n"},"kind":"ConfigMap","metadata":{"name":"a-config","namespace":"default"}}`),
		}},
	}, {
		name: "container-runtime-bootstrap",
		raw: `
---
apiVersion: machineconfiguration.openshift.io/v1
kind: ContainerRuntimeConfig
metadata:
  name: cr-pid-limit
spec:
  machineConfigPoolSelector:
    matchLabels:
      pools.operator.machineconfiguration.openshift.io/master: ''
  containerRuntimeConfig:
    pidsLimit: 100000
---
`,
		want: []manifest{{
			Raw: []byte(`{"apiVersion":"machineconfiguration.openshift.io/v1","kind":"ContainerRuntimeConfig","metadata":{"name":"cr-pid-limit"},"spec":{"containerRuntimeConfig":{"pidsLimit":100000},"machineConfigPoolSelector":{"matchLabels":{"pools.operator.machineconfiguration.openshift.io/master":""}}}}`),
		}},
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseManifests("dummy-file-name", strings.NewReader(test.raw))
			if err != nil {
				t.Fatalf("failed to parse manifest: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mismatch found %s", diff.Diff(got, test.want))
			}
		})
	}
}

// Implements a fake ImageStreamFactory.
type fakeImageStreamFactory struct {
	// The OSImageStream to return.
	stream *mcfgv1.OSImageStream
	// Whether the Create method was called.
	createCalled bool
	// The CreateOptions passed to the last Create call.
	lastCreateOptions osimagestream.CreateOptions
}

func (f *fakeImageStreamFactory) Create(_ context.Context, _ imageutils.SysContextFactory, createOptions osimagestream.CreateOptions) (*mcfgv1.OSImageStream, error) {
	f.createCalled = true
	f.lastCreateOptions = createOptions
	return f.stream, nil
}

// Instantiates a new instance of the Bootstrap struct for testing. This also
// does the following:
// 1. Copies the data from testdata/bootstrap into a temp directory so that it
// may be safely overwritten to test specific scenarios.
// 2. Creates a fake ImageStreamFactory instance and wires it up to return an
// OSImageStream.
func setupForBootstrapTest(t *testing.T) (*Bootstrap, *fakeImageStreamFactory, string, string) {
	t.Helper()

	srcDir := t.TempDir()
	destDir := t.TempDir()

	require.NoError(t, exec.Command("cp", "-r", "testdata/bootstrap/.", srcDir).Run())

	fakeFactory := &fakeImageStreamFactory{
		stream: &mcfgv1.OSImageStream{
			Status: mcfgv1.OSImageStreamStatus{
				AvailableStreams: []mcfgv1.OSImageStreamSet{
					{
						Name:              "stream-1",
						OSImage:           mcfgv1.ImageDigestFormat("registry.host.com/os:latest"),
						OSExtensionsImage: mcfgv1.ImageDigestFormat("registry.host.com/extensions:latest"),
					},
				},
				DefaultStream: "stream-1",
			},
		},
	}

	bootstrap := New("../../../templates", srcDir, filepath.Join(srcDir, "machineconfigcontroller-pull-secret"), &noopImagesInspectorFactory{})
	bootstrap.imageStreamFactory = fakeFactory

	return bootstrap, fakeFactory, srcDir, destDir
}

type noopImagesInspectorFactory struct{}

func (n *noopImagesInspectorFactory) ForContext(_ imageutils.SysContextFactory) osimagestream.ImagesInspector {
	return &noopImagesInspector{}
}

type noopImagesInspector struct{}

func (n *noopImagesInspector) Inspect(_ context.Context, _ ...string) ([]imageutils.BulkInspectResult, error) {
	return []imageutils.BulkInspectResult{{}}, nil
}

func (n *noopImagesInspector) FetchImageFile(_ context.Context, _, _ string) ([]byte, error) {
	return nil, nil
}

// TestBootstrapRunHypershift validates OSImageStream behavior under
// ExternalTopologyMode (HyperShift). The ExternalTopologyMode guard was
// removed in CNTRLPLANE-3840 because HyperShift now writes
// 99_osimagestream.yaml into the MCC template directory
// (openshift/hypershift#8792).
//
// NOTE for feature gate graduation: when OSStreams is promoted to GA and
// the feature gate check is removed, these tests should still pass since
// the fixture has OSStreams enabled. If the fixture changes, update
// accordingly.
func TestBootstrapRunHypershift(t *testing.T) {
	disabledFG := apicfgv1.FeatureGate{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apicfgv1.GroupVersion.String(),
			Kind:       "FeatureGate",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: apicfgv1.FeatureGateStatus{
			FeatureGates: []apicfgv1.FeatureGateDetails{{
				Version: "0.0.1-snapshot",
				Enabled: []apicfgv1.FeatureGateAttributes{
					{Name: "OpenShiftPodSecurityAdmission"},
				},
				Disabled: []apicfgv1.FeatureGateAttributes{
					{Name: "OSStreams"},
				},
			}},
		},
	}

	testCases := []struct {
		name                   string
		featureGateOverride    *apicfgv1.FeatureGate
		expectCreate           bool
		expectOSImages         bool
		expectReleaseImageUsed bool
	}{
		{
			name:                   "When OSStreams feature gate is enabled it should consume OSImageStream via ReleaseImage fallback",
			expectCreate:           true,
			expectOSImages:         true,
			expectReleaseImageUsed: true,
		},
		{
			name:                "When OSStreams feature gate is disabled it should not consume OSImageStream",
			featureGateOverride: &disabledFG,
			expectCreate:        false,
			expectOSImages:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bootstrap, fakeFactory, srcDir, destDir := setupForBootstrapTest(t)

			require.NoError(t, exec.Command("cp", "testdata/bootstrap-hypershift/machineconfigcontroller-controllerconfig.yaml", srcDir).Run())

			if tc.featureGateOverride != nil {
				fgBytes, err := yaml.Marshal(tc.featureGateOverride)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(srcDir, "featuregate.yaml"), fgBytes, 0644))
			}

			err := bootstrap.Run(destDir)
			require.NoError(t, err)

			assert.Equal(t, tc.expectCreate, fakeFactory.createCalled)
			cconfigBytes, err := os.ReadFile(filepath.Join(destDir, "controller-config", "machine-config-controller.yaml"))
			require.NoError(t, err)

			if tc.expectOSImages {
				assert.Contains(t, string(cconfigBytes), "baseOSContainerImage: registry.host.com/os:latest")
				assert.Contains(t, string(cconfigBytes), "baseOSExtensionsContainerImage: registry.host.com/extensions:latest")
			} else {
				assert.NotContains(t, string(cconfigBytes), "baseOSContainerImage: registry.host.com/os:latest")
				assert.NotContains(t, string(cconfigBytes), "baseOSExtensionsContainerImage: registry.host.com/extensions:latest")
			}

			if tc.expectReleaseImageUsed {
				// HyperShift has no ImageStream in manifests, so fetchOSImageStream
				// must fall back to cconfig.Spec.ReleaseImage for network-based discovery.
				assert.Nil(t, fakeFactory.lastCreateOptions.ReleaseImageStream,
					"ReleaseImageStream should be nil in HyperShift — no ImageStream in manifests")
				assert.NotEmpty(t, fakeFactory.lastCreateOptions.ReleaseImage,
					"ReleaseImage should be set from ControllerConfig.Spec.ReleaseImage as fallback")
			}
		})
	}
}

func TestBootstrapRun(t *testing.T) {
	bootstrap, fakeFactory, _, destDir := setupForBootstrapTest(t)

	err := bootstrap.Run(destDir)
	require.NoError(t, err)

	for _, poolName := range []string{"master", "worker"} {
		t.Run(poolName, func(t *testing.T) {
			paths, err := filepath.Glob(filepath.Join(destDir, "machine-configs", fmt.Sprintf("rendered-%s-*.yaml", poolName)))
			require.NoError(t, err)
			require.Len(t, paths, 1)
			mcBytes, err := os.ReadFile(paths[0])
			require.NoError(t, err)
			mc, err := mcoResourceRead.ReadMachineConfigV1(mcBytes)
			require.NoError(t, err)

			// Ensure that generated registries.conf corresponds to the testdata ImageContentSourcePolicy
			var registriesConfig *ign3types.File
			ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
			require.NoError(t, err)
			for i := range ignCfg.Storage.Files {
				f := &ignCfg.Storage.Files[i]
				if f.Path == "/etc/containers/registries.conf" {
					registriesConfig = f
				}
				require.False(t, f.Path == "/etc/kubernetes/kubelet-ca.crt")
			}
			require.NotNil(t, registriesConfig)
			ignContents, err := ctrlcommon.DecodeIgnitionFileContents(registriesConfig.Contents.Source, registriesConfig.Contents.Compression)
			require.NoError(t, err)
			// Only a minimal presence check; more comprehensive tests that the contents correspond to the ICSP semantics are
			// maintained in pkg/controller/container-runtime-config.
			assert.Contains(t, string(ignContents), "registry.mirror.example.com/ocp")
			assert.Contains(t, string(ignContents), "insecure-reg-1.io")
			assert.Contains(t, string(ignContents), "insecure-reg-2.io")
			assert.Contains(t, string(ignContents), "blocked-reg.io")
			assert.NotContains(t, string(ignContents), "release-registry.product.example.org")

			// Ensure that the values from the OSImageStream are populated into the ControllerConfig.
			assert.True(t, fakeFactory.createCalled)
			cconfigBytes, err := os.ReadFile(filepath.Join(destDir, "controller-config", "machine-config-controller.yaml"))
			require.NoError(t, err)
			assert.Contains(t, string(cconfigBytes), "baseOSContainerImage: registry.host.com/os:latest")
			assert.Contains(t, string(cconfigBytes), "baseOSExtensionsContainerImage: registry.host.com/extensions:latest")
		})
	}
}

// Check pruning and rendering for self-managed and HyperShift bootstrap.
func TestBootstrapTLSGroupsFeatureGate(t *testing.T) {
	modern := &apicfgv1.TLSSecurityProfile{Type: apicfgv1.TLSProfileModernType, Modern: &apicfgv1.ModernTLSProfile{}}
	custom := customProfileWithGroups(apicfgv1.TLSGroupX25519MLKEM768, apicfgv1.TLSGroupSecP256r1)
	override := customProfileWithGroups(apicfgv1.TLSGroupX25519MLKEM768, apicfgv1.TLSGroupSecP384r1)

	for _, hosted := range []bool{false, true} {
		for _, test := range []struct {
			name       string
			gate       bool
			fips       bool
			profile    *apicfgv1.TLSSecurityProfile
			override   *apicfgv1.TLSSecurityProfile
			rawCurves  bool
			wantMaster []int32
			wantWorker []int32
		}{
			{name: "unset profile, gate disabled"},
			{name: "unset profile, gate enabled", gate: true, wantMaster: []int32{4588, 29, 23, 24}, wantWorker: []int32{4588, 29, 23, 24}},
			{name: "named profile, gate disabled", profile: modern},
			{name: "named profile, gate enabled", gate: true, profile: modern, wantMaster: []int32{4588, 29, 23, 24}, wantWorker: []int32{4588, 29, 23, 24}},
			{name: "named profile, FIPS target", gate: true, fips: true, profile: modern, wantMaster: []int32{23, 24}, wantWorker: []int32{23, 24}},
			{name: "Custom profiles are pruned with gate disabled", profile: custom, override: override},
			{name: "gate disabled preserves raw curves and prunes Custom groups", profile: custom, override: override, rawCurves: true, wantWorker: []int32{23, 29}},
			{name: "Custom override, gate enabled", gate: true, profile: custom, override: override, wantMaster: []int32{4588, 23}, wantWorker: []int32{4588, 24}},
			{name: "Custom override, FIPS target", gate: true, fips: true, profile: custom, override: override, wantMaster: []int32{23}, wantWorker: []int32{24}},
			{name: "named override, gate enabled", gate: true, profile: custom, override: modern, wantMaster: []int32{4588, 23}, wantWorker: []int32{4588, 29, 23, 24}},
			{name: "named override, FIPS target", gate: true, fips: true, profile: custom, override: modern, wantMaster: []int32{23}, wantWorker: []int32{23, 24}},
		} {
			t.Run(fmt.Sprintf("hosted=%t/%s", hosted, test.name), func(t *testing.T) {
				bootstrap, _, srcDir, destDir := setupForBootstrapTest(t)
				if hosted {
					require.NoError(t, exec.Command("cp", "testdata/bootstrap-hypershift/machineconfigcontroller-controllerconfig.yaml", srcDir).Run())
				}
				writeManifest := func(name string, obj interface{}) {
					t.Helper()
					data, err := yaml.Marshal(obj)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(srcDir, name), data, 0644))
				}

				data, err := os.ReadFile(filepath.Join(srcDir, "featuregate.yaml"))
				require.NoError(t, err)
				var fg apicfgv1.FeatureGate
				require.NoError(t, yaml.Unmarshal(data, &fg))
				gate := apicfgv1.FeatureGateAttributes{Name: features.FeatureGateTLSGroupPreferences}
				if test.gate {
					fg.Status.FeatureGates[0].Enabled = append(fg.Status.FeatureGates[0].Enabled, gate)
				} else {
					fg.Status.FeatureGates[0].Disabled = append(fg.Status.FeatureGates[0].Disabled, gate)
				}
				writeManifest("featuregate.yaml", &fg)
				writeManifest("apiserver.yaml", &apicfgv1.APIServer{
					TypeMeta:   metav1.TypeMeta{APIVersion: apicfgv1.GroupVersion.String(), Kind: "APIServer"},
					ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName},
					Spec:       apicfgv1.APIServerSpec{TLSSecurityProfile: test.profile},
				})
				if test.override != nil {
					raw := []byte(`{"maxPods":123}`)
					if test.rawCurves {
						raw = []byte(`{"maxPods":123,"tlsCurvePreferences":[23,29]}`)
					}
					writeManifest("kubeletconfig.yaml", &mcfgv1.KubeletConfig{
						TypeMeta:   metav1.TypeMeta{APIVersion: mcfgv1.GroupVersion.String(), Kind: "KubeletConfig"},
						ObjectMeta: metav1.ObjectMeta{Name: "worker-tls"},
						Spec: mcfgv1.KubeletConfigSpec{
							MachineConfigPoolSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"pools.operator.machineconfiguration.openshift.io/worker": ""}},
							TLSSecurityProfile:        test.override,
							KubeletConfig:             &runtime.RawExtension{Raw: raw},
						},
					})
				}
				if test.fips {
					for _, role := range []string{"master", "worker"} {
						mc, err := ctrlcommon.MachineConfigFromIgnConfig(role, "99-"+role+"-fips", ctrlcommon.NewIgnConfig())
						require.NoError(t, err)
						mc.TypeMeta = metav1.TypeMeta{APIVersion: mcfgv1.GroupVersion.String(), Kind: "MachineConfig"}
						mc.Spec.FIPS = true
						writeManifest(mc.Name+".yaml", mc)
					}
				}

				require.NoError(t, bootstrap.Run(destDir))
				for _, role := range []string{"master", "worker"} {
					paths, err := filepath.Glob(filepath.Join(destDir, "machine-configs", "rendered-"+role+"-*.yaml"))
					require.NoError(t, err)
					require.Len(t, paths, 1)
					data, err := os.ReadFile(paths[0])
					require.NoError(t, err)
					mc, err := mcoResourceRead.ReadMachineConfigV1(data)
					require.NoError(t, err)
					ign, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
					require.NoError(t, err)
					contents, err := ctrlcommon.GetIgnitionFileDataByPath(&ign, "/etc/kubernetes/kubelet.conf")
					require.NoError(t, err)
					var config kubeletconfigv1beta1.KubeletConfiguration
					require.NoError(t, yaml.Unmarshal(contents, &config))
					want := test.wantMaster
					if role == "worker" {
						want = test.wantWorker
						if test.override != nil {
							require.EqualValues(t, 123, config.MaxPods, "pruning groups must preserve the rest of the KubeletConfig")
						}
					}
					require.Equal(t, want, config.TLSCurvePreferences, "pool %s", role)
					if want == nil {
						require.NotContains(t, string(contents), "tlsCurvePreferences", "pool %s", role)
					}
				}
			})
		}
	}
}

// customProfileWithGroups builds a Custom TLS profile for pruning tests.
func customProfileWithGroups(groups ...apicfgv1.TLSGroup) *apicfgv1.TLSSecurityProfile {
	return &apicfgv1.TLSSecurityProfile{
		Type: apicfgv1.TLSProfileCustomType,
		Custom: &apicfgv1.CustomTLSProfile{
			TLSProfileSpec: apicfgv1.TLSProfileSpec{
				Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256"},
				MinTLSVersion: apicfgv1.VersionTLS12,
				Groups:        groups,
			},
		},
	}
}

// Bootstrap pruning must match API-server pruning without mutating the input.
func TestPruneGatedTLSGroups(t *testing.T) {
	t.Run("groups are cleared without disturbing the rest of the profile", func(t *testing.T) {
		apiServer := &apicfgv1.APIServer{
			ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName},
			Spec: apicfgv1.APIServerSpec{
				TLSSecurityProfile: customProfileWithGroups(apicfgv1.TLSGroupX25519MLKEM768, apicfgv1.TLSGroupSecP256r1),
			},
		}

		pruned := pruneGatedTLSGroups(apiServer)

		require.Empty(t, pruned.Spec.TLSSecurityProfile.Custom.Groups)
		require.Equal(t, []string{"ECDHE-RSA-AES128-GCM-SHA256"}, pruned.Spec.TLSSecurityProfile.Custom.Ciphers,
			"pruning groups must not touch the ciphers")
		require.Equal(t, apicfgv1.VersionTLS12, pruned.Spec.TLSSecurityProfile.Custom.MinTLSVersion)

		// Preserve the original manifest.
		require.Len(t, apiServer.Spec.TLSSecurityProfile.Custom.Groups, 2,
			"the input APIServer must not be mutated")
	})

	t.Run("objects with nothing to prune are returned untouched", func(t *testing.T) {
		require.Nil(t, pruneGatedTLSGroups(nil))

		noProfile := &apicfgv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName}}
		require.Same(t, noProfile, pruneGatedTLSGroups(noProfile))

		noGroups := &apicfgv1.APIServer{
			ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.APIServerInstanceName},
			Spec:       apicfgv1.APIServerSpec{TLSSecurityProfile: customProfileWithGroups()},
		}
		require.Same(t, noGroups, pruneGatedTLSGroups(noGroups))
	})
}

// Check the same pruning behavior for KubeletConfig manifests.
func TestPruneGatedKubeletConfigTLSGroups(t *testing.T) {
	t.Run("groups are cleared without disturbing the rest of the profile", func(t *testing.T) {
		kc := &mcfgv1.KubeletConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "curves"},
			Spec: mcfgv1.KubeletConfigSpec{
				TLSSecurityProfile: customProfileWithGroups(apicfgv1.TLSGroupX25519),
			},
		}

		pruned := pruneGatedKubeletConfigTLSGroups(kc)

		require.Empty(t, pruned.Spec.TLSSecurityProfile.Custom.Groups)
		require.Equal(t, []string{"ECDHE-RSA-AES128-GCM-SHA256"}, pruned.Spec.TLSSecurityProfile.Custom.Ciphers)
		require.Len(t, kc.Spec.TLSSecurityProfile.Custom.Groups, 1,
			"the input KubeletConfig must not be mutated")
	})

	t.Run("objects with nothing to prune are returned untouched", func(t *testing.T) {
		require.Nil(t, pruneGatedKubeletConfigTLSGroups(nil))

		noProfile := &mcfgv1.KubeletConfig{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}
		require.Same(t, noProfile, pruneGatedKubeletConfigTLSGroups(noProfile))

		noGroups := &mcfgv1.KubeletConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "no-groups"},
			Spec:       mcfgv1.KubeletConfigSpec{TLSSecurityProfile: customProfileWithGroups()},
		}
		require.Same(t, noGroups, pruneGatedKubeletConfigTLSGroups(noGroups))
	})
}

func TestValidatePreBuiltImage(t *testing.T) {
	tests := []struct {
		name          string
		imageSpec     string
		errorContains string
	}{
		{
			name:          "Valid image with proper digest format",
			imageSpec:     "registry.example.com/test@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			errorContains: "",
		},
		{
			name:          "Empty image spec should fail",
			imageSpec:     "",
			errorContains: "cannot be empty",
		},
		{
			name:          "Image without digest should fail",
			imageSpec:     "registry.example.com/test:latest",
			errorContains: "must use digested format",
		},
		{
			name:          "Image with invalid digest length should fail",
			imageSpec:     "registry.example.com/test@sha256:12345",
			errorContains: "invalid reference format",
		},
		{
			name:          "Image with invalid digest characters should fail",
			imageSpec:     "registry.example.com/test@sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdez",
			errorContains: "invalid reference format",
		},
		{
			name:          "Image with uppercase digest should fail",
			imageSpec:     "registry.example.com/test@sha256:1234567890ABCDEF1234567890abcdef1234567890abcdef1234567890abcdef",
			errorContains: "invalid checksum digest format",
		},
		{
			name:          "Image with MD5 digest should fail",
			imageSpec:     "registry.example.com/test@md5:1234567890abcdef1234567890abcdef",
			errorContains: "unsupported digest algorithm",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePreBuiltImage(tt.imageSpec)

			if tt.errorContains != "" && err == nil {
				t.Errorf("Expected error but got none")
			}
			if tt.errorContains == "" && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if tt.errorContains != "" {
				// If we reach here, err must be non-nil (checked above)
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error to contain %q, but got: %v", tt.errorContains, err)
				}
			}
		})
	}
}
