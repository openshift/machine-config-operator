package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func TestConfigDriftMonitor(t *testing.T) {
	// Our errors. Their contents don't matter because we're not basing our
	// assertions off of their contents. Instead, we're more concerned about
	// their types.
	fileErr := &configDriftErr{&fileConfigDriftErr{fmt.Errorf("file error")}}
	unitErr := &configDriftErr{&unitConfigDriftErr{fmt.Errorf("unit error")}}

	// Filesystem Mutators
	// These are closures to avoid namespace collisions and pollution since
	// they're not useful outside of this test.
	changeFileContent := func(path string) error {
		return os.WriteFile(path, []byte("notthecontents"), defaultFilePermissions)
	}

	touchFile := func(path string) error {
		now := time.Now()
		return os.Chtimes(path, now.Local(), now.Local())
	}

	renameFile := func(path string) error {
		return os.Rename(path, path+".old")
	}

	overwriteFile := func(path string) error {
		tmpPath := filepath.Join(filepath.Dir(path), "overwritten")
		if err := writeFileAtomicallyWithDefaults(tmpPath, []byte("notthecontents")); err != nil {
			return err
		}

		return os.Rename(tmpPath, path)
	}

	chmodFile := func(path string) error {
		return os.Chmod(path, 0755)
	}

	// The general idea for this test is as follows:
	// 1. We create a temporary directory.
	// 2. For each test case, we create an Ignition config as usual.
	// 3. Apply the temp dir to paths in the testcase Ignition config.
	// 4. Write the files to the temporary directory.
	// 5. Start the Config Drift Monitor and supply a mock ondrift function.
	// 6. Mutate the files on disk with our mutate func.
	// 7. Wait a few milliseconds for the watcher to catch up.
	// 8. Assert that we have config drift.
	// 9. Shut down the monitor and move on.
	//
	// Note: We try to run the testcases in parallel for speed with each one
	// mutating its own temp directory.
	testCases := []configDriftMonitorTestCase{
		// Ignition File
		// These target the file called /etc/a-config-file defined by the test
		// fixture.
		{
			name:        "ign file content drift",
			expectedErr: fileErr,
			mutateFile:  changeFileContent,
		},
		{
			name:       "ign file touch",
			mutateFile: touchFile,
		},
		{
			name:        "ign file rename",
			expectedErr: fileErr,
			mutateFile:  renameFile,
		},
		{
			name:        "ign file delete",
			expectedErr: fileErr,
			mutateFile:  os.Remove,
		},
		{
			name:        "ign file overwrite",
			expectedErr: fileErr,
			mutateFile:  overwriteFile,
		},
		{
			name:        "ign file chmod",
			expectedErr: fileErr,
			mutateFile:  chmodFile,
		},
		// Compressed Ignition File
		// These target the file called /etc/a-compressed-file defined by the test
		// fixture.
		// Targets a regression identified by:
		// https://bugzilla.redhat.com/show_bug.cgi?id=2032565
		{
			name:                 "ign compressed file content drift",
			expectedErr:          fileErr,
			mutateCompressedFile: changeFileContent,
		},
		{
			name:                 "ign compressed file touch",
			mutateCompressedFile: touchFile,
		},
		{
			name:                 "ign compressed file rename",
			expectedErr:          fileErr,
			mutateCompressedFile: renameFile,
		},
		{
			name:                 "ign compressed file delete",
			expectedErr:          fileErr,
			mutateCompressedFile: os.Remove,
		},
		{
			name:                 "ign compressed file overwrite",
			expectedErr:          fileErr,
			mutateCompressedFile: overwriteFile,
		},
		{
			name:                 "ign compressed file chmod",
			expectedErr:          fileErr,
			mutateCompressedFile: chmodFile,
		},
		// Systemd Unit tests
		// These target the systemd unit files in the test fixture:
		// /etc/systemd/system/unittest.service
		{
			name:        "ign unit content drift",
			expectedErr: unitErr,
			mutateUnit:  changeFileContent,
		},
		{
			name:       "ign unit touch",
			mutateUnit: touchFile,
		},
		{
			name:        "ign unit rename",
			expectedErr: unitErr,
			mutateUnit:  renameFile,
		},
		{
			name:        "ign unit delete",
			expectedErr: unitErr,
			mutateUnit:  os.Remove,
		},
		{
			name:        "ign unit overwrite",
			expectedErr: unitErr,
			mutateUnit:  overwriteFile,
		},
		{
			name:        "ign unit chmod",
			expectedErr: unitErr,
			mutateUnit:  chmodFile,
		},
		// Systemd Dropin tests
		// These target the dropin files belonging to the unittest systemd unit in
		// the test fixture: /etc/systemd/system/unittest.service.d/10-unittest-service.conf
		{
			name:         "ign dropin content drift",
			expectedErr:  unitErr,
			mutateDropin: changeFileContent,
		},
		{
			name:         "ign dropin touch",
			mutateDropin: touchFile,
		},
		{
			name:         "ign dropin rename",
			expectedErr:  unitErr,
			mutateDropin: renameFile,
		},
		{
			name:         "ign dropin delete",
			expectedErr:  unitErr,
			mutateDropin: os.Remove,
		},
		{
			name:         "ign dropin overwrite",
			expectedErr:  unitErr,
			mutateDropin: overwriteFile,
		},
		{
			name:         "ign dropin chmod",
			expectedErr:  unitErr,
			mutateDropin: chmodFile,
		},
	}

	// Create a mutex for our test cases The mutex is needed because we now
	// overwrite the origParentDirPath and noOrigParentDirPath global variables
	// so that our filesystem mutations are confined to a tempdir created by the
	// test case. However, since this is a global value, we need to be sure that
	// only one testcase can use it at a time. Other than that, the test suite
	// does a good job of keeping each individual test case isolated in its own
	// tempdir.
	testMutex := &sync.Mutex{}

	for _, testCase := range testCases {
		// Wire up the mutex to each test case before executing so they don't stomp
		// on each other.
		testCase.testMutex = testMutex
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()

			testCase.tmpDir = tmpDir
			testCase.systemdPath = filepath.Join(tmpDir, pathSystemd)

			testCase.run(t)
		})
	}
}

// Holds a testcase and its associated helper funcs
type configDriftMonitorTestCase struct {
	// Name of the test case
	name string
	// The expected error, if any
	expectedErr error
	// The tmpdir for the test case (assigned at runtime)
	tmpDir string
	// The systemdroot for the test case (assigned at runtime)
	systemdPath string
	// Only one of these may be used per testcase:
	// The mutation to apply to the Ignition file
	mutateFile func(string) error
	// The mutation to apply to the compressed Ignition file
	mutateCompressedFile func(string) error
	// The mutation to apply to the systemd unit file
	mutateUnit func(string) error
	// The mutation to apply to the systemd dropin file
	mutateDropin func(string) error
	// Mutex to ensure that parallel tests do not stomp on one another
	testMutex *sync.Mutex
}

// Runs the test case
func (tc configDriftMonitorTestCase) run(t *testing.T) {
	// Get our test fixtures
	ignConfig, mc := tc.getFixtures(t)

	// Create our unexpected error channel
	errChan := make(chan error, 5)

	// Create a new instance of the Config Drift Monitor
	cdm := NewConfigDriftMonitor()

	var wg sync.WaitGroup
	wg.Add(2)

	// Create a goroutine to listen on the Done() method.
	doneCalled := false
	go func() {
		defer wg.Done()
		select {
		case <-cdm.Done():
			doneCalled = true
			return
		}
	}()

	// Create a goroutine to listen on the supplied error channel.
	go func() {
		defer wg.Done()
		for {
			select {
			case err, ok := <-errChan:
				// Closing the channel causes a nil value to be sent. So we
				// disambiguate a nil error object being sent from the channel closing.
				if err != nil {
					t.Errorf("unexpected error via error channel: %s", err)
				}

				// Only break out of the loop if the error channel is closed.
				if !ok {
					return
				}
			}
		}
	}()

	onDriftCalled := false

	// To listen on when
	onDriftChan := make(chan struct{})

	// Configure the config drift monitor
	opts := ConfigDriftMonitorOpts{
		ErrChan:       errChan,
		SystemdPath:   tc.systemdPath,
		MachineConfig: mc,
		OnDrift: func(err error) {
			go func() {
				onDriftChan <- struct{}{}
			}()
			onDriftCalled = true
			tc.onDriftFunc(t, err)
		},
	}

	// Start the config drift monitor
	require.Nil(t, cdm.Start(opts))

	// Ensure that it's running
	assert.True(t, cdm.IsRunning())

	// Mutate the filesystem
	require.Nil(t, tc.mutate(ignConfig))

	// TODO: Figure out a value to make this work on Macs because they take
	// longer to report the filesystem activity.
	timeout := 100 * time.Millisecond
	start := time.Now()

	// Give the watcher time to fire or time out if it doesn't
	select {
	case <-onDriftChan:
		t.Logf("Took %v to fire", time.Since(start))
	case <-time.After(timeout):
		if tc.expectedErr != nil {
			t.Errorf("expected onDrift to be called, but timed out after: %v", timeout)
		}
	}

	// Stop the config drift monitor
	cdm.Stop()

	// Ensure that it's no longer running
	assert.False(t, cdm.IsRunning())

	// Closing our errChan causes the monitoring goroutine to stop.
	close(errChan)

	// Wait for our listener goroutines to shut down
	wg.Wait()

	// Ensure that our done channel was called
	assert.True(t, doneCalled, "expected to receive a done event")

	// If we expect an error, make sure onDrift was called. Otherwise, make sure
	// it wasn't called.

	if tc.expectedErr == nil {
		assert.False(t, onDriftCalled, "expected onDrift not to be called")
	} else {
		assert.True(t, onDriftCalled, "expected onDrift to be called")
	}
}

// Permissions in CI are a bit more complicated than they are on an end-user
// machine since we're running in a container with an unknown username and
// unknown UID / GID. However, the defaults (-1 / -1) seem to work without
// issue as evidenced by writeFileAtomicallyWithDefaults() being able to write
// successfully.
func setDefaultUIDandGID(file ign3types.File) ign3types.File {
	file.Node.User.Name = nil
	file.Node.Group.Name = nil
	file.User.ID = helpers.IntToPtr(-1)
	file.Group.ID = helpers.IntToPtr(-1)
	return file
}

// Creates the Ignition Config test fixture
func (tc configDriftMonitorTestCase) getIgnConfig(t *testing.T) ign3types.Config {
	compressedFile, err := helpers.CreateGzippedIgn3File("/etc/a-compressed-file", "thefilecontents", int(defaultFilePermissions))
	require.Nil(t, err)

	return ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Storage: ign3types.Storage{
			Files: []ign3types.File{
				setDefaultUIDandGID(helpers.CreateEncodedIgn3File("/etc/a-config-file", "thefilecontents", int(defaultFilePermissions))),
				setDefaultUIDandGID(compressedFile),
			},
		},
		Systemd: ign3types.Systemd{
			Units: []ign3types.Unit{
				{
					Name:     "unittest.service",
					Contents: helpers.StrToPtr("unittest-unit-contents"),
					Dropins: []ign3types.Dropin{
						{
							Contents: helpers.StrToPtr("unittest-service-contents"),
							Name:     "10-unittest-service.conf",
						},
						{
							Name: "20-unittest-service.conf",
						},
					},
				},
				// Add a masked systemd unit to ensure that we don't inadvertantly drift.
				// See: https://issues.redhat.com/browse/OCPBUGS-3909
				{
					Name:     "mask-and-contents.service",
					Contents: helpers.StrToPtr("[Unit]\nDescription=Just random content"),
					Mask:     helpers.BoolToPtr(true),
				},
			},
		},
	}
}

// Determines which file to mutate and runs the appropriate mutator.
func (tc configDriftMonitorTestCase) mutate(ignConfig ign3types.Config) error {
	if tc.mutateFile != nil {
		return tc.mutateFile(ignConfig.Storage.Files[0].Path)
	}

	if tc.mutateCompressedFile != nil {
		return tc.mutateCompressedFile(ignConfig.Storage.Files[1].Path)
	}

	if tc.mutateDropin != nil {
		dropinPath := getIgn3SystemdDropinPath(tc.systemdPath, ignConfig.Systemd.Units[0], ignConfig.Systemd.Units[0].Dropins[0])
		return tc.mutateDropin(dropinPath)
	}

	if tc.mutateUnit != nil {
		unitPath := getIgn3SystemdUnitPath(tc.systemdPath, ignConfig.Systemd.Units[0])
		return tc.mutateUnit(unitPath)
	}

	return fmt.Errorf("no file mutator provided")
}

// Creates and modifies the Ignition Config and converts it into a
// MachineConfig suitable for this test.
func (tc configDriftMonitorTestCase) getFixtures(t *testing.T) (ign3types.Config, *mcfgv1.MachineConfig) {
	t.Helper()

	ignConfig := tc.getIgnConfig(t)

	// Prefix all the ignition files with the temp directory.
	for i, file := range ignConfig.Storage.Files {
		file.Path = filepath.Join(tc.tmpDir, file.Path)
		ignConfig.Storage.Files[i] = file
	}

	// Separate the disk write process so that we can be sure that the deferred
	// functions are run even when we encounter an error.
	require.NoError(t, tc.writeIgnitionConfig(t, ignConfig))

	// Create a MachineConfig from our Ignition Config
	mc := helpers.CreateMachineConfigFromIgnition(ignConfig)
	mc.Name = "config-drift-monitor" + string(uuid.NewUUID())

	return ignConfig, mc
}

// This needs to be a pointer receiver so we can lock / unlock the mutex.
func (tc *configDriftMonitorTestCase) writeIgnitionConfig(t *testing.T, ignConfig ign3types.Config) error {
	t.Helper()

	// This is the only place where this mutex is used throughout this test
	// suite. We need a mutex because the origParentDirPath and
	// noOrigParentDirPath variables are global and our individual test cases
	// execute in parallel.
	tc.testMutex.Lock()
	defer tc.testMutex.Unlock()

	// For the purposes of our test, we want all of our filesystem mutations to
	// be contained within our test temp dir. With this in mind, we temporarily
	// override these globals with our temp dir.
	globals := map[string]*string{
		"usrPath":             &usrPath,
		"origParentDirPath":   &origParentDirPath,
		"noOrigParentDirPath": &noOrigParentDirPath,
	}

	for name := range globals {
		cleanup := helpers.OverrideGlobalPathVar(t, name, globals[name])
		defer cleanup()
	}

	// Write files the same way the MCD does.
	// NOTE: We manually handle the errors here because using require.Nil or
	// require.NoError will skip the deferred functions, which is undesirable.
	if err := writeFiles(ignConfig.Storage.Files, true); err != nil {
		return fmt.Errorf("could not write ignition config files: %w", err)
	}

	// Write systemd units the same way the MCD does.
	if err := writeUnits(ignConfig.Systemd.Units, tc.systemdPath, true); err != nil {
		return fmt.Errorf("could not write systemd units: %w", err)
	}

	return nil
}

func (tc configDriftMonitorTestCase) onDriftFunc(t *testing.T, err error) {
	// If we're not expecting a configDriftErr, we should not end up here.
	if tc.expectedErr == nil {
		t.Errorf("expected no config drift error, but got one anyway: %s", err)
	}

	// Make sure that we get specific error types based upon the expected
	// values
	var cdErr *configDriftErr
	assert.ErrorAs(t, err, &cdErr)

	// If the testcase asks for a fileConfigDriftErr, be sure we got one.
	var fErr *fileConfigDriftErr
	if errors.As(tc.expectedErr, &fErr) {
		assert.ErrorAs(t, err, &fErr)
	}

	// If the testcase asks for a unitConfigDriftErr, be sure we got one.
	var uErr *unitConfigDriftErr
	if errors.As(tc.expectedErr, &uErr) {
		assert.ErrorAs(t, err, &uErr)
	}
}
