package daemon

import (
	"fmt"
	"reflect"

	"github.com/coreos/go-systemd/dbus"
	igntypes "github.com/coreos/ignition/v2/config/v3_1/types"
	mapset "github.com/deckarep/golang-set"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

type actionResult interface {
	Describe(dn *Daemon) string
	Execute(dn *Daemon, newConfig *mcfgv1.MachineConfig) error
}

type RebootPostAction struct {
	actionResult

	Reason string
}

func (a RebootPostAction) Describe(dn *Daemon) string {
	return fmt.Sprintf("Rebooting node %v: %v", dn.node.GetName(), a.Reason)
}

func (a RebootPostAction) Execute(dn *Daemon, newConfig *mcfgv1.MachineConfig) error {
	return dn.drainAndReboot(newConfig)
}

type ServicePostAction struct {
	actionResult

	Reason string

	ServiceName   string
	ServiceAction string
}

func (a ServicePostAction) Describe(dn *Daemon) string {
	return fmt.Sprintf("Restarting service %v", a.Reason)
}

func (a ServicePostAction) Execute(dn *Daemon, newConfig *mcfgv1.MachineConfig) error {
	// TODO: add support for reload operation
	// For now only restart operation is supported
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
	case "stop":
		glog.Infof("Stopping unit %q", a.ServiceName)
		_, err = systemdConnection.StopUnit(a.ServiceName, "replace", outputChannel)
	default:
		return fmt.Errorf("Unhandled systemd action %q for %q", a.ServiceAction, a.ServiceName)
	}

	if err != nil {
		return fmt.Errorf("Running systemd action failed: %s", err)
	}

	output := <-outputChannel
	switch output {
	case "done":
		glog.Infof("Systemd action %q for %q completed successful: %s", a.ServiceAction, a.ServiceName, output)
	case "skipped":
		// TODO: When are actions skipped... is it ever a failure?
		glog.Infof("Systemd action %q for %q was skipped: %s", a.ServiceAction, a.ServiceName, output)
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

func getFileChanges(oldIgnConfig, newIgnConfig igntypes.Config) []actionResult {
	actions := []actionResult{}

	oldFiles := mapset.NewSetFromSlice(getFileNames(oldIgnConfig.Storage.Files))
	newFiles := mapset.NewSetFromSlice(getFileNames(newIgnConfig.Storage.Files))

	for filename := range newFiles.Difference(oldFiles).Iter() {
		return []actionResult{RebootPostAction{Reason: fmt.Sprintf("File %q was added", filename.(string))}}
	}

	for filename := range oldFiles.Difference(newFiles).Iter() {
		return []actionResult{RebootPostAction{Reason: fmt.Sprintf("File %q was removed", filename.(string))}}
	}

//	oldFilesMap := filesToMap(oldIgnConfig.Storage.Files)
	newFilesMap := filesToMap(newIgnConfig.Storage.Files)
	for file := range newFiles.Intersect(oldFiles).Iter() {
		candidate := newFilesMap[file.(string)]
		if err := checkV3Files([]igntypes.File{candidate}); err != nil {
			if candidate.Node.Path != "/etc/containers/registry.conf" {
				return []actionResult{RebootPostAction{Reason: fmt.Sprintf("Storage file %q changed", candidate.Node.Path)}}
			}

			actions = append(actions, ServicePostAction{
				Reason:      fmt.Sprintf("change to %q", candidate.Node.Path),
				ServiceName: "crio.service", ServiceAction: "restart"})
		}
	}

	return actions
}

func calculateActions(oldConfig, newConfig *mcfgv1.MachineConfig, diff *machineConfigDiff) []actionResult {
	
	if diff.osUpdate || diff.kargs || diff.fips || diff.kernelType {
		return []actionResult{RebootPostAction{Reason: "OS/Kernel changed"}}
	}

	oldIgnConfig, err := ctrlcommon.ParseAndConvertConfig(oldConfig.Spec.Config.Raw)
	if err != nil {
		return []actionResult{RebootPostAction{
			Reason: fmt.Sprintf("parsing old Ignition config failed with error: %v", err)}}
	}
	newIgnConfig, err := ctrlcommon.ParseAndConvertConfig(newConfig.Spec.Config.Raw)
	if err != nil {
		return []actionResult{RebootPostAction{
			Reason: fmt.Sprintf("parsing new Ignition config failed with error: %v", err)}}
	}

	// Check for any changes not excluded by Reconcilable()
	// Alternatively, fold this code into that function
	if !reflect.DeepEqual(oldIgnConfig.Ignition, newIgnConfig.Ignition) {
		return []actionResult{RebootPostAction{Reason: "Ignition changed"}}
	}
	if !reflect.DeepEqual(oldIgnConfig.Passwd, newIgnConfig.Passwd) {
		return []actionResult{RebootPostAction{Reason: "Passwords changed"}}
	}
	if !reflect.DeepEqual(oldIgnConfig.Systemd, newIgnConfig.Systemd) {
		return []actionResult{RebootPostAction{Reason: "Systemd changed"}}
	}
	if !reflect.DeepEqual(oldIgnConfig.Storage.Files, newIgnConfig.Storage.Files) {
		return getFileChanges(oldIgnConfig, newIgnConfig)
	}

	return []actionResult{}
}
