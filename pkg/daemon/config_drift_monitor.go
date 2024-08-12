package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/coreos/vcontext/path"
	"github.com/fsnotify/fsnotify"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

// Outermost error type for config drift errors
type configDriftErr struct {
	error
}

// Error type for file config drifts
type fileConfigDriftErr struct {
	error
}

// Error type for systemd unit config drifts
type unitConfigDriftErr struct {
	error
}

type sshConfigDriftErr struct {
	error
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
	// System's root path
	RootPath string
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
	filePaths map[string]path.ContextPath
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

	if opts.RootPath == "" || opts.RootPath[0] != '/' {
		return nil, fmt.Errorf("root path is empty or does not begin with /")
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

	klog.V(4).Infof("Initializing Config Drift Monitor")
	klog.V(4).Infof("Current MachineConfig: %s", c.MachineConfig.Name)

	// Even though we wire up fsnotify to use the parent directories of each
	// file, we keep track of the file paths individually so that we can ignore
	// files in the same directory which are not referenced by the MachineConfig.
	// This is useful when, for example, someone places a file in /etc using a
	// MachineConfig (e.g., /etc/foo), but an unknown (to the MachineConfig) file
	// in /etc (e.g., /etc/bar) is written to.
	c.filePaths, err = getFilePathsFromMachineConfig(c.MachineConfig, c.RootPath)
	if err != nil {
		return fmt.Errorf("could not get file paths from machine config: %w", err)
	}

	// fsnotify (presently) uses inotify instead of fanotify on Linux.
	// See: https://github.com/fsnotify/fsnotify/issues/114
	//
	// We must be judicious about the number of files we have wired up. However,
	// inotify cannot recurse into directories. To work around these limitaions,
	// we get the dirnames for all of the files in the Ignition config and dedupe
	// them and attach watchers to those dirs.
	dirPaths := getDirPathsFromFilePaths(c.filePaths)

	klog.V(4).Infof("Will watch %d directories containing %d files:", len(dirPaths), len(c.filePaths))

	// Wire up fsnotify to watch our config dirs
	for _, path := range dirPaths {
		klog.V(4).Infof("Watching dir: \"%s\"", path)
		if err := c.watcher.Add(path); err != nil {
			return fmt.Errorf("could not add fsnotify watcher to dir \"%s\": %w", path, err)
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

	klog.Info("Config Drift Monitor started")
}

// Stops the watcher.
// Note: Once a Config Drift Watcher has been stopped, it cannot be started
// again. A new instance must be created.
func (c *configDriftWatcher) stop() {
	c.stopCh <- struct{}{}
	c.wg.Wait()
	klog.Info("Config Drift Monitor has shut down")
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

// Validates on disk state for potential config drift.
func (c *configDriftWatcher) checkMachineConfigForEvent(event fsnotify.Event) error {
	// Ignore events for files not found in the MachineConfig.
	mcPath, ok := c.filePaths[event.Name]
	if !ok {
		return nil
	}

	if err := validateOnDiskStateForEvent(c.MachineConfig, c.RootPath, mcPath); err != nil {
		return &configDriftErr{err}
	}

	return nil
}

// Finds the paths for all files in a given MachineConfig.
func getFilePathsFromMachineConfig(mc *mcfgv1.MachineConfig, rootPath string) (map[string]path.ContextPath, error) {
	ignConfig, err := ctrlcommon.IgnParseWrapper(mc.Spec.Config.Raw)
	if err != nil {
		return map[string]path.ContextPath{}, fmt.Errorf("could not get dirs from ignition config: %w", err)
	}

	switch typedConfig := ignConfig.(type) {
	case ign3types.Config:
		return getFilePathsFromIgn3Config(ignConfig.(ign3types.Config), rootPath), nil
	case ign2types.Config:
		return getFilePathsFromIgn2Config(ignConfig.(ign2types.Config), rootPath), nil
	default:
		return map[string]path.ContextPath{}, fmt.Errorf("unexpected type for ignition config: %v", typedConfig)
	}
}

// Extracts all unique directories from a given Ignition 3 config.
//
//nolint:dupl
func getFilePathsFromIgn3Config(ignConfig ign3types.Config, rootPath string) map[string]path.ContextPath {
	files := map[string]path.ContextPath{}
	files[filepath.Join(rootPath, constants.RHCOS9SSHKeyPath)] = path.New("passwd", "Passwd", "Users", 0, "SSHAuthorizedKeys")

	// Get all the file paths from the ignition config
	for i, ignFile := range ignConfig.Storage.Files {
		if _, err := os.Stat(ignFile.Path); err == nil && !os.IsNotExist(err) {
			files[ignFile.Path] = path.New("files", "Storage", "Files", i)
		}
	}

	// Get all the file paths for systemd dropins from the ignition config
	for i, unit := range ignConfig.Systemd.Units {
		unitPath := getIgn3SystemdUnitPath(rootPath, unit)
		if _, err := os.Stat(unitPath); err == nil && !os.IsNotExist(err) {
			files[unitPath] = path.New("units", "Systemd", "Units", i)
		}

		for j, dropin := range unit.Dropins {
			dropinPath := getIgn3SystemdDropinPath(rootPath, unit, dropin)
			if _, err := os.Stat(dropinPath); err == nil && !os.IsNotExist(err) {
				files[dropinPath] = path.New("dropins", "Systemd", "Units", i, "Dropins", j)
			}
		}
	}

	return files
}

// Extracts all unique directories from a given Ignition 2 config.
//
//nolint:dupl
func getFilePathsFromIgn2Config(ignConfig ign2types.Config, rootPath string) map[string]path.ContextPath {
	files := map[string]path.ContextPath{}
	files[filepath.Join(rootPath, constants.RHCOS9SSHKeyPath)] = path.New("passwd", "Passwd", "Users", 0, "SSHAuthorizedKeys")

	// Get all the file paths from the ignition config
	for i, ignFile := range ignConfig.Storage.Files {
		if _, err := os.Stat(ignFile.Path); err == nil && !os.IsNotExist(err) {
			files[ignFile.Path] = path.New("files", "Storage", "Files", i)
		}
	}

	// Get all the file paths for systemd dropins from the ignition config
	for i, unit := range ignConfig.Systemd.Units {
		systemdPath := getSystemdPath(rootPath)
		unitPath := getIgn2SystemdUnitPath(systemdPath, unit)
		c := path.New("units", "Systemd", "Units", i)
		if _, err := os.Stat(unitPath); err == nil && !os.IsNotExist(err) {
			files[unitPath] = c
		}

		for j, dropin := range unit.Dropins {
			dropinPath := getIgn2SystemdDropinPath(systemdPath, unit, dropin)
			if _, err := os.Stat(dropinPath); err == nil && !os.IsNotExist(err) {
				files[dropinPath] = c.Append(j)
			}
		}
	}

	return files
}

// Gets the directories for all the MachineConfig file paths while
// deduplicating them.
func getDirPathsFromFilePaths(filePaths map[string]path.ContextPath) []string {
	dirPaths := sets.NewString()

	for filePath := range filePaths {
		dirPaths.Insert(filepath.Dir(filePath))
	}

	return dirPaths.List()
}
