package nri

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/nri/pkg/api"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name          string
		configContent string
		expectedNS    []string
		fileNotExist  bool
	}{
		{
			name: "valid config with multiple namespaces",
			configContent: `allowed_namespaces:
  - kube-system
  - openshift-machine-config-operator
  - default`,
			expectedNS: []string{"kube-system", "openshift-machine-config-operator", "default"},
		},
		{
			name: "valid config with single namespace",
			configContent: `allowed_namespaces:
  - kube-system`,
			expectedNS: []string{"kube-system"},
		},
		{
			name:          "empty config",
			configContent: `allowed_namespaces: []`,
			expectedNS:    []string{},
		},
		{
			name:         "config file does not exist",
			fileNotExist: true,
			expectedNS:   []string{},
		},
		{
			name:          "invalid YAML",
			configContent: `invalid: yaml: content: [`,
			expectedNS:    []string{}, // Invalid YAML is non-fatal, uses empty allowed list
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := logrus.New()
			logger.SetOutput(os.Stderr)

			var configPath string
			if !tt.fileNotExist {
				tmpDir := t.TempDir()
				configPath = filepath.Join(tmpDir, "config.yaml")
				err := os.WriteFile(configPath, []byte(tt.configContent), 0644)
				require.NoError(t, err)
			} else {
				configPath = "/nonexistent/path/config.yaml"
			}

			p := &AllowMutationsPlugin{
				log: logger,
				config: Config{
					AllowedNamespaces: []string{},
				},
			}

			// loadConfig never fails - it just logs warnings and uses default
			p.loadConfig(configPath)
			assert.Equal(t, tt.expectedNS, p.config.AllowedNamespaces)
		})
	}
}

func TestIsNamespaceAllowed(t *testing.T) {
	tests := []struct {
		name              string
		allowedNamespaces []string
		testNamespace     string
		expectedResult    bool
	}{
		{
			name:              "namespace in allowed list",
			allowedNamespaces: []string{"kube-system", "default", "openshift-*"},
			testNamespace:     "kube-system",
			expectedResult:    true,
		},
		{
			name:              "namespace not in allowed list",
			allowedNamespaces: []string{"kube-system", "default"},
			testNamespace:     "user-namespace",
			expectedResult:    false,
		},
		{
			name:              "empty allowed list",
			allowedNamespaces: []string{},
			testNamespace:     "any-namespace",
			expectedResult:    false,
		},
		{
			name:              "namespace matches exactly",
			allowedNamespaces: []string{"openshift-machine-config-operator"},
			testNamespace:     "openshift-machine-config-operator",
			expectedResult:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &AllowMutationsPlugin{
				config: Config{
					AllowedNamespaces: tt.allowedNamespaces,
				},
				log: logrus.New(),
			}

			result := p.isNamespaceAllowed(tt.testNamespace)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestCreateContainer(t *testing.T) {
	tests := []struct {
		name                  string
		allowedNamespaces     []string
		podNamespace          string
		expectNilAdjustment   bool
		expectEmptyAdjustment bool
	}{
		{
			name:                "allowed namespace returns empty adjustment",
			allowedNamespaces:   []string{"kube-system", "default"},
			podNamespace:        "kube-system",
			expectNilAdjustment: false,
			expectEmptyAdjustment: true,
		},
		{
			name:                "denied namespace returns nil adjustment",
			allowedNamespaces:   []string{"kube-system", "default"},
			podNamespace:        "user-namespace",
			expectNilAdjustment: true,
			expectEmptyAdjustment: false,
		},
		{
			name:                "empty allowed list denies all",
			allowedNamespaces:   []string{},
			podNamespace:        "any-namespace",
			expectNilAdjustment: true,
			expectEmptyAdjustment: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := logrus.New()
			logger.SetOutput(os.Stderr)

			p := &AllowMutationsPlugin{
				config: Config{
					AllowedNamespaces: tt.allowedNamespaces,
				},
				log: logger,
			}

			pod := &api.PodSandbox{
				Namespace: tt.podNamespace,
				Name:      "test-pod",
			}

			container := &api.Container{
				Name: "test-container",
			}

			adjustment, updates, err := p.CreateContainer(context.Background(), pod, container)

			assert.NoError(t, err)
			assert.Nil(t, updates)

			if tt.expectNilAdjustment {
				assert.Nil(t, adjustment, "Expected nil adjustment for denied namespace")
			}

			if tt.expectEmptyAdjustment {
				assert.NotNil(t, adjustment, "Expected non-nil adjustment for allowed namespace")
			}
		})
	}
}

func TestRunPodSandbox(t *testing.T) {
	tests := []struct {
		name              string
		allowedNamespaces []string
		podNamespace      string
	}{
		{
			name:              "allowed namespace",
			allowedNamespaces: []string{"kube-system"},
			podNamespace:      "kube-system",
		},
		{
			name:              "denied namespace",
			allowedNamespaces: []string{"kube-system"},
			podNamespace:      "user-namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := logrus.New()
			logger.SetOutput(os.Stderr)

			p := &AllowMutationsPlugin{
				config: Config{
					AllowedNamespaces: tt.allowedNamespaces,
				},
				log: logger,
			}

			pod := &api.PodSandbox{
				Namespace: tt.podNamespace,
				Name:      "test-pod",
			}

			err := p.RunPodSandbox(context.Background(), pod)
			assert.NoError(t, err, "RunPodSandbox should not return error")
		})
	}
}

func TestConfigure(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	p := &AllowMutationsPlugin{
		log: logger,
		config: Config{
			AllowedNamespaces: []string{},
		},
	}

	mask, err := p.Configure(context.Background(), "", "crio", "1.0.0")
	assert.NoError(t, err)
	assert.NotEqual(t, 0, mask, "Event mask should not be zero")
}

func TestSynchronize(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	p := &AllowMutationsPlugin{
		log: logger,
		config: Config{
			AllowedNamespaces: []string{},
		},
	}

	pods := []*api.PodSandbox{
		{Namespace: "default", Name: "pod1"},
		{Namespace: "kube-system", Name: "pod2"},
	}

	containers := []*api.Container{
		{Name: "container1"},
		{Name: "container2"},
	}

	updates, err := p.Synchronize(context.Background(), pods, containers)
	assert.NoError(t, err)
	assert.Nil(t, updates)
}

func TestShutdown(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	p := &AllowMutationsPlugin{
		log: logger,
		config: Config{
			AllowedNamespaces: []string{},
		},
	}

	// Should not panic
	p.Shutdown(context.Background())
}
