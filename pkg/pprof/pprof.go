// Package pprof provides runtime profiling support for MCO components.
// It offers both simple fire-and-forget profiling (via StartPprof) and
// dynamic lifecycle management (via Manager) for ConfigMap-based enablement.
package pprof

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
)

const (
	// DefaultPprofPort is the default port for pprof HTTP server
	DefaultPprofPort = 6060

	// EnablePprofConfigMapName is the name of the ConfigMap used to enable pprof
	EnablePprofConfigMapName = "enable-pprof"
)

// Config holds pprof configuration values for CLI flags
type Config struct {
	Enabled bool
	Port    int
}

// GetPprofFlags returns the CLI flags for pprof configuration and a pointer to a Config struct
// that will be populated with the parsed flag values
func GetPprofFlags() (*pflag.FlagSet, *Config) {
	config := &Config{}
	flags := pflag.NewFlagSet("pprof", pflag.ContinueOnError)
	flags.BoolVar(&config.Enabled, "enable-pprof", false, "Enable pprof profiling endpoint")
	flags.IntVar(&config.Port, "pprof-port", DefaultPprofPort, "Port for pprof HTTP server")
	return flags, config
}

// createPprofServer creates a new HTTP server with pprof handlers
func createPprofServer(port int) *http.Server {
	// Create dedicated mux to avoid polluting default mux
	mux := http.NewServeMux()

	// Register pprof handlers
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}
}

// StartPprof starts the pprof HTTP server on its own mux
// This is a simple fire-and-forget function for binaries that use CLI flags
// Uses context for graceful shutdown
// Returns a done channel that is closed when the server has fully stopped
func StartPprof(ctx context.Context, port int) (<-chan struct{}, error) {
	server := createPprofServer(port)

	// Bind the listener synchronously to catch any bind errors immediately
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind pprof listener on %s: %w", addr, err)
	}

	klog.Infof("Starting pprof server on %s", addr)

	// Create done channel to signal shutdown completion
	done := make(chan struct{})

	// Start serving in a goroutine
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			klog.Errorf("pprof server error: %v", err)
		}
	}()

	// Graceful shutdown - monitors the input context
	// #nosec G118 -- context.Background is required here because ctx is already cancelled at shutdown time
	go func() {
		defer close(done)
		<-ctx.Done()
		// Create a new context for shutdown (can't reuse cancelled ctx)
		// Give the server 5 seconds to shut down gracefully
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			klog.Errorf("pprof server shutdown error: %v", err)
		} else {
			klog.Info("pprof server stopped")
		}
		// Wait for Serve to actually return
		<-serveDone
	}()

	return done, nil
}

// Manager manages the lifecycle of a pprof server with dynamic start/stop capability
// This is used by machine-config-operator for ConfigMap-based enablement
type Manager struct {
	mu       sync.Mutex
	cancelFn context.CancelFunc
	done     <-chan struct{}
	running  bool
}

// NewManager creates a new pprof Manager
func NewManager() *Manager {
	return &Manager{}
}

// Start starts the pprof server if not already running
// This is idempotent - calling Start when already running is a no-op
func (m *Manager) Start(ctx context.Context, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		klog.V(4).Info("pprof server already running, ignoring Start request")
		return nil
	}

	// Create a child context that can be cancelled independently
	serverCtx, cancel := context.WithCancel(ctx)
	m.cancelFn = cancel

	// Use the shared StartPprof function
	done, err := StartPprof(serverCtx, port)
	if err != nil {
		cancel()
		return err
	}

	m.done = done
	m.running = true
	return nil
}

// Stop stops the pprof server if running
// This is idempotent - calling Stop when not running is a no-op
// Waits for shutdown to complete before returning
func (m *Manager) Stop() {
	m.mu.Lock()

	if !m.running {
		m.mu.Unlock()
		klog.V(4).Info("pprof server not running, ignoring Stop request")
		return
	}

	// Trigger shutdown by cancelling the context
	if m.cancelFn != nil {
		m.cancelFn()
	}

	// Copy done channel while holding lock
	done := m.done

	// Release lock before waiting to avoid blocking other operations
	m.mu.Unlock()

	// Wait for shutdown to complete (safe even if done is nil or already closed)
	if done != nil {
		<-done
	}

	// Reacquire lock to clear state
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancelFn = nil
	m.done = nil
	m.running = false
}

// IsRunning returns true if the pprof server is currently running
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}
