package daemon

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/golang/glog"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

func (dn *Daemon) drainRequired() bool {
	// Drain operation is not useful on a single node cluster as there
	// is no other node in the cluster where workload with PDB set
	// can be rescheduled. It can lead to node being stuck at drain indefinitely.
	// These clusters can take advantage of graceful node shutdown feature.
	// https://kubernetes.io/docs/concepts/architecture/nodes/#graceful-node-shutdown
	return !isSingleNodeTopology(dn.getControlPlaneTopology())
}

func (dn *Daemon) performDrain() error {
	// Skip drain process when we're not cluster driven
	if dn.kubeClient == nil {
		return nil
	}

	if !dn.drainRequired() {
		logSystem("Drain not required, skipping")
		dn.nodeWriter.Eventf(corev1.EventTypeNormal, "Drain", "Drain not required, skipping")
		return nil
	}

	desiredConfigName, err := getNodeAnnotation(dn.node, constants.DesiredMachineConfigAnnotationKey)
	if err != nil {
		return err
	}
	desiredDrainAnnotationValue := fmt.Sprintf("%s-%s", "drain", desiredConfigName)
	if dn.node.Annotations[constants.LastAppliedDrainerAnnotationKey] == desiredDrainAnnotationValue {
		// We should only enter this in one of three scenarios:
		// A previous drain timed out, and the controller succeeded while we waited for the next sync
		// Or the MCD restarted for some reason
		// Or we are forcing an update
		// In either case, we just assume the drain we want has been completed
		// A third case we can hit this is that the node fails validation upon reboot, and then we use
		// the forcefile. But in that case, since we never uncordoned, skipping the drain should be fine
		logSystem("drain is already completed on this node")
		return nil
	}

	// We are here, that means we need to cordon and drain node
	logSystem("Update prepared; requesting cordon and drain via annotation to controller")
	startTime := time.Now()

	// We probably don't need to separate out cordon and drain, but we sort of do it today for various scenarios
	// TODO (jerzhang): revisit
	dn.nodeWriter.Eventf(corev1.EventTypeNormal, "Cordon", "Cordoned node to apply update")
	dn.nodeWriter.Eventf(corev1.EventTypeNormal, "Drain", "Draining node to update config.")

	// TODO (jerzhang): definitely don't have to block here, but as an initial PoC, this is easier
	if err := dn.nodeWriter.SetDesiredDrainer(desiredDrainAnnotationValue); err != nil {
		return fmt.Errorf("Could not set drain annotation: %w", err)
	}

	if err := wait.Poll(10*time.Second, 1*time.Hour, func() (bool, error) {
		node, err := dn.kubeClient.CoreV1().Nodes().Get(context.TODO(), dn.name, metav1.GetOptions{})
		if err != nil {
			glog.Warningf("Failed to get node: %v", err)
			return false, nil
		}
		if node.Annotations[constants.DesiredDrainerAnnotationKey] != node.Annotations[constants.LastAppliedDrainerAnnotationKey] {
			return false, nil
		}
		return true, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			failMsg := fmt.Sprintf("failed to drain node: %s after 1 hour. Please see machine-config-controller logs for more information", dn.node.Name)
			dn.nodeWriter.Eventf(corev1.EventTypeWarning, "FailedToDrain", failMsg)
			return fmt.Errorf(failMsg)
		}
		return fmt.Errorf("Something went wrong while attempting to drain node: %v", err)
	}

	logSystem("drain complete")
	t := time.Since(startTime).Seconds()
	glog.Infof("Successful drain took %v seconds", t)

	return nil
}

// isDrainRequired determines whether node drain is required or not to apply config changes.
func isDrainRequired(actions, diffFileSet []string, oldIgnConfig, newIgnConfig ign3types.Config) (bool, error) {
	if ctrlcommon.InSlice(postConfigChangeActionReboot, actions) {
		// Node is going to reboot, we definitely want to perform drain
		return true, nil
	} else if ctrlcommon.InSlice(postConfigChangeActionReloadCrio, actions) {
		// Drain may or may not be necessary in case of container registry config changes.
		if ctrlcommon.InSlice(constants.ContainerRegistryConfPath, diffFileSet) {
			isSafe, err := isSafeContainerRegistryConfChanges(oldIgnConfig, newIgnConfig)
			if err != nil {
				return false, err
			}
			return !isSafe, nil
		}
		return false, nil
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
// 1. A new mirror that has 'pull-from-mirror=digest-only' is added
// 2. A new registry has been added that has all mirrors with 'pull-from-mirror=digest-only'
// See https://bugzilla.redhat.com/show_bug.cgi?id=1943315
//
//nolint:gocyclo
func isSafeContainerRegistryConfChanges(oldIgnConfig, newIgnConfig ign3types.Config) (bool, error) {
	// /etc/containers/registries.conf contains config in toml format. Parse the file
	oldData, err := ctrlcommon.GetIgnitionFileDataByPath(&oldIgnConfig, constants.ContainerRegistryConfPath)
	if err != nil {
		return false, fmt.Errorf("failed decoding Data URL scheme string: %w", err)
	}

	newData, err := ctrlcommon.GetIgnitionFileDataByPath(&newIgnConfig, constants.ContainerRegistryConfPath)
	if err != nil {
		return false, fmt.Errorf("failed decoding Data URL scheme string %w", err)
	}

	tomlConfOldReg := sysregistriesv2.V2RegistriesConf{}
	if _, err := toml.Decode(string(oldData), &tomlConfOldReg); err != nil {
		return false, fmt.Errorf("failed decoding TOML content from file %s: %w", constants.ContainerRegistryConfPath, err)
	}

	tomlConfNewReg := sysregistriesv2.V2RegistriesConf{}
	if _, err := toml.Decode(string(newData), &tomlConfNewReg); err != nil {
		return false, fmt.Errorf("failed decoding TOML content from file %s: %w", constants.ContainerRegistryConfPath, err)
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
			glog.Infof("%s: registry %s has been removed", constants.ContainerRegistryConfPath, regLoc)
			return false, nil
		}
	}

	// Check for modified registry
	for regLoc, newReg := range newRegHashMap {
		oldReg, ok := oldRegHashMap[regLoc]
		if ok {
			// Registry is available in both old and new config.
			if !reflect.DeepEqual(oldReg, newReg) {
				// Registry has been changed in the new config.
				// Check that changes made are safe or not.
				if oldReg.Prefix != newReg.Prefix {
					glog.Infof("%s: prefix value for registry %s has changed from %s to %s",
						constants.ContainerRegistryConfPath, regLoc, oldReg.Prefix, newReg.Prefix)
					return false, nil
				}
				if oldReg.Location != newReg.Location {
					glog.Infof("%s: location value for registry %s has changed from %s to %s",
						constants.ContainerRegistryConfPath, regLoc, oldReg.Location, newReg.Location)
					return false, nil
				}
				if oldReg.Blocked != newReg.Blocked {
					glog.Infof("%s: blocked value for registry %s has changed from %t to %t",
						constants.ContainerRegistryConfPath, regLoc, oldReg.Blocked, newReg.Blocked)
					return false, nil
				}
				if oldReg.Insecure != newReg.Insecure {
					glog.Infof("%s: insecure value for registry %s has changed from %t to %t",
						constants.ContainerRegistryConfPath, regLoc, oldReg.Insecure, newReg.Insecure)
					return false, nil
				}

				// Ensure that all the old mirrors are present
				for _, m := range oldReg.Mirrors {
					if found, _ := searchRegistryMirror(m.Location, newReg.Mirrors); !found {
						glog.Infof("%s: mirror %s has been removed in registry %s",
							constants.ContainerRegistryConfPath, m.Location, regLoc)
						return false, nil
					}
				}
				for _, m := range newReg.Mirrors {
					// Ensure that any change to current does not unset pull-from-mirror="digest-only"
					if found, oldMirror := searchRegistryMirror(m.Location, oldReg.Mirrors); found {
						if m.PullFromMirror != oldMirror.PullFromMirror && m.PullFromMirror != sysregistriesv2.MirrorByDigestOnly {
							glog.Infof("%s: pull-from-mirror value for mirror %s has changed from %s to %s ",
								constants.ContainerRegistryConfPath, m.Location, oldMirror.PullFromMirror, m.PullFromMirror)
							return false, nil
						}
					}
					// Ensure that any added mirror has set pull-from-mirror="digest-only"
					if found, _ := searchRegistryMirror(m.Location, oldReg.Mirrors); !found {
						if m.PullFromMirror != sysregistriesv2.MirrorByDigestOnly && !newReg.MirrorByDigestOnly {
							glog.Infof("%s: mirror %s has been added in registry %s that has pull-from-mirror set to %s ",
								constants.ContainerRegistryConfPath, m.Location, regLoc, m.PullFromMirror)
							return false, nil
						}

					}
				}
			}
		} else if !allDigestOnlyMirror(newReg) {
			// Ensure that each mirror under the newReg has pull-from-mirror=digest-only
			glog.Infof("%s: registry %s has been added with mirror does not set pull-from-mirror=digest-only",
				constants.ContainerRegistryConfPath, regLoc)
			return false, nil
		}
	}

	glog.Infof("%s: changes made are safe to skip drain", constants.ContainerRegistryConfPath)
	return true, nil
}

// searchRegistryMirror does lookup of a mirror in the mirrorList specified for a registry
// Returns true if found
func searchRegistryMirror(loc string, mirrors []sysregistriesv2.Endpoint) (bool, sysregistriesv2.Endpoint) {
	found := false
	for _, m := range mirrors {
		if m.Location == loc {
			found = true
			return found, m
		}
	}
	return found, sysregistriesv2.Endpoint{}
}

func allDigestOnlyMirror(reg sysregistriesv2.Registry) bool {
	if len(reg.Mirrors) == 0 {
		return reg.MirrorByDigestOnly
	}
	for _, m := range reg.Mirrors {
		if m.PullFromMirror != sysregistriesv2.MirrorByDigestOnly {
			return false
		}
	}
	return true
}
