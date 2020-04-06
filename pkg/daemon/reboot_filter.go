package daemon

import (
	"fmt"
	"os/exec"
	"path/filepath"
	// "reflect"

	systemdDbus "github.com/coreos/go-systemd/dbus"
	igntypes "github.com/coreos/ignition/config/v2_2/types"
	"github.com/deckarep/golang-set"
	"github.com/golang/glog"
)

type FileFilterEntry struct {
	glob             string
	postUpdateAction PostUpdateAction
}

type UnitFilterEntry struct {
	name          string
	drainRequired bool
}

type AvoidRebootConfig struct {
	// Files filter which do not require reboot
	Files []*FileFilterEntry
	// List of systemd unit that do not require system reboot, but rather just unit restart
	Units []*UnitFilterEntry
}

// TODO: create a proper filter config as this one is just a testing one
var filterConfig = AvoidRebootConfig{
	Files: []*FileFilterEntry{
		&FileFilterEntry{
			glob: "/home/core/testfile",
			postUpdateAction: RunBinaryAction{
				binary: "/bin/bash",
				args: []string{
					"-c",
					"echo \"$(date)\" >> /home/core/testfile.out",
				},
				DrainRequired: DrainRequired{drainRequired: false},
			},
		},
		&FileFilterEntry{
			glob: "/home/core/drain_required",
			postUpdateAction: RunBinaryAction{
				binary: "/bin/bash",
				args: []string{
					"-c",
					"echo \"$(date)\" >> /home/core/drain_required.out",
				},
				DrainRequired: DrainRequired{drainRequired: true},
			},
		},
	},
	Units: []*UnitFilterEntry{
		&UnitFilterEntry{
			name:          "test-service-drain.service",
			drainRequired: true,
		},
		&UnitFilterEntry{
			name:          "test-service.service",
			drainRequired: false,
		},
	},
}

func (config AvoidRebootConfig) getFileAction(filePath string) PostUpdateAction {
	for _, entry := range config.Files {
		matched, err := filepath.Match(entry.glob, filePath)
		if err != nil {
			// TODO: log
			continue
		}
		if matched {
			return entry.postUpdateAction
		}
	}
	return nil
}

func (config AvoidRebootConfig) getUnitAction(unit igntypes.Unit, systemdConnection *systemdDbus.Conn) PostUpdateAction {
	for _, entry := range config.Units {
		if entry.name == unit.Name {
			// same logic like in writeUnits()
			enableUnit := false
			if unit.Enable {
				enableUnit = true
			} else if unit.Enabled != nil {
				enableUnit = *unit.Enabled
			}
			return SystemdAction{
				unit.Name,
				unitRestart,
				enableUnit,
				systemdConnection,
				DrainRequired{drainRequired: entry.drainRequired},
			}
		}
	}
	return nil
}

type PostUpdateAction interface {
	Run() error
	getIsDrainRequired() bool
}

type DrainRequired struct {
	drainRequired bool
}

func (idr DrainRequired) getIsDrainRequired() bool {
	return idr.drainRequired
}

type RunBinaryAction struct {
	binary string
	args   []string
	DrainRequired
}

func (action RunBinaryAction) Run() error {
	glog.Infof(
		"Running post update action: running command: %v %v", action.binary, action.args,
	)
	output, err := exec.Command(action.binary, action.args...).CombinedOutput()
	// TODO: Add some timeout?
	if err != nil {
		glog.Errorf("Running post update action (running command: '%s %s') failed: %s; command output: %s", action.binary, action.args, err, output)
		return err
	}
	return nil
}

type UnitOperation string

const (
	unitRestart UnitOperation = "restart"
	unitReload  UnitOperation = "reload"
)

type SystemdAction struct {
	unitName          string
	operation         UnitOperation
	enabled           bool
	systemdConnection *systemdDbus.Conn
	DrainRequired
}

func (action SystemdAction) Run() error {
	// TODO: add support for reload operation
	// For now only restart operation is supported
	if action.systemdConnection == nil {
		return fmt.Errorf(
			"Unable to run post update action for unit %q: systemd dbus connection not specified",
			action.unitName,
		)
	}
	var err error
	outputChannel := make(chan string)
	if action.enabled {
		glog.Infof("Restarting unit %q", action.unitName)
		_, err = action.systemdConnection.RestartUnit(action.unitName, "replace", outputChannel)
	} else {
		glog.Infof("Stopping unit %q", action.unitName)
		_, err = action.systemdConnection.StopUnit(action.unitName, "replace", outputChannel)
	}
	if err != nil {
		return fmt.Errorf("Running systemd action failed: %s", err)
	}
	output := <-outputChannel

	switch output {
	case "done":
		fallthrough
	case "skipped":
		glog.Infof("Systemd action successful: %s", output)
	default:
		return fmt.Errorf("Systemd action %s", output)
	}
	return nil
}

type ChangeType string

const (
	changeCreated ChangeType = "created"
	changeDeleted ChangeType = "deleted"
	changeUpdated ChangeType = "updated"
)

type FileChange struct {
	name       string
	file       igntypes.File
	changeType ChangeType
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

func getFilesChanges(oldFilesConfig, newFilesConfig []igntypes.File) []*FileChange {
	oldFiles := mapset.NewSetFromSlice(getFileNames(oldFilesConfig))
	oldFilesMap := filesToMap(oldFilesConfig)
	newFiles := mapset.NewSetFromSlice(getFileNames(newFilesConfig))
	newFilesMap := filesToMap(newFilesConfig)
	changes := make([]*FileChange, 0, newFiles.Cardinality())
	for created := range newFiles.Difference(oldFiles).Iter() {
		changes = append(changes, &FileChange{
			name:       created.(string),
			file:       newFilesMap[created.(string)],
			changeType: changeCreated,
		})
	}
	for deleted := range oldFiles.Difference(newFiles).Iter() {
		changes = append(changes, &FileChange{
			name:       deleted.(string),
			file:       oldFilesMap[deleted.(string)],
			changeType: changeDeleted,
		})
	}
	for changeCandidate := range newFiles.Intersect(oldFiles).Iter() {
		newFile := newFilesMap[changeCandidate.(string)]
		// if !reflect.DeepEqual(newFile, oldFilesMap[changeCandidate.(string)]) {
		if !checkFiles([]igntypes.File{newFile}) {
			changes = append(changes, &FileChange{
				name:       changeCandidate.(string),
				file:       newFile,
				changeType: changeUpdated,
			})
		}
	}
	return changes
}

type UnitChange struct {
	name       string
	oldUnit    igntypes.Unit
	newUnit    igntypes.Unit
	changeType ChangeType
}

func getUnitNames(units []igntypes.Unit) []interface{} {
	names := make([]interface{}, len(units))
	for i, unit := range units {
		names[i] = unit.Name
	}
	return names
}

func unitsToMap(units []igntypes.Unit) map[string]igntypes.Unit {
	unitMap := make(map[string]igntypes.Unit, len(units))
	for _, unit := range units {
		unitMap[unit.Name] = unit
	}
	return unitMap
}

func getUnitsChanges(oldUnitsConfig, newUnitsConfig []igntypes.Unit) []*UnitChange {
	oldUnits := mapset.NewSetFromSlice(getUnitNames(oldUnitsConfig))
	oldUnitsMap := unitsToMap(oldUnitsConfig)
	newUnits := mapset.NewSetFromSlice(getUnitNames(newUnitsConfig))
	newUnitsMap := unitsToMap(newUnitsConfig)
	changes := make([]*UnitChange, 0, newUnits.Cardinality())
	for created := range newUnits.Difference(oldUnits).Iter() {
		changes = append(changes, &UnitChange{
			name:       created.(string),
			newUnit:    newUnitsMap[created.(string)],
			changeType: changeCreated,
		})
	}
	for deleted := range oldUnits.Difference(newUnits).Iter() {
		changes = append(changes, &UnitChange{
			name:       deleted.(string),
			oldUnit:    oldUnitsMap[deleted.(string)],
			changeType: changeDeleted,
		})
	}
	for changeCandidate := range newUnits.Intersect(oldUnits).Iter() {
		changedUnitName := changeCandidate.(string)
		newUnit := newUnitsMap[changedUnitName]
		oldUnit := oldUnitsMap[changedUnitName]
		// if !reflect.DeepEqual(newUnit, oldUnit) {
		if !checkUnits([]igntypes.Unit{newUnit}) {
			changes = append(changes, &UnitChange{
				name:       changedUnitName,
				newUnit:    newUnit,
				oldUnit:    oldUnit,
				changeType: changeUpdated,
			})
		}
	}
	return changes
}

func getPostUpdateActions(filesChanges []*FileChange, unitsChanges []*UnitChange, systemdConnection *systemdDbus.Conn) ([]PostUpdateAction, error) {
	glog.Info("Trying to check whether changes in files and units require system reboot.")
	actions := make([]PostUpdateAction, 0, len(filesChanges)+len(unitsChanges))
	rebootRequiredMsg := ", reboot will be required"
	for _, change := range filesChanges {
		switch change.changeType {
		case changeCreated:
			fallthrough
		case changeUpdated:
			action := filterConfig.getFileAction(change.name)
			if action == nil {
				err := fmt.Errorf("No action found for file %q", change.name)
				glog.Infof("%s%s", err, rebootRequiredMsg)
				return nil, err
			}
			actions = append(actions, action)
			glog.Infof("Action found for file %q", change.name)
		default:
			err := fmt.Errorf("File %q was removed", change.name)
			glog.Infof("%s%s", err, rebootRequiredMsg)
			return nil, err
		}
	}

	for _, change := range unitsChanges {
		switch change.changeType {
		case changeCreated:
			fallthrough
		case changeUpdated:
			action := filterConfig.getUnitAction(change.newUnit, systemdConnection)
			if action == nil {
				err := fmt.Errorf("No action found for unit %q", change.name)
				glog.Infof("%s%s", err, rebootRequiredMsg)
				return nil, err
			}
			if systemdConnection == nil {
				err := fmt.Errorf(
					"Missing systemd connection for running post update action for unit %q",
					change.name,
				)
				glog.Errorf("%s%s", err, rebootRequiredMsg)
				return nil, err
			}
			actions = append(actions, action)
			glog.Infof("Action found for unit %q", change.name)
		default:
			err := fmt.Errorf("Unit %q was removed", change.name)
			glog.Infof("%s%s", err, rebootRequiredMsg)
			return nil, err
		}
	}
	return actions, nil
}

func isDrainRequired(actions []PostUpdateAction) bool {
	isRequired := false
	for _, action := range actions {
		isRequired = isRequired || action.getIsDrainRequired()
	}
	return isRequired
}

// returns true in case reboot is required (some actions failed), false
// otherwise
func runPostUpdateActions(actions []PostUpdateAction) bool {
	glog.Infof("Running %d post update action(s)...", len(actions))
	for _, action := range actions {
		if err := action.Run(); err != nil {
			glog.Errorf("Post update action failed: %s", err)
			return true
		}
	}
	glog.Info("Running post update Actions were sucessfull")
	return false
}
