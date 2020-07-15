package daemon

import (
	"fmt"
	"reflect"
	"strings"

	"k8s.io/client-go/tools/record"

	"github.com/coreos/go-systemd/dbus"
	igntypes "github.com/coreos/ignition/v2/config/v3_1/types"
	mapset "github.com/deckarep/golang-set"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

type ConfigUpdateAction interface {
	Describe() string
	Execute(dn *Daemon, newConfig *mcfgv1.MachineConfig) error
}

type RebootPostAction struct {
	ConfigUpdateAction

	Reason string
}

func (a RebootPostAction) Describe() string {
	return fmt.Sprintf("Rebooting node: %v", a.Reason)
}

func (a RebootPostAction) Execute(dn *Daemon, newConfig *mcfgv1.MachineConfig) error {
	return dn.finalizeAndReboot(newConfig)
}

type ServicePostAction struct {
	ConfigUpdateAction

	Reason string

	ServiceName   string
	ServiceAction string
}

func (a ServicePostAction) Describe() string {
	return fmt.Sprintf("Restarting service %v", a.Reason)
}

func (a ServicePostAction) Execute(dn *Daemon, newConfig *mcfgv1.MachineConfig) error {
	// TODO: add support for stop and reload operations if necessary
	// For now only restart operation is supported

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.V(2).Infof)

	systemdConnection, dbusConnErr := dbus.NewSystemConnection()
	if dbusConnErr != nil {
		glog.Warningf("Unable to establish systemd dbus connection: %s", dbusConnErr)
		return dbusConnErr
	}

	defer systemdConnection.Close()

	var err error
	outputChannel := make(chan string)
	switch a.ServiceAction {
	case "restart":
		glog.Infof("Restarting unit %q", a.ServiceName)
		_, err = systemdConnection.RestartUnit(a.ServiceName, "replace", outputChannel)
	default:
		return fmt.Errorf("Unhandled systemd action %q for %q", a.ServiceAction, a.ServiceName)
	}

	if err != nil {
		return fmt.Errorf("Running systemd action failed: %s", err)
	}

	// If the provided channel is non-nil, a result string will be sent to it upon
	// job completion
	output := <-outputChannel
	switch output {
	// one of: done, canceled, timeout, failed, dependency, skipped.
	case "done":
		glog.Infof("Systemd action %q for %q completed successful: %s", a.ServiceAction, a.ServiceName, output)
	case "skipped":
		// The code suggests that 'skipped indicates that a job was
		// skipped because it didn't apply to the units current state'
		//
		// This should only apply to stop and start actions which we
		// don't support yet so treat it like an error for now
		return fmt.Errorf("Systemd action %q for %q was skipped: %s", a.ServiceAction, a.ServiceName, output)
	default:
		return fmt.Errorf("Systemd action %q for %q failed: %s", a.ServiceAction, a.ServiceName, output)
	}
	return nil
}

func getFileNames(files []igntypes.File) []interface{} {
	names := make([]interface{}, len(files))
	for i, file := range files {
		names[i] = file.Path
	}
	return names
}

func filesToMap(files []igntypes.File) map[string]igntypes.File {
	fileMap := make(map[string]igntypes.File, len(files))
	for _, file := range files {
		fileMap[file.Path] = file
	}
	return fileMap
}

type ChangeStrategy struct {
	actions []ConfigUpdateAction
}

func lookupStrategy(stripPrefix, filePath string) ([]ConfigUpdateAction, error) {

	strategies := map[string]ChangeStrategy{}

	key := filePath
	if len(stripPrefix) > 0 {
		key = strings.TrimPrefix(filePath, stripPrefix)
	}

	if strategy, ok := strategies[key]; ok {
		return strategy.actions, nil
	}
	return []ConfigUpdateAction{}, fmt.Errorf("Default strategy for applying changes to %q", key)
}

func getFileChanges(stripPrefix string, oldIgnConfig, newIgnConfig igntypes.Config) []ConfigUpdateAction {
	actions := []ConfigUpdateAction{}

	oldFiles := mapset.NewSetFromSlice(getFileNames(oldIgnConfig.Storage.Files))
	newFiles := mapset.NewSetFromSlice(getFileNames(newIgnConfig.Storage.Files))

	for filename := range newFiles.Difference(oldFiles).Iter() {
		return []ConfigUpdateAction{RebootPostAction{Reason: fmt.Sprintf("File %q was added", filename.(string))}}
	}

	for filename := range oldFiles.Difference(newFiles).Iter() {
		return []ConfigUpdateAction{RebootPostAction{Reason: fmt.Sprintf("File %q was removed", filename.(string))}}
	}

	newFilesMap := filesToMap(newIgnConfig.Storage.Files)
	for file := range newFiles.Intersect(oldFiles).Iter() {
		candidate := newFilesMap[file.(string)]
		if err := checkV3Files([]igntypes.File{candidate}); err != nil {
			strategyActions, err := lookupStrategy(stripPrefix, candidate.Node.Path)
			if err == nil {
				for _, a := range strategyActions {
					actions = append(actions, a)
				}
			} else {
				return []ConfigUpdateAction{RebootPostAction{Reason: err.Error()}}
			}
		}
	}

	return actions
}

func calculateActions(stripPrefix string, oldConfig, newConfig *mcfgv1.MachineConfig, diff *machineConfigDiff) []ConfigUpdateAction {

	if diff.osUpdate || diff.kargs || diff.fips || diff.kernelType {
		return []ConfigUpdateAction{RebootPostAction{Reason: "OS/Kernel changed"}}
	}

	oldIgnConfig, err := ctrlcommon.ParseAndConvertConfig(oldConfig.Spec.Config.Raw)
	if err != nil {
		return []ConfigUpdateAction{RebootPostAction{
			Reason: fmt.Sprintf("parsing old Ignition config failed with error: %v", err)}}
	}
	newIgnConfig, err := ctrlcommon.ParseAndConvertConfig(newConfig.Spec.Config.Raw)
	if err != nil {
		return []ConfigUpdateAction{RebootPostAction{
			Reason: fmt.Sprintf("parsing new Ignition config failed with error: %v", err)}}
	}

	// Check for any changes not already excluded by Reconcilable()
	// Alternatively, fold this code into that function
	if !reflect.DeepEqual(oldIgnConfig.Ignition, newIgnConfig.Ignition) {
		return []ConfigUpdateAction{RebootPostAction{Reason: "Ignition changed"}}
	}
	if !reflect.DeepEqual(oldIgnConfig.Passwd, newIgnConfig.Passwd) {
		return []ConfigUpdateAction{RebootPostAction{Reason: "Passwords changed"}}
	}
	if !reflect.DeepEqual(oldIgnConfig.Systemd, newIgnConfig.Systemd) {
		return []ConfigUpdateAction{RebootPostAction{Reason: "Systemd configuration changed"}}
	}
	if !reflect.DeepEqual(oldIgnConfig.Storage.Files, newIgnConfig.Storage.Files) {
		return getFileChanges(stripPrefix, oldIgnConfig, newIgnConfig)
	}

	return []ConfigUpdateAction{}
}
