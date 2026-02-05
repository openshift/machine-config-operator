package daemon

import (
	"context"
	"fmt"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
)

const (
	// Systemd unit load states
	mockLoadStateLoaded = "loaded"

	// Systemd unit active states
	mockActiveStateActive   = "active"
	mockActiveStateInactive = "inactive"

	// Systemd unit sub states
	mockSubStateRunning = "running"
	mockSubStateDead    = "dead"
)

// mockSystemdConnection is a mock implementation of SystemdConnection for testing
type mockSystemdConnection struct {
	// Unit state - the source of truth for all units
	units map[string]*mockUnitState

	// Callback functions - if set, these are called before default behavior
	// The callback can perform assertions, modify state, and return errors
	// If the callback returns a non-nil error, default behavior is skipped
	OnEnableFunc      func(ctx context.Context, force bool, units ...string) error
	OnDisableFunc     func(ctx context.Context, units ...string) error
	OnIsEnabledFunc   func(ctx context.Context, unit string) (bool, error)
	OnTryRestartFunc  func(ctx context.Context, unit string) error
	OnRestartFunc     func(ctx context.Context, unit string) error
	OnStartFunc       func(ctx context.Context, unit string) error
	OnStopFunc        func(ctx context.Context, unit string) error
	OnReloadFunc      func(ctx context.Context, unit string) error
	OnPresetFunc      func(ctx context.Context, unit string) error
	OnReloadDaemonFunc func(ctx context.Context) error
	OnListUnitsFunc   func(ctx context.Context) (map[string]systemddbus.UnitStatus, error)
}

type mockUnitState struct {
	name        string
	enabled     bool
	active      bool
	loadState   string
	activeState string
	subState    string
}

// newMockSystemdConnection creates a new mock systemd connection with the specified units
func newMockSystemdConnection(units map[string]*mockUnitState) *mockSystemdConnection {
	if units == nil {
		units = make(map[string]*mockUnitState)
	}
	return &mockSystemdConnection{
		units: units,
	}
}

// newMockUnitState creates a mock unit with defaults (disabled, inactive)
func newMockUnitState(name string) *mockUnitState {
	return &mockUnitState{
		name:        name,
		enabled:     false,
		active:      false,
		loadState:   mockLoadStateLoaded,
		activeState: mockActiveStateInactive,
		subState:    mockSubStateDead,
	}
}

// newMockEnabledUnit creates a mock unit that is enabled and active
func newMockEnabledUnit(name string) *mockUnitState {
	return &mockUnitState{
		name:        name,
		enabled:     true,
		active:      true,
		loadState:   mockLoadStateLoaded,
		activeState: mockActiveStateActive,
		subState:    mockSubStateRunning,
	}
}

func (m *mockSystemdConnection) Enable(ctx context.Context, force bool, units ...string) error {
	if m.OnEnableFunc != nil {
		if err := m.OnEnableFunc(ctx, force, units...); err != nil {
			return err
		}
	}

	for _, unitName := range units {
		if unit, ok := m.units[unitName]; ok {
			unit.enabled = true
		} else {
			// Create unit if it doesn't exist
			m.units[unitName] = &mockUnitState{
				name:        unitName,
				enabled:     true,
				active:      false,
				loadState:   mockLoadStateLoaded,
				activeState: mockActiveStateInactive,
				subState:    mockSubStateDead,
			}
		}
	}
	return nil
}

func (m *mockSystemdConnection) Disable(ctx context.Context, units ...string) error {
	if m.OnDisableFunc != nil {
		if err := m.OnDisableFunc(ctx, units...); err != nil {
			return err
		}
	}

	// Default behavior: disable the units
	for _, unitName := range units {
		if unit, ok := m.units[unitName]; ok {
			unit.enabled = false
		}
	}
	return nil
}

func (m *mockSystemdConnection) IsEnabled(ctx context.Context, unitName string) (bool, error) {
	if m.OnIsEnabledFunc != nil {
		enabled, err := m.OnIsEnabledFunc(ctx, unitName)
		if err != nil {
			return false, err
		}
		return enabled, nil
	}

	// Default behavior: return enabled state from units map
	if unit, ok := m.units[unitName]; ok {
		return unit.enabled, nil
	}
	return false, fmt.Errorf("unit %q not found", unitName)
}

func (m *mockSystemdConnection) TryRestart(ctx context.Context, unitName string) error {
	if m.OnTryRestartFunc != nil {
		if err := m.OnTryRestartFunc(ctx, unitName); err != nil {
			return err
		}
	}

	// Default behavior: only restart if unit is active
	if unit, ok := m.units[unitName]; ok && unit.active {
		unit.activeState = mockActiveStateActive
		unit.subState = mockSubStateRunning
	}
	return nil
}

func (m *mockSystemdConnection) Restart(ctx context.Context, unitName string) error {
	if m.OnRestartFunc != nil {
		if err := m.OnRestartFunc(ctx, unitName); err != nil {
			return err
		}
	}

	// Default behavior: restart the unit
	if unit, ok := m.units[unitName]; ok {
		unit.active = true
		unit.activeState = mockActiveStateActive
		unit.subState = mockSubStateRunning
	}
	return nil
}

func (m *mockSystemdConnection) Start(ctx context.Context, unitName string) error {
	if m.OnStartFunc != nil {
		if err := m.OnStartFunc(ctx, unitName); err != nil {
			return err
		}
	}

	// Default behavior: start the unit
	if unit, ok := m.units[unitName]; ok {
		unit.active = true
		unit.activeState = mockActiveStateActive
		unit.subState = mockSubStateRunning
	}
	return nil
}

func (m *mockSystemdConnection) Stop(ctx context.Context, unitName string) error {
	if m.OnStopFunc != nil {
		if err := m.OnStopFunc(ctx, unitName); err != nil {
			return err
		}
	}

	// Default behavior: stop the unit
	if unit, ok := m.units[unitName]; ok {
		unit.active = false
		unit.activeState = mockActiveStateInactive
		unit.subState = mockSubStateDead
	}
	return nil
}

func (m *mockSystemdConnection) Reload(ctx context.Context, unitName string) error {
	if m.OnReloadFunc != nil {
		if err := m.OnReloadFunc(ctx, unitName); err != nil {
			return err
		}
	}

	// Default behavior: no-op (reload doesn't change state)
	return nil
}

func (m *mockSystemdConnection) Preset(ctx context.Context, unitName string) error {
	if m.OnPresetFunc != nil {
		if err := m.OnPresetFunc(ctx, unitName); err != nil {
			return err
		}
	}

	// Default behavior: disable the unit (common preset default)
	if unit, ok := m.units[unitName]; ok {
		unit.enabled = false
	}
	return nil
}

func (m *mockSystemdConnection) ReloadDaemon(ctx context.Context) error {
	if m.OnReloadDaemonFunc != nil {
		if err := m.OnReloadDaemonFunc(ctx); err != nil {
			return err
		}
	}

	// Default behavior: no-op
	return nil
}

func (m *mockSystemdConnection) ListUnits(ctx context.Context) (map[string]systemddbus.UnitStatus, error) {
	if m.OnListUnitsFunc != nil {
		units, err := m.OnListUnitsFunc(ctx)
		if err != nil {
			return nil, err
		}
		return units, nil
	}

	// Default behavior: return all units from the state map
	result := make(map[string]systemddbus.UnitStatus)
	for name, unit := range m.units {
		result[name] = systemddbus.UnitStatus{
			Name:        unit.name,
			LoadState:   unit.loadState,
			ActiveState: unit.activeState,
			SubState:    unit.subState,
		}
	}
	return result, nil
}

func (m *mockSystemdConnection) Close() {
	// No-op for mock
}

// mockSystemdManager is a mock implementation of SystemdManager for testing
type mockSystemdManager struct {
	// The connection to return from NewConnection
	connection SystemdConnection

	// Callback functions - if set, these are called instead of default behavior
	// The callback can perform assertions, modify state, and return errors
	OnNewConnectionFunc func(ctx context.Context) (SystemdConnection, error)
	OnDoConnectionFunc  func(ctx context.Context, fn SystemdManagerDoConnectionFn) error
}

// newMockSystemdManager creates a new mock systemd manager with a default mock connection
func newMockSystemdManager(units map[string]*mockUnitState) *mockSystemdManager {
	return &mockSystemdManager{
		connection: newMockSystemdConnection(units),
	}
}

// newMockSystemdManagerWithConnection creates a new mock systemd manager with a specific connection
func newMockSystemdManagerWithConnection(conn SystemdConnection) *mockSystemdManager {
	return &mockSystemdManager{
		connection: conn,
	}
}

// NewConnection returns the configured connection or calls the callback
func (m *mockSystemdManager) NewConnection(ctx context.Context) (SystemdConnection, error) {
	if m.OnNewConnectionFunc != nil {
		conn, err := m.OnNewConnectionFunc(ctx)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}

	// Default behavior: return the configured connection
	if m.connection == nil {
		return nil, fmt.Errorf("no connection configured")
	}
	return m.connection, nil
}

// DoConnection executes a function with a systemd connection, managing the connection lifecycle
func (m *mockSystemdManager) DoConnection(ctx context.Context, fn SystemdManagerDoConnectionFn) error {
	if m.OnDoConnectionFunc != nil {
		// Callback completely replaces default behavior
		return m.OnDoConnectionFunc(ctx, fn)
	}

	// Default behavior: create connection, call fn, close connection
	conn, err := m.NewConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(ctx, conn)
}
