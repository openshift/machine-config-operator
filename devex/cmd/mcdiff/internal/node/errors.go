package node

import (
	"errors"
	"fmt"
)

var (
	// ErrNodeNotFound is returned when the named Node does not exist.
	ErrNodeNotFound = errors.New("node not found")
	// ErrFileNotFound is returned when the path does not exist on the node's host filesystem.
	// Distinguishes a missing file from an empty file (empty file returns nil error and zero-length content).
	ErrFileNotFound = errors.New("file not found on node")
	// ErrPermissionDenied is returned when the host file cannot be read.
	ErrPermissionDenied = errors.New("permission denied reading node file")
	// ErrMCDUnavailable is returned when the machine-config-daemon pod cannot be used for exec.
	ErrMCDUnavailable = errors.New("machine-config-daemon pod unavailable")
)

func wrapNodeNotFound(nodeName string, err error) error {
	return fmt.Errorf("failed to get node %q: %w: %w", nodeName, ErrNodeNotFound, err)
}

func wrapFileNotFound(nodeName, path string) error {
	return fmt.Errorf("file %q is missing on node %q: %w", path, nodeName, ErrFileNotFound)
}

func wrapPermissionDenied(nodeName, path string, err error) error {
	if err == nil {
		return fmt.Errorf("permission denied reading %q on node %q: %w", path, nodeName, ErrPermissionDenied)
	}
	return fmt.Errorf("permission denied reading %q on node %q: %w: %w", path, nodeName, ErrPermissionDenied, err)
}

func wrapMCDUnavailable(nodeName string, err error) error {
	if err == nil {
		return fmt.Errorf("machine-config-daemon on node %q is unavailable: %w", nodeName, ErrMCDUnavailable)
	}
	return fmt.Errorf("machine-config-daemon on node %q is unavailable: %w: %w", nodeName, ErrMCDUnavailable, err)
}
