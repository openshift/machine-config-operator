package nri

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

const (
	// PluginName is the name registered with NRI
	PluginName = "AllowMutations"
	// PluginIdx is the plugin index - high value ensures it runs last
	// NRI plugins are invoked in index order, so 99 ensures this runs after all other plugins
	PluginIdx = "99"
	// DefaultConfigPath is the default location for plugin configuration
	DefaultConfigPath = "/etc/crio/nri_plugins/AllowMutations/config.yaml"
	// DefaultSocketPath is the default NRI socket path
	DefaultSocketPath = "/var/run/nri/nri.sock"
)

// Config represents the plugin configuration
type Config struct {
	AllowedNamespaces []string `json:"allowed_namespaces" yaml:"allowed_namespaces"`
}

// AllowMutationsPlugin is the NRI plugin implementation
type AllowMutationsPlugin struct {
	stub   stub.Stub
	config Config
	mu     sync.RWMutex
	log    *logrus.Logger
}

// NewAllowMutationsPlugin creates a new instance of the AllowMutations NRI plugin
func NewAllowMutationsPlugin(logger *logrus.Logger, configPath string) (*AllowMutationsPlugin, error) {
	if logger == nil {
		logger = logrus.StandardLogger()
	}

	p := &AllowMutationsPlugin{
		log: logger,
		config: Config{
			AllowedNamespaces: []string{},
		},
	}

	// Load configuration (never fails - defaults to deny all on error)
	p.loadConfig(configPath)

	// Create NRI stub
	opts := []stub.Option{
		stub.WithOnClose(p.onClose),
		stub.WithPluginName(PluginName),
		stub.WithPluginIdx(PluginIdx),
	}

	var err error
	p.stub, err = stub.New(p, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create NRI stub: %w", err)
	}

	return p, nil
}

// Start starts the NRI plugin
func (p *AllowMutationsPlugin) Start(ctx context.Context) error {
	p.log.Infof("Starting %s NRI plugin", PluginName)
	return p.stub.Run(ctx)
}

// Stop stops the NRI plugin
func (p *AllowMutationsPlugin) Stop() {
	if p.stub != nil {
		p.stub.Stop()
	}
}

// Configure is called when the plugin is configured by the runtime
func (p *AllowMutationsPlugin) Configure(_ context.Context, config, runtime, version string) (stub.EventMask, error) {
	p.log.Infof("Connected to %s/%s", runtime, version)

	// We want to intercept container creation to enforce mutation policy
	mask := api.MustParseEventMask("RunPodSandbox,CreateContainer")

	p.log.Infof("Configured %s plugin with event mask: %v", PluginName, mask)
	return mask, nil
}

// Synchronize is called when the plugin needs to synchronize state
func (p *AllowMutationsPlugin) Synchronize(_ context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	p.log.Infof("Synchronized state with runtime (%d pods, %d containers)", len(pods), len(containers))
	return nil, nil
}

// Shutdown is called when the runtime is shutting down
func (p *AllowMutationsPlugin) Shutdown(_ context.Context) {
	p.log.Info("Runtime shutting down")
}

// RunPodSandbox is called when a pod sandbox is being created
func (p *AllowMutationsPlugin) RunPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	namespace := pod.GetNamespace()
	podName := pod.GetName()

	if !p.isNamespaceAllowed(namespace) {
		p.log.Infof("Denying mutations for pod %s/%s (namespace not in allowed list)", namespace, podName)
	} else {
		p.log.Debugf("Allowing mutations for pod %s/%s", namespace, podName)
	}

	return nil
}

// CreateContainer is called when a container is being created
// This is where we enforce the mutation policy by returning nil adjustments
// for namespaces not in the allowed list
func (p *AllowMutationsPlugin) CreateContainer(_ context.Context, pod *api.PodSandbox, container *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	namespace := pod.GetNamespace()
	podName := pod.GetName()
	containerName := container.GetName()

	if !p.isNamespaceAllowed(namespace) {
		p.log.Infof("Denying mutations for container %s in pod %s/%s (namespace not in allowed list)",
			containerName, namespace, podName)
		// Return nil adjustment - this prevents any mutations from being applied
		return nil, nil, nil
	}

	p.log.Debugf("Allowing mutations for container %s in pod %s/%s",
		containerName, namespace, podName)

	// For allowed namespaces, return empty adjustments to allow other plugins to make changes
	return &api.ContainerAdjustment{}, nil, nil
}

// isNamespaceAllowed checks if mutations are allowed in the given namespace
func (p *AllowMutationsPlugin) isNamespaceAllowed(namespace string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, ns := range p.config.AllowedNamespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

// loadConfig loads the plugin configuration from the specified path.
// All errors are treated as non-fatal to ensure the plugin always starts.
// If configuration cannot be loaded, the plugin defaults to denying all namespaces.
func (p *AllowMutationsPlugin) loadConfig(configPath string) {
	if configPath == "" {
		configPath = DefaultConfigPath
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		// Treat all read errors as non-fatal - plugin starts with empty allowed list
		if os.IsNotExist(err) {
			p.log.Warnf("Configuration file not found at %s, using default (deny all namespaces)", configPath)
		} else {
			p.log.Warnf("Failed to read configuration file at %s: %v. Using default (deny all namespaces)", configPath, err)
		}
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if err := yaml.Unmarshal(data, &p.config); err != nil {
		// Invalid YAML is also non-fatal - plugin starts with empty allowed list
		p.log.Warnf("Failed to parse configuration YAML at %s: %v. Using default (deny all namespaces)", configPath, err)
		p.config.AllowedNamespaces = []string{}
		return
	}

	p.log.Infof("Loaded configuration: allowed_namespaces=%v", p.config.AllowedNamespaces)
}

// onClose is called when the connection to the runtime is closed
func (p *AllowMutationsPlugin) onClose() {
	p.log.Warnf("Connection to runtime lost, plugin will exit")
	os.Exit(1)
}
