package daemon

import (
	"path/filepath"
	"testing"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/stretchr/testify/assert"
)

func dirForTest(path string) string {
	return filepath.Join("/tmp", "dir", "for", "test", path)
}

func applyDirForTest(paths []string) []string {
	out := []string{}

	for _, path := range paths {
		out = append(out, dirForTest(path))
	}

	return out
}

func TestValidationPathsSSH(t *testing.T) {
	rhcos8PathFragments := []string{
		constants.RHCOS8SSHKeyPath,
		constants.CoreUserSSH,
	}

	rhcos9PathFragments := []string{
		constants.RHCOS9SSHKeyPath,
		"/home/core/.ssh/authorized_keys.d",
		constants.CoreUserSSH,
	}

	sshTestCases := []struct {
		name                   string
		vp                     ValidationPaths
		expectedSSHRoot        string
		expectedSSHFragments   []string
		unexpectedSSHFragments []string
	}{
		{
			name: "RHCOS 8",
			vp: ValidationPaths{
				expectedSSHKeyPath:   constants.RHCOS8SSHKeyPath,
				unexpectedSSHKeyPath: constants.RHCOS9SSHKeyPath,
			},
			expectedSSHRoot:        constants.CoreUserSSH,
			expectedSSHFragments:   rhcos8PathFragments,
			unexpectedSSHFragments: rhcos9PathFragments,
		},
		{
			name: "RHCOS 9",
			vp: ValidationPaths{
				expectedSSHKeyPath:   constants.RHCOS9SSHKeyPath,
				unexpectedSSHKeyPath: constants.RHCOS8SSHKeyPath,
			},
			expectedSSHRoot:        constants.CoreUserSSH,
			expectedSSHFragments:   rhcos9PathFragments,
			unexpectedSSHFragments: rhcos8PathFragments,
		},
		{
			name: "Test Temp Dir RHCOS 8",
			vp: ValidationPaths{
				expectedSSHKeyPath:   dirForTest(constants.RHCOS8SSHKeyPath),
				unexpectedSSHKeyPath: dirForTest(constants.RHCOS9SSHKeyPath),
			},
			expectedSSHRoot:        dirForTest(constants.CoreUserSSH),
			expectedSSHFragments:   applyDirForTest(rhcos8PathFragments),
			unexpectedSSHFragments: applyDirForTest(rhcos9PathFragments),
		},
		{
			name: "Test Temp Dir RHCOS 9",
			vp: ValidationPaths{
				expectedSSHKeyPath:   dirForTest(constants.RHCOS9SSHKeyPath),
				unexpectedSSHKeyPath: dirForTest(constants.RHCOS8SSHKeyPath),
			},
			expectedSSHRoot:        dirForTest(constants.CoreUserSSH),
			expectedSSHFragments:   applyDirForTest(rhcos9PathFragments),
			unexpectedSSHFragments: applyDirForTest(rhcos8PathFragments),
		},
	}

	for _, testCase := range sshTestCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expectedSSHRoot, testCase.vp.SSHKeyRoot())
			assert.Equal(t, testCase.expectedSSHFragments, testCase.vp.ExpectedSSHPathFragments())
			assert.Equal(t, testCase.unexpectedSSHFragments, testCase.vp.UnexpectedSSHPathFragments())
			assert.NoError(t, testCase.vp.validate())
		})
	}
}

func TestValidationPathsSystemd(t *testing.T) {
	systemdUnitName := "test-unit"
	systemdDropinName := "test-dropin"

	systemdTestCases := []struct {
		name                      string
		vp                        ValidationPaths
		expectedSystemdRootPath   string
		expectedSystemdUnitPath   string
		expectedSystemdDropinPath string
	}{
		{
			name:                      "Default path",
			vp:                        ValidationPaths{},
			expectedSystemdRootPath:   pathSystemd,
			expectedSystemdUnitPath:   filepath.Join(pathSystemd, systemdUnitName),
			expectedSystemdDropinPath: filepath.Join(pathSystemd, systemdUnitName+".d", systemdDropinName),
		},
		{
			name: "Custom path",
			vp: ValidationPaths{
				systemdPath: dirForTest(pathSystemd),
			},
			expectedSystemdRootPath:   dirForTest(pathSystemd),
			expectedSystemdUnitPath:   dirForTest(filepath.Join(pathSystemd, systemdUnitName)),
			expectedSystemdDropinPath: dirForTest(filepath.Join(pathSystemd, systemdUnitName+".d", systemdDropinName)),
		},
	}

	for _, testCase := range systemdTestCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.expectedSystemdRootPath, testCase.vp.SystemdPath())
			assert.Equal(t, testCase.expectedSystemdUnitPath, testCase.vp.SystemdUnitPath(systemdUnitName))
			assert.Equal(t, testCase.expectedSystemdDropinPath, testCase.vp.SystemdDropinPath(systemdUnitName, systemdDropinName))
			// We expect this to fail validation because the SSH key paths aren't set.
			assert.Error(t, testCase.vp.validate())
		})
	}
}
