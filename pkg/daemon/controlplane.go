package daemon

// This file provides changes that we make to the control plane
// only.

import (
	"fmt"
	"os/exec"
	"time"
)

const (
	// controlPlaneUpdateDelay is a time period we wait to quiece for control plane node updates
	controlPlaneUpdateDelay = 3 * time.Minute
)

// updateOstreeObjectSync enables "per-object-fsync" which helps avoid
// latency spikes for etcd; see https://github.com/ostreedev/ostree/pull/2152
func updateOstreeObjectSync() error {
	if err := exec.Command("ostree", "--repo=/sysroot/ostree/repo", "config", "set", "core.per-object-fsync", "true").Run(); err != nil {
		return fmt.Errorf("failed to set per-object-fsync for ostree: %w", err)
	}
	return nil
}

// initializeControlPlane performs setup for the node that should
// only occur on the control plane.  This used to set the IO
// scheduler too but we now only do that late in the process when
// we go to start an OS update.
func (dn *Daemon) initializeControlPlane() error {
	if err := updateOstreeObjectSync(); err != nil {
		return err
	}
	return nil
}
