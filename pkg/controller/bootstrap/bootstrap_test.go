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
	"k8s.io/apimachinery/pkg/util/diff"
	"sigs.k8s.io/yaml"

	apicfgv1 "github.com/openshift/api/config/v1"
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

	bootstrap := New("../../../templates", srcDir, filepath.Join(srcDir, "machineconfigcontroller-pull-secret"))

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

	bootstrap.imageStreamFactory = fakeFactory

	return bootstrap, fakeFactory, srcDir, destDir
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
