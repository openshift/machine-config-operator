package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	"github.com/fsnotify/fsnotify"
	"github.com/golang/glog"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Outermost error type for config drift errors
type configDriftErr struct {
	error
}

func (c *configDriftErr) Unwrap() error {
	return c.error
}

// Error type for file config drifts
type fileConfigDriftErr struct {
	error
}

func (f *fileConfigDriftErr) Unwrap() error {
	return f.error
}

// Error type for systemd unit config drifts
type unitConfigDriftErr struct {
	error
}

func (u *unitConfigDriftErr) Unwrap() error {
	return u.error
}

// Error type for SSH key config drifts
type sshConfigDriftErr struct {
	error
}

func (s *sshConfigDriftErr) Unwrap() error {
	return s.error
}

// Error type for unexpected SSH key config drift
type unexpectedSSHFileErr struct {
	filename string
	error
}

func (u *unexpectedSSHFileErr) Unwrap() error {
	return u.error
}

type ConfigDriftMonitor interface {
	Start(ConfigDriftMonitorOpts) error
	Done() <-chan struct{}
	IsRunning() bool
	Stop()
}

type ConfigDriftMonitorOpts struct {
	// Called whenever a config drift is detected.
	OnDrift func(error)
	// The currently applied MachineConfig.
	MachineConfig *mcfgv1.MachineConfig
	Paths
	// Channel to report unknown errors
	ErrChan chan<- error
}

// Holds the Config Drift Watcher and ensures we only have a single instance
// running at a given time.
type configDriftMonitor struct {
	cdw    *configDriftWatcher
	mu     sync.Mutex
	stopCh chan struct{}
}

// Implements the Config Drift Watcher
type configDriftWatcher struct {
	ConfigDriftMonitorOpts
	watcher   *fsnotify.Watcher
	filePaths sets.String
	wg        sync.WaitGroup
	stopCh    chan struct{}
}

// Holds a single Config Drift Watcher and starts / stops it as necessary while
// ensuring that only a single Config Drift Monitor is running at any given
// time (within its scope).
func NewConfigDriftMonitor() ConfigDriftMonitor {
	return &configDriftMonitor{
		mu:     sync.Mutex{},
		stopCh: make(chan struct{}),
	}
}

// Determines if a Config Drift Watcher is running; useful for avoiding certain
// logic such as emitting a startup event.
func (c *configDriftMonitor) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cdw != nil
}

// Starts the Config Drift Monitor if not already started. It will create a new
// Config Drift Monitor instance, if necessary.
func (c *configDriftMonitor) Start(opts ConfigDriftMonitorOpts) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cdw != nil {
		return nil
	}

	var err error

	// We don't have a Config Drift Watcher running, create one.
	c.cdw, err = newConfigDriftWatcher(opts)
	if err != nil {
		return err
	}

	c.cdw.start()

	return nil
}

// Provides a channel for listening if the currently running Config Drift
// Monitor has been stopped. Can be used similar to <-ctx.Done().
func (c *configDriftMonitor) Done() <-chan struct{} {
	out := make(chan struct{})

	go func() {
		select {
		case <-c.stopCh:
			out <- struct{}{}
			close(out)
			return
		}
	}()

	return out
}

// Stops the currently running Config Drift Monitor and deletes its instance so
// that it cannot be reused. Also sends a signal to the Done() listener.
func (c *configDriftMonitor) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cdw != nil {
		c.cdw.stop()
		c.cdw = nil
		c.stopCh <- struct{}{}
	}
}

// Creates a new config drift watcher for a given machineconfig and registers
// a callback for when a drift occurs.
func newConfigDriftWatcher(opts ConfigDriftMonitorOpts) (*configDriftWatcher, error) {
	if opts.OnDrift == nil {
		return nil, fmt.Errorf("no ondrift function attached")
	}

	if opts.ErrChan == nil {
		return nil, fmt.Errorf("no error channel provided")
	}

	if opts.MachineConfig == nil {
		return nil, fmt.Errorf("no machine config provided")
	}

	c := &configDriftWatcher{
		ConfigDriftMonitorOpts: opts,
		stopCh:                 make(chan struct{}),
	}

	if err := c.initialize(); err != nil {
		return nil, fmt.Errorf("could not initialize config drift monitor: %w", err)
	}

	return c, nil
}

// Performs the initial setup and initialization of the Config Drift Monitor,
// such as identifying files from the MachineConfig, wiring up the watchers,
// etc.
func (c *configDriftWatcher) initialize() error {
	var err error

	// Initialize the fsnotify watcher
	c.watcher, err = fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("could not start watcher: %w", err)
	}

	glog.V(4).Infof("Initializing Config Drift Monitor")
	glog.V(4).Infof("Current MachineConfig: %s", c.MachineConfig.Name)

	// Even though we wire up fsnotify to use the parent directories of each
	// file, we keep track of the file paths individually so that we can ignore
	// files in the same directory which are not referenced by the MachineConfig.
	// This is useful when, for example, someone places a file in /etc using a
	// MachineConfig (e.g., /etc/foo), but an unknown (to the MachineConfig) file
	// in /etc (e.g., /etc/bar) is written to.
	c.filePaths, err = getFilePathsFromMachineConfig(c.MachineConfig, c.Paths)
	if err != nil {
		return fmt.Errorf("could not get file paths from machine config: %w", err)
	}

	// Add all of the SSH key path fragments to our watch list.
	for _, path := range c.Paths.ExpectedSSHPathFragments() {
		c.filePaths.Insert(path)
	}

	// fsnotify (presently) uses inotify instead of fanotify on Linux.
	// See: https://github.com/fsnotify/fsnotify/issues/114
	//
	// We must be judicious about the number of files we have wired up. However,
	// inotify cannot recurse into directories. To work around these limitaions,
	// we get the dirnames for all of the files in the Ignition config and dedupe
	// them and attach watchers to those dirs.
	dirPaths := getDirPathsFromFilePaths(c.filePaths)

	glog.V(4).Infof("Will watch %d directories containing %d files:", len(dirPaths), len(c.filePaths))

	// Wire up fsnotify to watch our config dirs
	for _, path := range dirPaths {
		glog.V(4).Infof("Watching dir: \"%s\"", path)
		if err := c.watcher.Add(path); err != nil {
			return fmt.Errorf("could not add fsnotify watcher to dir %q: %w", path, err)
		}
	}

	return nil
}

// Starts the Config Drift Watcher
func (c *configDriftWatcher) start() {
	c.wg = sync.WaitGroup{}
	c.wg.Add(1)

	go func() {
		defer c.wg.Done()
		for {
			select {
			case event := <-c.watcher.Events:
				// Our watcher is reporting an event that we should look at.
				if err := c.handleFileEvent(event); err != nil {
					// Send unknown file event errors to the error channel.
					c.ErrChan <- err
				}
			case err := <-c.watcher.Errors:
				// Send fsnotify errors directly to the error channel.
				c.ErrChan <- fmt.Errorf("fsnotify error: %w", err)
			case <-c.stopCh:
				// We received a stop signal, shutdown our watcher.
				c.watcher.Close()
				return
			}
		}
	}()

	glog.Info("Config Drift Monitor started")
}

// Stops the watcher.
// Note: Once a Config Drift Watcher has been stopped, it cannot be started
// again. A new instance must be created.
func (c *configDriftWatcher) stop() {
	c.stopCh <- struct{}{}
	c.wg.Wait()
	glog.Info("Config Drift Monitor has shut down")
}

// Handles the filesystem event for any of the files we're watching and
// filters any config drift errors to the provided callback.
func (c *configDriftWatcher) handleFileEvent(event fsnotify.Event) error {
	err := c.checkMachineConfigForEvent(event)

	if err == nil {
		return nil
	}

	var cdErr *configDriftErr
	if errors.As(err, &cdErr) {
		c.OnDrift(cdErr)
		// Don't bubble this error up further since it's handled by OnDrift.
		return nil
	}

	return fmt.Errorf("unknown config drift error: %w", err)
}

func (c *configDriftWatcher) shouldValidateOnDiskState(event fsnotify.Event) bool {
	// If the filename is not in the MachineConfig or it doesn't contain
	// /home/core/.ssh, we ignore it.
	//
	// We do a fuzzy match of /home/core/.ssh because we want to cover any events
	// in /home/core/.ssh and/or within /home/core/.ssh/authorized_keys.d. We
	// can't watch for those events directly using fsnotify because:
	//
	// 1. fsnotify cannot recurse into a subdirectories.
	// 2. fsnotify cannot alert on files that do not exist.
	return c.filePaths.Has(event.Name) || strings.Contains(event.Name, c.Paths.SSHKeyRoot())
}

// Validates on disk state for potential config drift.
func (c *configDriftWatcher) checkMachineConfigForEvent(event fsnotify.Event) error {
	// Ignore events for files not found in the MachineConfig.
	if !c.shouldValidateOnDiskState(event) {
		return nil
	}

	if err := validateOnDiskState(c.MachineConfig, c.Paths); err != nil {
		return &configDriftErr{err}
	}

	return nil
}

// Finds the paths for all files in a given MachineConfig.
func getFilePathsFromMachineConfig(mc *mcfgv1.MachineConfig, paths Paths) (sets.String, error) {
	ignConfig, err := ctrlcommon.IgnParseWrapper(mc.Spec.Config.Raw)
	if err != nil {
		return sets.String{}, fmt.Errorf("could not get dirs from ignition config: %w", err)
	}

	switch typedConfig := ignConfig.(type) {
	case ign3types.Config:
		return getFilePathsFromIgn3Config(ignConfig.(ign3types.Config), paths), nil
	case ign2types.Config:
		return getFilePathsFromIgn2Config(ignConfig.(ign2types.Config), paths), nil
	default:
		return sets.String{}, fmt.Errorf("unexpected type for ignition config: %v", typedConfig)
	}
}

// Extracts all unique directories from a given Ignition 3 config.
//
//nolint:dupl // This code must be duplicated because of different types.
func getFilePathsFromIgn3Config(ignConfig ign3types.Config, paths Paths) sets.String {
	files := sets.NewString()

	// Get all the file paths from the ignition config
	for _, ignFile := range ignConfig.Storage.Files {
		if _, err := os.Stat(ignFile.Path); err == nil && !os.IsNotExist(err) {
			files.Insert(ignFile.Path)
		}
	}

	// Get all the file paths for systemd dropins from the ignition config
	for _, unit := range ignConfig.Systemd.Units {
		unitPath := paths.SystemdUnitPath(unit.Name)
		if _, err := os.Stat(unitPath); err == nil && !os.IsNotExist(err) {
			files.Insert(unitPath)
		}

		for _, dropin := range unit.Dropins {
			dropinPath := paths.SystemdDropinPath(unit.Name, dropin.Name)
			if _, err := os.Stat(dropinPath); err == nil && !os.IsNotExist(err) {
				files.Insert(dropinPath)
			}
		}
	}

	return files
}

// Extracts all unique directories from a given Ignition 2 config.
//
//nolint:dupl // This code must be duplicated because of different types.
func getFilePathsFromIgn2Config(ignConfig ign2types.Config, paths Paths) sets.String {
	files := sets.NewString()

	// Get all the file paths from the ignition config
	for _, ignFile := range ignConfig.Storage.Files {
		if _, err := os.Stat(ignFile.Path); err == nil && !os.IsNotExist(err) {
			files.Insert(ignFile.Path)
		}
	}

	// Get all the file paths for systemd dropins from the ignition config
	for _, unit := range ignConfig.Systemd.Units {
		unitPath := paths.SystemdUnitPath(unit.Name)
		if _, err := os.Stat(unitPath); err == nil && !os.IsNotExist(err) {
			files.Insert(unitPath)
		}

		for _, dropin := range unit.Dropins {
			dropinPath := paths.SystemdDropinPath(unit.Name, dropin.Name)
			if _, err := os.Stat(dropinPath); err == nil && !os.IsNotExist(err) {
				files.Insert(dropinPath)
			}
		}
	}

	return files
}

// Gets the directories for all the MachineConfig file paths while
// deduplicating them.
func getDirPathsFromFilePaths(filePaths sets.String) []string {
	dirPaths := sets.NewString()

	for filePath := range filePaths {
		dirPaths.Insert(filepath.Dir(filePath))
	}

	return dirPaths.List()
}
