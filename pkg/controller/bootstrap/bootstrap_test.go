package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/diff"

	mcoResourceRead "github.com/openshift/machine-config-operator/lib/resourceread"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
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

func TestBootstrapRun(t *testing.T) {
	destDir, err := os.MkdirTemp("", "controller-bootstrap")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	bootstrap := New("../../../templates", "testdata/bootstrap", "testdata/bootstrap/machineconfigcontroller-pull-secret")
	err = bootstrap.Run(destDir)
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
			contents, err := ctrlcommon.DecodeIgnitionFileContents(registriesConfig.Contents.Source, registriesConfig.Contents.Compression)
			require.NoError(t, err)
			// Only a minimal presence check; more comprehensive tests that the contents correspond to the ICSP semantics are
			// maintained in pkg/controller/container-runtime-config.
			assert.Contains(t, string(contents), "registry.mirror.example.com/ocp")
			assert.Contains(t, string(contents), "insecure-reg-1.io")
			assert.Contains(t, string(contents), "insecure-reg-2.io")
			assert.Contains(t, string(contents), "blocked-reg.io")
			assert.NotContains(t, string(contents), "release-registry.product.example.org")
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
