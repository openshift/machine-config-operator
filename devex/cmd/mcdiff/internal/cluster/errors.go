package cluster

import (
	"errors"
	"fmt"
)

var (
	// ErrPoolNotFound is returned when the MachineConfigPool does not exist.
	ErrPoolNotFound = errors.New("machineconfigpool not found")
	// ErrNoRenderedConfiguration is returned when the pool has no rendered MachineConfig name.
	ErrNoRenderedConfiguration = errors.New("pool has no rendered configuration")
	// ErrRenderedNotFound is returned when the named rendered MachineConfig does not exist.
	ErrRenderedNotFound = errors.New("rendered machineconfig not found")
	// ErrSourceUnavailable is returned when one or more source MachineConfigs cannot be retrieved.
	// LoadPoolFile still returns expected content from the rendered MachineConfig when this is set
	// on PoolFile.AttributionErr.
	ErrSourceUnavailable = errors.New("source machineconfigs unavailable")
)

func wrapPoolNotFound(poolName string, err error) error {
	return fmt.Errorf("failed to get MachineConfigPool %q: %w: %w", poolName, ErrPoolNotFound, err)
}

func wrapNoRendered(poolName string) error {
	return fmt.Errorf("failed to resolve rendered MachineConfig for pool %q: %w", poolName, ErrNoRenderedConfiguration)
}

func wrapRenderedNotFound(poolName, renderedName string, err error) error {
	return fmt.Errorf("failed to resolve rendered MachineConfig %q for pool %q: %w: %w", renderedName, poolName, ErrRenderedNotFound, err)
}

func wrapSourceUnavailable(poolName string, missing []string, err error) error {
	return fmt.Errorf("source MachineConfigs %v for pool %q are unavailable: %w: %w", missing, poolName, ErrSourceUnavailable, err)
}
