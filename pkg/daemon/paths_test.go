package daemon

import (
	"path/filepath"
	"testing"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/stretchr/testify/assert"
)

func TestPaths(t *testing.T) {
	systemdUnitName := "test-unit"
	systemdDropinName := "test-dropin"

	rhcos8PathFragments := []string{
		constants.RHCOS8SSHKeyPath,
		constants.CoreUserSSH,
	}

	rhcos9PathFragments := []string{
		constants.RHCOS9SSHKeyPath,
		"/home/core/.ssh/authorized_keys.d",
		constants.CoreUserSSH,
	}

	testCases := []struct {
		name                        string
		os                          osRelease
		expectedSSHRoot             string
		expectedSSHFragments        []string
		unexpectedSSHFragments      []string
		expectedSystemdRootPath     string
		expectedSystemdUnitPath     string
		expectedSystemdDropinPath   string
		expectedOrigFileName        string
		expectedNoOrigFileStampName string
		expectedOrigParentDir       string
		expectedNoOrigParentDir     string
		expectedWithUsrPath         string
	}{
		{
			name:                        "RHCOS 8",
			os:                          rhcos8(),
			expectedSSHRoot:             constants.CoreUserSSH,
			expectedSSHFragments:        rhcos8PathFragments,
			unexpectedSSHFragments:      rhcos9PathFragments,
			expectedSystemdRootPath:     pathSystemd,
			expectedSystemdUnitPath:     filepath.Join(pathSystemd, systemdUnitName),
			expectedSystemdDropinPath:   filepath.Join(pathSystemd, systemdUnitName+".d", systemdDropinName),
			expectedOrigFileName:        "/etc/machine-config-daemon/orig/something.mcdorig",
			expectedNoOrigFileStampName: "/etc/machine-config-daemon/noorig/something.mcdnoorig",
			expectedOrigParentDir:       "/etc/machine-config-daemon/orig",
			expectedNoOrigParentDir:     "/etc/machine-config-daemon/noorig",
			expectedWithUsrPath:         "/usr/something",
		},
		{
			name:                        "RHCOS 9",
			os:                          rhcos9(),
			expectedSSHRoot:             constants.CoreUserSSH,
			expectedSSHFragments:        rhcos9PathFragments,
			unexpectedSSHFragments:      rhcos8PathFragments,
			expectedSystemdRootPath:     pathSystemd,
			expectedSystemdUnitPath:     filepath.Join(pathSystemd, systemdUnitName),
			expectedSystemdDropinPath:   filepath.Join(pathSystemd, systemdUnitName+".d", systemdDropinName),
			expectedOrigFileName:        "/etc/machine-config-daemon/orig/something.mcdorig",
			expectedNoOrigFileStampName: "/etc/machine-config-daemon/noorig/something.mcdnoorig",
			expectedOrigParentDir:       "/etc/machine-config-daemon/orig",
			expectedNoOrigParentDir:     "/etc/machine-config-daemon/noorig",
			expectedWithUsrPath:         "/usr/something",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name+" Defaults", func(t *testing.T) {
			paths, err := GetPaths(testCase.os)
			assert.NoError(t, err)
			assert.Equal(t, testCase.expectedSSHRoot, paths.SSHKeyRoot())
			assert.Equal(t, testCase.expectedSSHFragments, paths.ExpectedSSHPathFragments())
			assert.Equal(t, testCase.unexpectedSSHFragments, paths.UnexpectedSSHPathFragments())
			assert.Equal(t, testCase.expectedSystemdRootPath, paths.SystemdPath())
			assert.Equal(t, testCase.expectedSystemdUnitPath, paths.SystemdUnitPath(systemdUnitName))
			assert.Equal(t, testCase.expectedSystemdDropinPath, paths.SystemdDropinPath(systemdUnitName, systemdDropinName))
			assert.Equal(t, testCase.expectedOrigFileName, paths.OrigFileName("something"))
			assert.Equal(t, testCase.expectedNoOrigFileStampName, paths.NoOrigFileStampName("something"))
			assert.Equal(t, testCase.expectedOrigParentDir, paths.OrigParentDir())
			assert.Equal(t, testCase.expectedNoOrigParentDir, paths.NoOrigParentDir())
			assert.Equal(t, testCase.expectedWithUsrPath, paths.WithUsrPath("something"))
		})

		t.Run(testCase.name+" With Prefix", func(t *testing.T) {
			prefix := "/this/is/a/temp/dir"

			apply := func(in string) string {
				return filepath.Join(prefix, in)
			}

			applyToAll := func(in []string) []string {
				out := []string{}

				for _, item := range in {
					out = append(out, apply(item))
				}

				return out
			}

			paths, err := GetPathsWithPrefix(testCase.os, prefix)
			assert.NoError(t, err)
			assert.Equal(t, apply(testCase.expectedSSHRoot), paths.SSHKeyRoot())
			assert.Equal(t, applyToAll(testCase.expectedSSHFragments), paths.ExpectedSSHPathFragments())
			assert.Equal(t, applyToAll(testCase.unexpectedSSHFragments), paths.UnexpectedSSHPathFragments())
			assert.Equal(t, apply(testCase.expectedSystemdRootPath), paths.SystemdPath())
			assert.Equal(t, apply(testCase.expectedSystemdUnitPath), paths.SystemdUnitPath(systemdUnitName))
			assert.Equal(t, apply(testCase.expectedSystemdDropinPath), paths.SystemdDropinPath(systemdUnitName, systemdDropinName))
			assert.Equal(t, apply(testCase.expectedOrigFileName), paths.OrigFileName("something"))
			assert.Equal(t, apply(testCase.expectedNoOrigFileStampName), paths.NoOrigFileStampName("something"))
			assert.Equal(t, apply(testCase.expectedOrigParentDir), paths.OrigParentDir())
			assert.Equal(t, apply(testCase.expectedNoOrigParentDir), paths.NoOrigParentDir())
			assert.Equal(t, apply(testCase.expectedWithUsrPath), paths.WithUsrPath("something"))

		})
	}
}
