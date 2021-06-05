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
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 10 * time.Second,
		Factor:   2,
	}

	// fetch our CRD
	// if not timeExpired, checkSkipPods = True
	// do we need to set an Expired time?  If we are willing to release locks
	// early to let others do work if we find ourselves in a wedged situation,
	// we might be able to avoid this timeout.

	var lastErr error
	if err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		podsNotDrained := []corev1.Pod{}

		skipDrainFn := func(pod corev1.Pod) drain.PodDeleteStatus {
			glog.Infof("Calling skipDrainFn %v", pod)
			if _, exists := pod.ObjectMeta.Annotations["michaelgugino.github.com/skip-drain"]; exists {
				// We need to not skip some pods here such as Pending, Failed,
				// and possibly other states/conditions, or we might wedge later.
				glog.Infof("Found skip drain annotation on pod %v/%v", pod.Namespace, pod.Name)
				podsNotDrained = append(podsNotDrained, pod)
				return drain.MakePodDeleteStatusSkip()
			}
			return drain.MakePodDeleteStatusOkay()
		}
		// if checkSkipPods
		dn.drainer.AdditionalFilters = []drain.PodFilter{skipDrainFn}
		// endif
		err := drain.RunNodeDrain(dn.drainer, dn.node.Name)
		if err != nil {
			lastErr = err
			glog.Infof("Draining failed with: %v, retrying", err)
			return false, nil
		}
		for _, p := range(podsNotDrained) {
			glog.Infof("Pod with annotation to skip draining: %v/%v", p.Name, p.Namespace)
		}
		dn.drainer.AdditionalFilters = []drain.PodFilter{}
		// copy podsNotDrained to outerPodsNotDrained
		return true, nil

	}); err != nil {
		if err == wait.ErrWaitTimeout {
			failMsg := fmt.Sprintf("%d tries: %v", backoff.Steps, lastErr)
			MCDDrainErr.WithLabelValues(dn.node.Name, "WaitTimeout").Set(float64(backoff.Steps))
			dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeWarning, "FailedToDrain", failMsg)
			return errors.Wrapf(lastErr, "failed to drain node (%d tries): %v", backoff.Steps, err)
		}
		MCDDrainErr.WithLabelValues(dn.node.Name, "UnknownError").Set(float64(backoff.Steps))
		dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeWarning, "FailedToDrain", err.Error())
		return errors.Wrap(err, "failed to drain node")
	}

	// err := nil
	// if checkSkipPods && len(outerPodsNotDrained) > 0
		// sleep 10 seconds so we don't jump the line
		// acquire global drain lock or return err
			// put a retry-backoff here of a few minutes so we don't jump the line
		// fetch PDBs needed
		// update CRD with timestamp, list of pods, PDBs
			// make sure we only append to the PDB list here so we can unlock
			// any PDBs even if the corresponding pod goes missing during a
			// retry loop
		// evaluate PDBs
			// if (all PDBs are unlocked, or locked by us), and all healthy
				// lock PDBs that need our lock
				// update CRD to note which PDBs were successfully locked
				// capture any errors
			// else if (some PDBs locked by me and some by others) or not all healthy
				// release global drain lock
					// release the global lock early as we're only unlocking our
					// own locks at this point.  This will allow us to put
					// retry timeout on the lock operation to avoid having to
					// get the global lock again to complete in case of temporary
					// network/api issue.
				// unlock my PDBs, retry backoff, if timeout, send warning
				// accross all available channels
					// as somebody might have read stale mutex and
					// we'll both be wedged potentially, or some application/admin
					// lock an app, but we should unlock other apps so other nodes
					// will be unimpeded.
					// Similarly, if there is one unhealthy PDB in our group, we
					// don't want to lock the others indefinitely and cause a
					// traffic jam.  Unlocking will potentially let others have a
					// shot at proceeding
			// else capture any errors
		// release global drain lock

	// return err
	return nil
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
