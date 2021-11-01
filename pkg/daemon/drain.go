package daemon

import (
	"fmt"
	"reflect"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/golang/glog"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/pkg/errors"
	"github.com/vincent-petithory/dataurl"
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
	verb := "cordon"
	if !desired {
		verb = "uncordon"
	}

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
			glog.Infof("%s failed with: %v, retrying", verb, err)
			return false, nil
		}
		return true, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			return errors.Wrapf(lastErr, "failed to %s node (%d tries): %v", verb, backoff.Steps, err)
		}
		return errors.Wrapf(err, "failed to %s node", verb)
	}

	return nil
}

func (dn *Daemon) drain() error {
	failedDrains := 0
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
						failedDrains++
						if failedDrains > 5 {
							time.Sleep(5 * time.Minute)
						} else {
							time.Sleep(1 * time.Minute)
						}
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
		MCDDrainErr.Set(1)
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
	dn.logSystem("Update prepared; beginning drain")
	startTime := time.Now()

	dn.recorder.Eventf(getNodeRef(dn.node), corev1.EventTypeNormal, "Drain", "Draining node to update config.")

	if err := dn.drain(); err != nil {
		return err
	}

	dn.logSystem("drain complete")
	t := time.Since(startTime).Seconds()
	glog.Infof("Successful drain took %v seconds", t)
	MCDDrainErr.Set(0)

	return nil
}

// isDrainRequired determines whether node drain is required or not to apply config changes.
func isDrainRequired(actions []string, oldIgnConfig, newIgnConfig ign3types.Config) (bool, error) {
	if ctrlcommon.InSlice(postConfigChangeActionReboot, actions) {
		// Node is going to reboot, we definitely want to perform drain
		return true, nil
	} else if ctrlcommon.InSlice(postConfigChangeActionReloadCrio, actions) {
		// Drain may or may not be necessary in case of container registry config changes.
		isSafe, err := isSafeContainerRegistryConfChanges(oldIgnConfig, newIgnConfig)
		if err != nil {
			return false, err
		}
		return !isSafe, nil
	} else if ctrlcommon.InSlice(postConfigChangeActionNone, actions) {
		return false, nil
	}
	// For any unhandled cases, default to drain
	return true, nil
}

// isSafeContainerRegistryConfChanges looks inside old and new versions of registries.conf file.
// It compares the content and determines whether changes made are safe or not. This will
// help MCD to decide whether we can skip node drain for applied changes into container
// registry.
// Currently, we consider following container registry config changes as safe to skip node drain:
// 1. A new mirror is added to an existing registry that has `mirror-by-digest-only=true`
// 2. A new registry has been added that has `mirror-by-digest-only=true`
// See https://bugzilla.redhat.com/show_bug.cgi?id=1943315
//nolint:gocyclo
func isSafeContainerRegistryConfChanges(oldIgnConfig, newIgnConfig ign3types.Config) (bool, error) {
	containerRegistryConfPath := "/etc/containers/registries.conf"
	var oldContainerRegistryConf, newContainerRegistryConf ign3types.File

	for _, f := range oldIgnConfig.Storage.Files {
		if f.Path == containerRegistryConfPath {
			oldContainerRegistryConf = f
		}
	}

	for _, f := range newIgnConfig.Storage.Files {
		if f.Path == containerRegistryConfPath {
			newContainerRegistryConf = f
		}
	}

	// /etc/containers/registries.conf contains config in toml format. Parse the file and compare the content
	oldDataURL, err := dataurl.DecodeString(*oldContainerRegistryConf.Contents.Source)
	if err != nil {
		return false, fmt.Errorf("Failed decoding Data URL scheme string: %v", err)
	}

	newDataURL, err := dataurl.DecodeString(*newContainerRegistryConf.Contents.Source)
	if err != nil {
		return false, fmt.Errorf("Failed decoding Data URL scheme string %v", err)
	}

	tomlConfOldReg := sysregistriesv2.V2RegistriesConf{}
	if _, err := toml.Decode(string(oldDataURL.Data), &tomlConfOldReg); err != nil {
		return false, fmt.Errorf("Failed decoding TOML content from file %s: %v", oldContainerRegistryConf.Path, err)
	}

	tomlConfNewReg := sysregistriesv2.V2RegistriesConf{}
	if _, err := toml.Decode(string(newDataURL.Data), &tomlConfNewReg); err != nil {
		return false, fmt.Errorf("Failed decoding TOML content from file %s: %v", oldContainerRegistryConf.Path, err)
	}

	// Ensure that any unqualified-search-registries has not been deleted
	if len(tomlConfOldReg.UnqualifiedSearchRegistries) > len(tomlConfNewReg.UnqualifiedSearchRegistries) {
		return false, nil
	}
	for i, regURL := range tomlConfOldReg.UnqualifiedSearchRegistries {
		// Order of UnqualifiedSearchRegistries matters since image lookup occurs in order
		if tomlConfNewReg.UnqualifiedSearchRegistries[i] != regURL {
			return false, nil
		}
	}

	oldRegHashMap := make(map[string]sysregistriesv2.Registry)
	for _, reg := range tomlConfOldReg.Registries {
		scope := reg.Location
		if reg.Prefix != "" {
			scope = reg.Prefix
		}
		oldRegHashMap[scope] = reg
	}

	newRegHashMap := make(map[string]sysregistriesv2.Registry)
	for _, reg := range tomlConfNewReg.Registries {
		scope := reg.Location
		if reg.Prefix != "" {
			scope = reg.Prefix
		}
		newRegHashMap[scope] = reg
	}

	// Check for removed registry
	for regLoc := range oldRegHashMap {
		_, ok := newRegHashMap[regLoc]
		if !ok {
			glog.Infof("%s: registry %s has been removed", containerRegistryConfPath, regLoc)
			return false, nil
		}
	}

	// Check for modified registry
	for regLoc, newReg := range newRegHashMap {
		oldReg, ok := oldRegHashMap[regLoc]
		if ok && !reflect.DeepEqual(oldReg, newReg) {
			// Registry is available in both old and new config and some changes has been found.
			// Check that changes made are safe or not.
			if oldReg.Prefix != newReg.Prefix {
				glog.Infof("%s: prefix value for registry %s has changed from %s to %s",
					containerRegistryConfPath, regLoc, oldReg.Prefix, newReg.Prefix)
				return false, nil
			}
			if oldReg.Location != newReg.Location {
				glog.Infof("%s: location value for registry %s has changed from %s to %s",
					containerRegistryConfPath, regLoc, oldReg.Location, newReg.Location)
				return false, nil
			}
			if oldReg.Blocked != newReg.Blocked {
				glog.Infof("%s: blocked value for registry %s has changed from %t to %t",
					containerRegistryConfPath, regLoc, oldReg.Blocked, newReg.Blocked)
				return false, nil
			}
			if oldReg.Insecure != newReg.Insecure {
				glog.Infof("%s: insecure value for registry %s has changed from %t to %t",
					containerRegistryConfPath, regLoc, oldReg.Insecure, newReg.Insecure)
				return false, nil
			}
			if oldReg.MirrorByDigestOnly == newReg.MirrorByDigestOnly {
				// Ensure that all the old mirrors are present
				for _, m := range oldReg.Mirrors {
					if !searchRegistryMirror(m.Location, newReg.Mirrors) {
						glog.Infof("%s: mirror %s has been removed in registry %s",
							containerRegistryConfPath, m.Location, regLoc)
						return false, nil
					}
				}
				// Ensure that any added mirror has mirror-by-digest-only set to true
				for _, m := range newReg.Mirrors {
					if !searchRegistryMirror(m.Location, oldReg.Mirrors) && !newReg.MirrorByDigestOnly {
						glog.Infof("%s: mirror %s has been added in registry %s that has mirror-by-digest-only set to %t ",
							containerRegistryConfPath, m.Location, regLoc, newReg.MirrorByDigestOnly)
						return false, nil
					}
				}
			} else {
				glog.Infof("%s: mirror-by-digest-only value for registry %s has changed from %t to %t",
					containerRegistryConfPath, regLoc, oldReg.MirrorByDigestOnly, newReg.MirrorByDigestOnly)
				return false, nil
			}
		} else if !newReg.MirrorByDigestOnly {
			// New mirrors added into registry but mirror-by-digest-only has been set to false
			glog.Infof("%s: registry %s has been added with mirror-by-digest-only set to %t",
				containerRegistryConfPath, regLoc, newReg.MirrorByDigestOnly)
			return false, nil
		}
	}

	glog.Infof("%s: changes made are safe to skip drain", containerRegistryConfPath)
	return true, nil
}

// searchRegistryMirror does lookup of a mirror in the mirroList specified for a registry
// Returns true if found
func searchRegistryMirror(loc string, mirrors []sysregistriesv2.Endpoint) bool {
	found := false
	for _, m := range mirrors {
		if m.Location == loc {
			found = true
			break
		}
	}
	return found
}
