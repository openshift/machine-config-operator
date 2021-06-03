package daemon

import (
	"fmt"
	"time"

	"github.com/golang/glog"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubectl/pkg/drain"
)

func (dn *Daemon) drainRequired() bool {
	// Drain operation is not useful on a single node cluster as there
	// is no other node in the cluster where workload with PDB set
	// can be rescheduled. It can lead to node being stuck at drain indefinitely.
	// These clusters can take advantage of graceful node shutdown feature.
	// https://kubernetes.io/docs/concepts/architecture/nodes/#graceful-node-shutdown
	return !isSingleNodeTopology(dn.getControlPlaneTopology())
}

func (dn *Daemon) cordonOrUncordonNode(desired bool) error {
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 10 * time.Second,
		Factor:   2,
	}
	var lastErr error
	if err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		err := drain.RunCordonOrUncordon(dn.drainer, dn.node, desired)
		if err != nil {
			lastErr = err
			glog.Infof("cordon/uncordon failed with: %v, retrying", err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			return errors.Wrapf(lastErr, "failed to cordon/uncordon node (%d tries): %v", backoff.Steps, err)
		}
		return errors.Wrap(err, "failed to cordon/uncordon node")
	}
	return nil
}

func (dn *Daemon) drain() error {
	done := make(chan bool, 1)

	drainer := func() chan error {
		ret := make(chan error)
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					if err := drain.RunNodeDrain(dn.drainer, dn.node.Name); err != nil {
						glog.Infof("Draining failed with: %v, retrying", err)
						time.Sleep(5 * time.Minute)
						continue
					}
					close(ret)
					return
				}
			}
		}()
		return ret
	}

	select {
	case <-time.After(1 * time.Hour):
		done <- true
		failMsg := fmt.Sprintf("failed to drain node : %s after 1 hour", dn.node.Name)
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeWarning, "FailedToDrain", failMsg)
		return errors.New(failMsg)
	case <-drainer():
		return nil
	}
}

func (dn *Daemon) performDrain() error {
	// Skip drain process when we're not cluster driven
	if dn.kubeClient == nil {
		return nil
	}

	if err := dn.cordonOrUncordonNode(true); err != nil {
		return err
	}
	dn.logSystem("Node has been successfully cordoned")
	dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "Cordon", "Cordoned node to apply update")

	if !dn.drainRequired() {
		dn.logSystem("Drain not required, skipping")
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "Drain", "Drain not required, skipping")
		return nil
	}

	// We are here, that means we need to cordon and drain node
	MCDDrainErr.WithLabelValues(dn.node.Name, "").Set(0)
	dn.logSystem("Update prepared; beginning drain")
	startTime := time.Now()

	dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "Drain", "Draining node to update config.")

	if err := dn.drain(); err != nil {
		return err
	}

	dn.logSystem("drain complete")
	t := time.Since(startTime).Seconds()
	glog.Infof("Successful drain took %v seconds", t)
	MCDDrainErr.WithLabelValues(dn.node.Name, "").Set(0)

	return nil
}
