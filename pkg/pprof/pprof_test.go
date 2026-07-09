package pprof

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_StartStop(t *testing.T) {
	t.Parallel()

	m := NewManager()
	require.NotNil(t, m)

	port := 16062
	ctx := context.Background()

	// Initially not running
	assert.False(t, m.IsRunning())

	// Start the manager
	err := m.Start(ctx, port)
	require.NoError(t, err)
	assert.True(t, m.IsRunning())

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Verify server is accessible
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Stop the manager
	m.Stop()
	assert.False(t, m.IsRunning())

	// Give server time to shut down
	time.Sleep(200 * time.Millisecond)

	// Verify server is no longer accessible
	_, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port))
	assert.Error(t, err, "server should be stopped")
}

func TestManager_StartIdempotent(t *testing.T) {
	t.Parallel()

	m := NewManager()
	port := 16063
	ctx := context.Background()

	// Start the manager
	err := m.Start(ctx, port)
	require.NoError(t, err)
	assert.True(t, m.IsRunning())

	// Start again - should be no-op
	err = m.Start(ctx, port)
	require.NoError(t, err)
	assert.True(t, m.IsRunning())

	// Start with different port - should still be no-op (same server keeps running)
	err = m.Start(ctx, 9999)
	require.NoError(t, err)
	assert.True(t, m.IsRunning())

	// Verify original server still works
	time.Sleep(100 * time.Millisecond)
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify new port was NOT started
	_, err = http.Get("http://127.0.0.1:9999/debug/pprof/")
	assert.Error(t, err, "second Start call should not have started a new server")

	// Cleanup
	m.Stop()
}

func TestManager_StopIdempotent(t *testing.T) {
	t.Parallel()

	m := NewManager()
	port := 16064
	ctx := context.Background()

	// Start the manager
	err := m.Start(ctx, port)
	require.NoError(t, err)

	// Stop once
	m.Stop()
	assert.False(t, m.IsRunning())

	// Stop again - should be no-op (no panic)
	m.Stop()
	assert.False(t, m.IsRunning())
}

func TestManager_StopWithoutStart(t *testing.T) {
	t.Parallel()

	m := NewManager()

	// Stop without starting - should be no-op (no panic)
	m.Stop()
	assert.False(t, m.IsRunning())
}

func TestManager_RestartAfterStop(t *testing.T) {
	t.Parallel()

	m := NewManager()
	port := 16065
	ctx := context.Background()

	// Start
	err := m.Start(ctx, port)
	require.NoError(t, err)
	assert.True(t, m.IsRunning())

	time.Sleep(100 * time.Millisecond)

	// Stop
	m.Stop()
	assert.False(t, m.IsRunning())

	time.Sleep(200 * time.Millisecond)

	// Restart on same port
	err = m.Start(ctx, port)
	require.NoError(t, err)
	assert.True(t, m.IsRunning())

	time.Sleep(100 * time.Millisecond)

	// Verify server works
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Cleanup
	m.Stop()
}

func TestManager_DifferentPortAfterStop(t *testing.T) {
	t.Parallel()

	m := NewManager()
	port1 := 16066
	port2 := 16067
	ctx := context.Background()

	// Start on port1
	err := m.Start(ctx, port1)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// Stop
	m.Stop()
	time.Sleep(200 * time.Millisecond)

	// Start on port2
	err = m.Start(ctx, port2)
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// Verify port2 works
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port2))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify port1 is not running
	_, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port1))
	assert.Error(t, err)

	// Cleanup
	m.Stop()
}

func TestManager_StartReturnsBindError(t *testing.T) {
	t.Parallel()

	port := 16068
	ctx := context.Background()

	// Start first manager to bind the port
	m1 := NewManager()
	err := m1.Start(ctx, port)
	require.NoError(t, err)
	assert.True(t, m1.IsRunning())
	defer m1.Stop()

	time.Sleep(100 * time.Millisecond)

	// Try to start second manager on same port - should fail immediately
	m2 := NewManager()
	err = m2.Start(ctx, port)
	assert.Error(t, err, "should return bind error when port is already in use")
	assert.False(t, m2.IsRunning(), "manager should not be marked as running if bind fails")
	assert.Contains(t, err.Error(), "failed to bind pprof listener")
}

func TestStartPprof_ReturnsBindError(t *testing.T) {
	t.Parallel()

	port := 16069
	ctx := context.Background()

	// Start first server to bind the port
	_, err := StartPprof(ctx, port)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Try to start second server on same port - should fail immediately
	_, err = StartPprof(ctx, port)
	assert.Error(t, err, "should return bind error when port is already in use")
	assert.Contains(t, err.Error(), "failed to bind pprof listener")
}

func TestManager_StopWaitsForShutdown(t *testing.T) {
	t.Parallel()

	port := 16070
	ctx := context.Background()

	m := NewManager()
	err := m.Start(ctx, port)
	require.NoError(t, err)
	assert.True(t, m.IsRunning())

	time.Sleep(100 * time.Millisecond)

	// Verify server is running
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", port))
	require.NoError(t, err)
	_ = resp.Body.Close()

	// Record time before stop
	startStop := time.Now()

	// Stop should wait for shutdown to complete
	m.Stop()

	// Stop should have waited (at least a few milliseconds for shutdown)
	stopDuration := time.Since(startStop)
	assert.True(t, stopDuration > 0, "Stop should have waited for shutdown")

	// After Stop returns, server should be fully stopped
	assert.False(t, m.IsRunning())

	// Verify we can immediately start a new server on the same port
	// (proves shutdown completed and released the port)
	m2 := NewManager()
	err = m2.Start(ctx, port)
	require.NoError(t, err, "should be able to bind to the same port immediately after Stop returns")
	assert.True(t, m2.IsRunning())

	// Cleanup
	m2.Stop()
}

func TestManager_RepeatedStopIsSafe(t *testing.T) {
	t.Parallel()

	port := 16071
	ctx := context.Background()

	m := NewManager()
	err := m.Start(ctx, port)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// First stop
	m.Stop()
	assert.False(t, m.IsRunning())

	// Second stop should not panic or deadlock
	m.Stop()
	assert.False(t, m.IsRunning())

	// Third stop for good measure
	m.Stop()
	assert.False(t, m.IsRunning())
}
