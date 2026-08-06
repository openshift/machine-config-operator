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

	imgtypes "github.com/containers/image/v5/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/diff"
	"sigs.k8s.io/yaml"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcoResourceRead "github.com/openshift/machine-config-operator/lib/resourceread"
	buildconstants "github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	"github.com/openshift/machine-config-operator/pkg/version"
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

// Implements a fake ImagesInspector. FetchImageFileFunc and InspectFunc are
// stubbable per-test; if left nil, the corresponding method returns an error,
// simulating an unreachable registry.
type fakeImagesInspector struct {
	FetchImageFileFunc func(ctx context.Context, image, path string) ([]byte, error)
	InspectFunc        func(ctx context.Context, image ...string) ([]imageutils.BulkInspectResult, error)
}

func (f *fakeImagesInspector) FetchImageFile(ctx context.Context, image, path string) ([]byte, error) {
	if f.FetchImageFileFunc != nil {
		return f.FetchImageFileFunc(ctx, image, path)
	}
	return nil, fmt.Errorf("fakeImagesInspector: FetchImageFile not configured for image %q", image)
}

func (f *fakeImagesInspector) Inspect(ctx context.Context, image ...string) ([]imageutils.BulkInspectResult, error) {
	if f.InspectFunc != nil {
		return f.InspectFunc(ctx, image...)
	}
	return nil, fmt.Errorf("fakeImagesInspector: Inspect not configured for image(s) %v", image)
}

// Implements a fake ImagesInspectorFactory.
type fakeImagesInspectorFactory struct {
	inspector *fakeImagesInspector
	// Whether the ForContext method was called.
	forContextCalled bool
}

func (f *fakeImagesInspectorFactory) ForContext(_ imageutils.SysContextFactory) osimagestream.ImagesInspector {
	f.forContextCalled = true
	return f.inspector
}

// fakeOSReleaseContent returns minimal /etc/os-release-style content with
// the given OPENSHIFT_VERSION field set.
func fakeOSReleaseContent(openshiftVersion string) []byte {
	return []byte(fmt.Sprintf("ID=\"rhcos\"\nVERSION_ID=\"9\"\nOPENSHIFT_VERSION=%q\n", openshiftVersion))
}

// inspectResultWithLabels builds a successful imageutils.BulkInspectResult
// with the given labels for a single image.
func inspectResultWithLabels(image string, labels map[string]string) []imageutils.BulkInspectResult {
	return []imageutils.BulkInspectResult{{
		Image:       image,
		InspectInfo: &imgtypes.ImageInspectInfo{Labels: labels},
	}}
}

// inspectResultWithError builds a failed imageutils.BulkInspectResult for a
// single image, simulating an unreachable registry.
func inspectResultWithError(image string, err error) []imageutils.BulkInspectResult {
	return []imageutils.BulkInspectResult{{
		Image: image,
		Error: err,
	}}
}

// Instantiates a new instance of the Bootstrap struct for testing. This also
// does the following:
// 1. Copies the data from testdata/bootstrap into a temp directory so that it
// may be safely overwritten to test specific scenarios.
// 2. Creates a fake ImageStreamFactory instance and wires it up to return an
// OSImageStream.
// 3. Creates a fake ImagesInspectorFactory instance and wires it up so that
// the pre-built image referenced by testdata/bootstrap/layered-worker.machineosconfig.yaml
// passes version validation (its /etc/os-release always reports an
// OPENSHIFT_VERSION matching version.ReleaseVersion).
func setupForBootstrapTest(t *testing.T) (*Bootstrap, *fakeImageStreamFactory, *fakeImagesInspectorFactory, string, string) {
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

	fakeInspectorFactory := &fakeImagesInspectorFactory{
		inspector: &fakeImagesInspector{
			FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
				return fakeOSReleaseContent(version.ReleaseVersion), nil
			},
		},
	}

	bootstrap := New("../../../templates", srcDir, filepath.Join(srcDir, "machineconfigcontroller-pull-secret"), fakeInspectorFactory)
	bootstrap.imageStreamFactory = fakeFactory

	return bootstrap, fakeFactory, fakeInspectorFactory, srcDir, destDir
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
					{Name: "SigstoreImageVerification"},
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
			bootstrap, fakeFactory, _, srcDir, destDir := setupForBootstrapTest(t)

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
	bootstrap, fakeFactory, fakeInspectorFactory, _, destDir := setupForBootstrapTest(t)

	err := bootstrap.Run(destDir)
	require.NoError(t, err)

	// testdata/bootstrap/layered-worker.machineosconfig.yaml carries a
	// pre-built image annotation, so bootstrap-time validation must have
	// used the (fake) ImagesInspector to check it.
	assert.True(t, fakeInspectorFactory.forContextCalled)

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

const (
	testDigestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDigestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestImageDigest(t *testing.T) {
	tests := []struct {
		name          string
		imageSpec     string
		wantDigest    string
		errorContains string
	}{
		{
			name:       "valid digested reference",
			imageSpec:  "registry.example.com/test@sha256:" + testDigestA,
			wantDigest: "sha256:" + testDigestA,
		},
		{
			name:          "reference without digest",
			imageSpec:     "registry.example.com/test:latest",
			errorContains: "is not in digested form",
		},
		{
			name:          "malformed reference",
			imageSpec:     "not a valid ref!!",
			errorContains: "invalid image reference",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := imageDigest(tt.imageSpec)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantDigest, got.String())
		})
	}
}

func TestSameMajorMinor(t *testing.T) {
	tests := []struct {
		name          string
		a             string
		b             string
		want          bool
		errorContains string
	}{
		{name: "identical major.minor with differing patch", a: "4.16", b: "4.16.3", want: true},
		{name: "differing minor", a: "4.16", b: "4.17", want: false},
		{name: "differing major", a: "5.0", b: "4.16", want: false},
		{name: "leading v is tolerated", a: "v4.16", b: "4.16.1", want: true},
		{name: "unparseable a", a: "not-a-version", b: "4.16", errorContains: "could not parse version"},
		{name: "unparseable b", a: "4.16", b: "not-a-version", errorContains: "could not parse version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sameMajorMinor(tt.a, tt.b)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOpenshiftVersionFromImage(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		inspector := &fakeImagesInspector{
			FetchImageFileFunc: func(_ context.Context, _, path string) ([]byte, error) {
				assert.Equal(t, osReleasePath, path)
				return fakeOSReleaseContent("4.16"), nil
			},
		}
		got, err := openshiftVersionFromImage(ctx, inspector, "registry.example.com/test@sha256:"+testDigestA)
		require.NoError(t, err)
		assert.Equal(t, "4.16", got)
	})

	t.Run("fetch error", func(t *testing.T) {
		inspector := &fakeImagesInspector{
			FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
				return nil, fmt.Errorf("registry unreachable")
			},
		}
		_, err := openshiftVersionFromImage(ctx, inspector, "registry.example.com/test@sha256:"+testDigestA)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not fetch")
	})

	t.Run("missing OPENSHIFT_VERSION field", func(t *testing.T) {
		inspector := &fakeImagesInspector{
			FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
				return []byte(`ID="rhel"` + "\n"), nil
			},
		}
		_, err := openshiftVersionFromImage(ctx, inspector, "registry.example.com/test@sha256:"+testDigestA)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no OPENSHIFT_VERSION field")
	})
}

func TestValidatePreBuiltImageDigestFallback(t *testing.T) {
	imageSpec := "registry.example.com/layered@sha256:" + testDigestA

	tests := []struct {
		name                string
		labels              map[string]string
		expectedBaseOSImage string
		errorContains       string
	}{
		{
			name: "matching digest through a different registry host (mirror-tolerant)",
			labels: map[string]string{
				preBuiltImageBaseOSLabelKey: "mirror.example.com/os@sha256:" + testDigestB,
			},
			expectedBaseOSImage: "registry.host.com/os@sha256:" + testDigestB,
		},
		{
			name: "mismatched digest",
			labels: map[string]string{
				preBuiltImageBaseOSLabelKey: "registry.host.com/os@sha256:" + testDigestA,
			},
			expectedBaseOSImage: "registry.host.com/os@sha256:" + testDigestB,
			errorContains:       "does not match the cluster's resolved base OS image",
		},
		{
			name:                "missing label is allowed through with a warning",
			labels:              map[string]string{},
			expectedBaseOSImage: "registry.host.com/os@sha256:" + testDigestB,
		},
		{
			name: "invalid label",
			labels: map[string]string{
				preBuiltImageBaseOSLabelKey: "not a valid ref!!",
			},
			expectedBaseOSImage: "registry.host.com/os@sha256:" + testDigestB,
			errorContains:       "invalid",
		},
		{
			name: "invalid expected base OS image",
			labels: map[string]string{
				preBuiltImageBaseOSLabelKey: "registry.host.com/os@sha256:" + testDigestB,
			},
			expectedBaseOSImage: "registry.host.com/os:latest",
			errorContains:       "is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePreBuiltImageDigestFallback(tt.labels, imageSpec, tt.expectedBaseOSImage)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidatePreBuiltImageVersion(t *testing.T) {
	const imageSpec = "registry.example.com/layered@sha256:" + testDigestA
	const expectedBaseOSImage = "registry.host.com/os@sha256:" + testDigestB

	tests := []struct {
		name          string
		inspector     *fakeImagesInspector
		errorContains string
	}{
		{
			name: "OCP version matches",
			inspector: &fakeImagesInspector{
				FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
					return fakeOSReleaseContent(version.ReleaseVersion), nil
				},
				InspectFunc: func(_ context.Context, _ ...string) ([]imageutils.BulkInspectResult, error) {
					t.Fatal("Inspect should not be called when the OCP version check succeeds")
					return nil, nil
				},
			},
		},
		{
			name: "OCP version mismatch",
			inspector: &fakeImagesInspector{
				FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
					return fakeOSReleaseContent("999.999"), nil
				},
			},
			errorContains: "does not match the cluster's OCP version",
		},
		{
			name: "no os-release, digest fallback matches",
			inspector: &fakeImagesInspector{
				FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
					return nil, fmt.Errorf("no such file")
				},
				InspectFunc: func(_ context.Context, image ...string) ([]imageutils.BulkInspectResult, error) {
					return inspectResultWithLabels(image[0], map[string]string{
						preBuiltImageBaseOSLabelKey: expectedBaseOSImage,
					}), nil
				},
			},
		},
		{
			name: "no os-release, digest mismatch",
			inspector: &fakeImagesInspector{
				FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
					return nil, fmt.Errorf("no such file")
				},
				InspectFunc: func(_ context.Context, image ...string) ([]imageutils.BulkInspectResult, error) {
					return inspectResultWithLabels(image[0], map[string]string{
						preBuiltImageBaseOSLabelKey: "registry.host.com/os@sha256:" + testDigestA,
					}), nil
				},
			},
			errorContains: "does not match the cluster's resolved base OS image",
		},
		{
			name: "neither OPENSHIFT_VERSION nor baseOSContainerImage label available",
			inspector: &fakeImagesInspector{
				FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
					return nil, fmt.Errorf("no such file")
				},
				InspectFunc: func(_ context.Context, image ...string) ([]imageutils.BulkInspectResult, error) {
					return inspectResultWithLabels(image[0], map[string]string{}), nil
				},
			},
		},
		{
			name: "Inspect call itself fails (registry unreachable)",
			inspector: &fakeImagesInspector{
				FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
					return nil, fmt.Errorf("no such file")
				},
				InspectFunc: func(_ context.Context, _ ...string) ([]imageutils.BulkInspectResult, error) {
					return nil, fmt.Errorf("dial tcp: no route to host")
				},
			},
			errorContains: "could not inspect pre-built image",
		},
		{
			name: "Inspect returns a per-image error (registry unreachable or image not found)",
			inspector: &fakeImagesInspector{
				FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
					return nil, fmt.Errorf("no such file")
				},
				InspectFunc: func(_ context.Context, image ...string) ([]imageutils.BulkInspectResult, error) {
					return inspectResultWithError(image[0], fmt.Errorf("manifest unknown")), nil
				},
			},
			errorContains: "could not access pre-built image",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePreBuiltImageVersion(context.Background(), tt.inspector, imageSpec, expectedBaseOSImage)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCreatePreBuiltImageMachineConfigs(t *testing.T) {
	pools := []*mcfgv1.MachineConfigPool{
		{ObjectMeta: metav1.ObjectMeta{Name: "layered-worker"}},
	}
	cconfig := &mcfgv1.ControllerConfig{
		Spec: mcfgv1.ControllerConfigSpec{
			BaseOSContainerImage: "registry.host.com/os@sha256:" + testDigestB,
		},
	}
	preBuiltImage := "quay.io/example/layered@sha256:" + testDigestA

	versionMatchingInspector := &fakeImagesInspector{
		FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
			return fakeOSReleaseContent(version.ReleaseVersion), nil
		},
	}

	newMOSC := func(annotations map[string]string, conditions []metav1.Condition) *mcfgv1.MachineOSConfig {
		return &mcfgv1.MachineOSConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "layered-worker",
				Annotations: annotations,
			},
			Spec: mcfgv1.MachineOSConfigSpec{
				MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: "layered-worker"},
			},
			Status: mcfgv1.MachineOSConfigStatus{
				Conditions: conditions,
			},
		}
	}

	t.Run("valid pre-built image produces a component MachineConfig", func(t *testing.T) {
		mosc := newMOSC(map[string]string{buildconstants.PreBuiltImageAnnotationKey: preBuiltImage}, nil)
		mcs, err := createPreBuiltImageMachineConfigs(context.Background(), []*mcfgv1.MachineOSConfig{mosc}, pools, cconfig, versionMatchingInspector)
		require.NoError(t, err)
		require.Len(t, mcs, 1)
	})

	t.Run("missing annotation before seeding fails", func(t *testing.T) {
		mosc := newMOSC(nil, nil)
		_, err := createPreBuiltImageMachineConfigs(context.Background(), []*mcfgv1.MachineOSConfig{mosc}, pools, cconfig, versionMatchingInspector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is missing required annotation")
	})

	t.Run("missing annotation after seeding is skipped", func(t *testing.T) {
		mosc := newMOSC(nil, []metav1.Condition{{
			Type:   buildconstants.MachineOSConfigSeeded,
			Status: metav1.ConditionTrue,
		}})
		mcs, err := createPreBuiltImageMachineConfigs(context.Background(), []*mcfgv1.MachineOSConfig{mosc}, pools, cconfig, versionMatchingInspector)
		require.NoError(t, err)
		assert.Empty(t, mcs)
	})

	t.Run("non-existent pool fails", func(t *testing.T) {
		mosc := newMOSC(map[string]string{buildconstants.PreBuiltImageAnnotationKey: preBuiltImage}, nil)
		mosc.Spec.MachineConfigPool.Name = "does-not-exist"
		_, err := createPreBuiltImageMachineConfigs(context.Background(), []*mcfgv1.MachineOSConfig{mosc}, pools, cconfig, versionMatchingInspector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "references non-existent pool")
	})

	t.Run("invalid image format fails", func(t *testing.T) {
		mosc := newMOSC(map[string]string{buildconstants.PreBuiltImageAnnotationKey: "quay.io/example/layered:latest"}, nil)
		_, err := createPreBuiltImageMachineConfigs(context.Background(), []*mcfgv1.MachineOSConfig{mosc}, pools, cconfig, versionMatchingInspector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid pre-built image")
	})

	t.Run("OCP version mismatch fails", func(t *testing.T) {
		mismatchInspector := &fakeImagesInspector{
			FetchImageFileFunc: func(_ context.Context, _, _ string) ([]byte, error) {
				return fakeOSReleaseContent("999.999"), nil
			},
		}
		mosc := newMOSC(map[string]string{buildconstants.PreBuiltImageAnnotationKey: preBuiltImage}, nil)
		_, err := createPreBuiltImageMachineConfigs(context.Background(), []*mcfgv1.MachineOSConfig{mosc}, pools, cconfig, mismatchInspector)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed validation")
	})
}
