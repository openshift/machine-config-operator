// Package daemon provides systemd integration for the Machine Config Daemon.
// It implements a factory pattern for managing systemd connections and operations.
//
// The factory pattern allows for efficient connection reuse:
//   - Use SystemdManager.NewConnection() for multiple operations on a single connection
//   - Use SystemdManager.DoConnection() for one-off operations with automatic cleanup
//
// All systemd unit operations automatically normalize unit names to include proper
// suffixes (e.g., "crio" becomes "crio.service") to comply with D-Bus requirements.
package daemon

import (
	"context"
	"fmt"
	"maps"
	"strings"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

// NormalizeSystemdUnitNames ensures unit names have valid systemd suffixes.
// D-Bus requires the full unit name (e.g., "crio.service") while systemctl
// accepts short names (e.g., "crio"). This function adds ".service" if no
// recognized suffix is present.
//
// Valid systemd unit suffixes are:
//
//	.service, .socket, .device, .mount, .automount, .swap,
//	.target, .path, .timer, .slice, .scope
func NormalizeSystemdUnitNames(units ...string) []string {
	validSuffixes := []string{
		".service",
		".socket",
		".device",
		".mount",
		".automount",
		".swap",
		".target",
		".path",
		".timer",
		".slice",
		".scope",
	}

	normalized := make([]string, len(units))
	for i, unit := range units {
		hasValidSuffix := false
		for _, suffix := range validSuffixes {
			if strings.HasSuffix(unit, suffix) {
				hasValidSuffix = true
				break
			}
		}

		if hasValidSuffix {
			normalized[i] = unit
		} else {
			// No recognized suffix, assume it's a service unit
			normalized[i] = unit + ".service"
		}
	}

	return normalized
}

// SystemdConnection represents an active connection to systemd that can perform multiple operations
// without reconnecting. Users should call Close() when done to release resources.
type SystemdConnection interface {
	// Enable enables one or more systemd units.
	Enable(ctx context.Context, force bool, units ...string) error

	// Disable disables one or more systemd units.
	Disable(ctx context.Context, units ...string) error

	// IsEnabled checks if a systemd unit is enabled.
	IsEnabled(ctx context.Context, unit string) (bool, error)

	// TryRestart tries to restart a systemd unit (only if it's already running).
	TryRestart(ctx context.Context, unit string) error

	// Restart restarts a systemd unit.
	Restart(ctx context.Context, unit string) error

	// Start starts a systemd unit.
	Start(ctx context.Context, unit string) error

	// Stop stops a systemd unit.
	Stop(ctx context.Context, unit string) error

	// Reload reloads a systemd unit.
	Reload(ctx context.Context, unit string) error

	// Preset resets a systemd unit to its preset state.
	Preset(ctx context.Context, unit string) error

	// ReloadDaemon reloads the systemd daemon configuration.
	ReloadDaemon(ctx context.Context) error

	// ListUnits retrieves all currently loaded systemd units and returns them as a map indexed by unit name.
	ListUnits(ctx context.Context) (map[string]systemddbus.UnitStatus, error)

	// Close closes the systemd connection and releases resources.
	Close()
}

// SystemdManager is a factory for creating systemd connections.
type SystemdManager interface {
	// NewConnection creates a new connection to systemd for multiple operations
	// that benefit from connection reuse. Caller must call Close() when done.
	NewConnection(ctx context.Context) (SystemdConnection, error)

	// DoConnection performs a one-off operation with automatic connection cleanup.
	// The connection is automatically closed after the function executes.
	DoConnection(ctx context.Context, fn SystemdManagerDoConnectionFn) error
}

// SystemdManagerDefault is the default implementation of SystemdManager.
type SystemdManagerDefault struct{}

// NewSystemdManagerDefault creates a new SystemdManagerDefault
func NewSystemdManagerDefault() *SystemdManagerDefault {
	return &SystemdManagerDefault{}
}

// NewConnection creates a new connection to systemd
func (s *SystemdManagerDefault) NewConnection(ctx context.Context) (SystemdConnection, error) {
	conn, err := systemddbus.NewSystemdConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to systemd: %w", err)
	}
	return &systemdConnectionImpl{conn: conn}, nil
}

// SystemdManagerDoConnectionFn is a function type for operations that use a SystemdConnection.
// Used with DoConnection for one-off operations with automatic cleanup.
type SystemdManagerDoConnectionFn func(ctx context.Context, conn SystemdConnection) error

// DoConnection creates a new connection, executes the provided function, and automatically
// closes the connection. Use this for one-off operations.
func (s *SystemdManagerDefault) DoConnection(ctx context.Context, fn SystemdManagerDoConnectionFn) error {
	conn, err := s.NewConnection(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(ctx, conn)
}

// systemdConnectionImpl implements SystemdConnection.
type systemdConnectionImpl struct {
	conn *systemddbus.Conn
}

// Close closes the systemd connection
func (s *systemdConnectionImpl) Close() {
	s.conn.Close()
}

// ReloadDaemon reloads the systemd daemon configuration
func (s *systemdConnectionImpl) ReloadDaemon(ctx context.Context) error {
	if err := s.conn.ReloadContext(ctx); err != nil {
		return fmt.Errorf("failed to reload systemd daemon: %w", err)
	}
	return nil
}

// SystemdReloadDaemon returns a function that reloads the systemd daemon configuration.
// Use with DoConnection for one-off systemd daemon reload operations.
func SystemdReloadDaemon() SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.ReloadDaemon(ctx)
	}
}

// SystemdEnable returns a function that enables one or more systemd units.
// Use with DoConnection for one-off enable operations.
func SystemdEnable(force bool, units ...string) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.Enable(ctx, force, units...)
	}
}

// SystemdDisable returns a function that disables one or more systemd units.
// Use with DoConnection for one-off disable operations.
func SystemdDisable(units ...string) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.Disable(ctx, units...)
	}
}

// SystemdIsEnabled returns a function that checks if a systemd unit is enabled.
// The result is stored in the provided enabled pointer.
// Use with DoConnection for one-off check operations.
func SystemdIsEnabled(unit string, enabled *bool) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		result, err := conn.IsEnabled(ctx, unit)
		if err != nil {
			return err
		}
		*enabled = result
		return nil
	}
}

// SystemdTryRestart returns a function that tries to restart a systemd unit.
// The unit is only restarted if it's already running.
// Use with DoConnection for one-off try-restart operations.
func SystemdTryRestart(unit string) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.TryRestart(ctx, unit)
	}
}

// SystemdRestart returns a function that restarts a systemd unit.
// Use with DoConnection for one-off restart operations.
func SystemdRestart(unit string) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.Restart(ctx, unit)
	}
}

// SystemdStart returns a function that starts a systemd unit.
// Use with DoConnection for one-off start operations.
func SystemdStart(unit string) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.Start(ctx, unit)
	}
}

// SystemdStop returns a function that stops a systemd unit.
// Use with DoConnection for one-off stop operations.
func SystemdStop(unit string) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.Stop(ctx, unit)
	}
}

// SystemdReload returns a function that reloads a systemd unit.
// Use with DoConnection for one-off reload operations.
func SystemdReload(unit string) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.Reload(ctx, unit)
	}
}

// SystemdPreset returns a function that resets a systemd unit to its preset state.
// Use with DoConnection for one-off preset operations.
func SystemdPreset(unit string) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		return conn.Preset(ctx, unit)
	}
}

// SystemdListUnits returns a function that retrieves all currently loaded systemd units.
// The units are copied into the provided result map indexed by unit name.
// Use with DoConnection for one-off list units operations.
func SystemdListUnits(result map[string]systemddbus.UnitStatus) SystemdManagerDoConnectionFn {
	return func(ctx context.Context, conn SystemdConnection) error {
		units, err := conn.ListUnits(ctx)
		if err != nil {
			return err
		}
		maps.Copy(result, units)
		return nil
	}
}

// Enable enables one or more systemd units. Unit names are automatically normalized.
func (s *systemdConnectionImpl) Enable(ctx context.Context, force bool, units ...string) error {
	normalizedUnits := NormalizeSystemdUnitNames(units...)
	if _, _, err := s.conn.EnableUnitFilesContext(ctx, normalizedUnits, false, force); err != nil {
		return fmt.Errorf("enabling systemd units %v: %w", normalizedUnits, err)
	}
	return nil
}

// Disable disables one or more systemd units. Unit names are automatically normalized.
func (s *systemdConnectionImpl) Disable(ctx context.Context, units ...string) error {
	normalizedUnits := NormalizeSystemdUnitNames(units...)
	if _, err := s.conn.DisableUnitFilesContext(ctx, normalizedUnits, false); err != nil {
		return fmt.Errorf("disabling systemd units %v: %w", normalizedUnits, err)
	}
	return nil
}

// TryRestart tries to restart a systemd unit (only if it's already running). Unit name is automatically normalized.
func (s *systemdConnectionImpl) TryRestart(ctx context.Context, unit string) error {
	normalizedName := NormalizeSystemdUnitNames(unit)[0]
	if _, err := s.conn.TryRestartUnitContext(ctx, normalizedName, "replace", nil); err != nil {
		return fmt.Errorf("try-restarting systemd unit %q: %w", normalizedName, err)
	}
	return nil
}

// Restart restarts a systemd unit. Unit name is automatically normalized.
func (s *systemdConnectionImpl) Restart(ctx context.Context, unit string) error {
	normalizedName := NormalizeSystemdUnitNames(unit)[0]
	if _, err := s.conn.RestartUnitContext(ctx, normalizedName, "replace", nil); err != nil {
		return fmt.Errorf("restarting systemd unit %q: %w", normalizedName, err)
	}
	return nil
}

// Start starts a systemd unit. Unit name is automatically normalized.
func (s *systemdConnectionImpl) Start(ctx context.Context, unit string) error {
	normalizedName := NormalizeSystemdUnitNames(unit)[0]
	if _, err := s.conn.StartUnitContext(ctx, normalizedName, "replace", nil); err != nil {
		return fmt.Errorf("starting systemd unit %q: %w", normalizedName, err)
	}
	return nil
}

// Stop stops a systemd unit. Unit name is automatically normalized.
func (s *systemdConnectionImpl) Stop(ctx context.Context, unit string) error {
	normalizedName := NormalizeSystemdUnitNames(unit)[0]
	if _, err := s.conn.StopUnitContext(ctx, normalizedName, "replace", nil); err != nil {
		return fmt.Errorf("stopping systemd unit %q: %w", normalizedName, err)
	}
	return nil
}

// Reload reloads a systemd unit. Unit name is automatically normalized.
func (s *systemdConnectionImpl) Reload(ctx context.Context, unit string) error {
	normalizedName := NormalizeSystemdUnitNames(unit)[0]
	if _, err := s.conn.ReloadUnitContext(ctx, normalizedName, "replace", nil); err != nil {
		return fmt.Errorf("reloading systemd unit %q: %w", normalizedName, err)
	}
	return nil
}

// Preset resets a systemd unit to its preset state. Unit name is automatically normalized.
//
// Note: This method uses the low-level dbus library directly because
// PresetUnitFiles is not exposed by the coreos/go-systemd library.
func (s *systemdConnectionImpl) Preset(ctx context.Context, unit string) error {
	dbusConn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("failed to connect to system bus: %w", err)
	}

	obj := dbusConn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1")

	// Call PresetUnitFiles: takes files array, runtime bool, force bool
	// Returns changes array and carries_install_info bool
	var carriesInstallInfo bool
	var changes [][]interface{}

	normalizedName := NormalizeSystemdUnitNames(unit)[0]
	err = obj.CallWithContext(
		ctx,
		"org.freedesktop.systemd1.Manager.PresetUnitFiles",
		0,
		[]string{normalizedName},
		false, false,
	).Store(&carriesInstallInfo, &changes)
	if err != nil {
		return fmt.Errorf("presetting systemd unit %q: %w", normalizedName, err)
	}

	return nil
}

// IsEnabled checks if a systemd unit is enabled. Unit name is automatically normalized.
func (s *systemdConnectionImpl) IsEnabled(ctx context.Context, unit string) (bool, error) {
	normalizedName := NormalizeSystemdUnitNames(unit)[0]
	enabledUnits, err := s.conn.ListUnitFilesByPatternsContext(
		ctx,
		[]string{"enabled", "enabled-runtime"},
		[]string{normalizedName},
	)
	if err != nil {
		return false, fmt.Errorf("checking if unit %q is enabled: %w", normalizedName, err)
	}

	return len(enabledUnits) > 0, nil
}

// ListUnits retrieves all currently loaded systemd units and returns them as a map indexed by unit name.
// Only units that are both active and/or enabled will be returned.
func (s *systemdConnectionImpl) ListUnits(ctx context.Context) (map[string]systemddbus.UnitStatus, error) {
	units, err := s.conn.ListUnitsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting units list: %w", err)
	}

	result := make(map[string]systemddbus.UnitStatus)
	for _, unit := range units {
		result[unit.Name] = unit
	}
	return result, nil
}
