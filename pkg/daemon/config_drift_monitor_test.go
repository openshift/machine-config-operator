package daemon

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func TestConfigDriftMonitor(t *testing.T) {
	// Our errors. Their contents don't matter because we're not basing our
	// assertions off of their contents. Instead, we're more concerned about
	// their types.
	fileErr := configDriftErr(fileConfigDriftErr(fmt.Errorf("file error")))
	unitErr := configDriftErr(unitConfigDriftErr(fmt.Errorf("unit error")))

	// Filesystem Mutators
	// These are closures to avoid namespace collisions and pollution since
	// they're not useful outside of this test.
	changeFileContent := func(path string) error {
		return ioutil.WriteFile(path, []byte("notthecontents"), defaultFilePermissions)
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

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			defer os.RemoveAll(tmpDir)

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
				helpers.CreateEncodedIgn3File("/etc/a-config-file", "thefilecontents", int(defaultFilePermissions)),
				compressedFile,
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

	// Prefix all the ignition files with the temp directory and write to disk.
	for i, file := range ignConfig.Storage.Files {
		file.Path = filepath.Join(tc.tmpDir, file.Path)
		ignConfig.Storage.Files[i] = file
		tc.writeIgn3FileForTest(t, file)
	}

	// Prefix all the systemd files with the temp dir and write them.
	for _, unit := range ignConfig.Systemd.Units {
		unitPath := getIgn3SystemdUnitPath(tc.systemdPath, unit)
		tc.writeFileForTest(t, unitPath, unit.Contents)
		for _, dropin := range unit.Dropins {
			dropinPath := getIgn3SystemdDropinPath(tc.systemdPath, unit, dropin)
			tc.writeFileForTest(t, dropinPath, dropin.Contents)
		}
	}

	// Create a MachineConfig from our Ignition Config
	mc := helpers.CreateMachineConfigFromIgnition(ignConfig)
	mc.Name = "config-drift-monitor" + string(uuid.NewUUID())

	return ignConfig, mc
}

func (tc configDriftMonitorTestCase) onDriftFunc(t *testing.T, err error) {
	// If we're not expecting a configDriftErr, we should not end up here.
	if tc.expectedErr == nil {
		t.Errorf("expected no config drift error, but got one anyway: %s", err)
	}

	// Make sure that we get specific error types based upon the expected
	// values
	var cdErr configDriftErr
	assert.ErrorAs(t, err, &cdErr)

	// If the testcase asks for a fileConfigDriftErr, be sure we got one.
	var fErr fileConfigDriftErr
	if errors.As(tc.expectedErr, &fErr) {
		assert.ErrorAs(t, err, &fErr)
	}

	// If the testcase asks for a unitConfigDriftErr, be sure we got one.
	var uErr unitConfigDriftErr
	if errors.As(tc.expectedErr, &uErr) {
		assert.ErrorAs(t, err, &uErr)
	}
}

func (tc configDriftMonitorTestCase) writeIgn3FileForTest(t *testing.T, file ign3types.File) {
	t.Helper()

	decodedContents, err := decodeContents(file.Contents.Source, file.Contents.Compression)
	require.Nil(t, err)

	require.Nil(t, writeFileAtomicallyWithDefaults(file.Path, decodedContents))
}

func (tc configDriftMonitorTestCase) writeFileForTest(t *testing.T, path string, contents *string) {
	t.Helper()

	out := ""
	if contents != nil {
		out = *contents
	}

	require.Nil(t, writeFileAtomicallyWithDefaults(path, []byte(out)))
}
