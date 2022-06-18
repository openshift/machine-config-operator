package daemon

// This file provides changes that we make to the control plane
// only.

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
)

// setRootDeviceSchedulerBFQ switches to the `bfq` I/O scheduler
// for the root block device to better share I/O between etcd
// and other processes.  See
// https://github.com/openshift/machine-config-operator/issues/1897
// Note this is the current systemd default in Fedora, but not RHEL8,
// except for NVMe devices.
func setRootDeviceSchedulerBFQ() error {
	sched := "bfq"

	rootDevSysfs, err := getRootBlockDeviceSysfs()
	if err != nil {
		return err
	}

	schedulerPath := filepath.Join(rootDevSysfs, "/queue/scheduler")
	schedulerContentsBuf, err := ioutil.ReadFile(schedulerPath)
	if err != nil {
		return err
	}
	schedulerContents := string(schedulerContentsBuf)
	schedSupported := false
	for _, v := range strings.Split(schedulerContents, " ") {
		switch v {
		case fmt.Sprintf("[%s]", sched):
			glog.Infof("Device %s already uses scheduler %s", rootDevSysfs, sched)
			return nil
		case sched:
			schedSupported = true
			break
		}
	}
	if !schedSupported {
		glog.Infof("Device %s does not support the %s scheduler", rootDevSysfs, sched)
		return nil
	}

	f, err := os.OpenFile(schedulerPath, os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte(sched))
	if err != nil {
		return err
	}
	glog.Infof("Set root blockdev %s to use scheduler %v", rootDevSysfs, sched)

	return nil
}

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
	if err := updateOstreeObjectSync(); err != nil && dn.os.IsCoreOSVariant() {
		return err
	}
	return nil
}
