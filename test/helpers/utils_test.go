package helpers

import (
	"testing"

	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSSHPaths(t *testing.T) {
	t.Parallel()

	rhcos8OSReleaseContents := `NAME="Red Hat Enterprise Linux CoreOS"
ID="rhcos"
ID_LIKE="rhel fedora"
VERSION="412.86.202301311551-0"
VERSION_ID="4.12"
PLATFORM_ID="platform:el8"
RHEL_VERSION="8.6"
VARIANT_ID=coreos`

	rhcos9OSReleaseContents := `NAME="Red Hat Enterprise Linux CoreOS"
ID="rhcos"
ID_LIKE="rhel fedora"
VERSION="413.90.202212151724-0"
VERSION_ID="4.13"
PLATFORM_ID="platform:el9"
RHEL_VERSION="9.0"
VARIANT="CoreOS"
VARIANT_ID=coreos`

	rhcos10OSReleaseContents := `NAME="Red Hat Enterprise Linux CoreOS"
VERSION="10.1.20251005-0 (Coughlan)"
ID="rhel"
ID_LIKE="centos fedora"
VERSION_ID="10.1"
PLATFORM_ID="platform:el10"
VARIANT=CoreOS
VARIANT_ID=coreos`

	scosOSReleaseContents := `NAME="CentOS Stream CoreOS"
ID="scos"
ID_LIKE="rhel fedora"
VERSION="412.9.202211241749-0"
VERSION_ID="4.12"
PLATFORM_ID="platform:el9"
VARIANT="CoreOS"
VARIANT_ID=coreos`

	// CentOS Stream CoreOS 10 uses ID=centos (not ID=scos), so IsSCOS() is false
	// and GetSSHPaths must rely on IsEL10() for the new SSH key path.
	centos10OSReleaseContents := `NAME="CentOS Stream CoreOS"
VERSION="10.0.20260614-0 (Coughlan)"
ID="centos"
ID_LIKE="rhel fedora"
VERSION_ID="10"
PLATFORM_ID="platform:el10"
VARIANT=CoreOS
VARIANT_ID=coreos`

	fcosOSReleaseContents := `NAME="Fedora Linux"
VERSION="37.20230126.20.0 (CoreOS)"
ID=fedora
VERSION_ID=37
PLATFORM_ID="platform:f37"
VARIANT="CoreOS"
VARIANT_ID=coreos`

	testCases := []struct {
		name            string
		osRelease       string
		wantExpected    string
		wantNotExpected string
	}{
		{
			name:            "RHCOS 8 uses legacy SSH key path",
			osRelease:       rhcos8OSReleaseContents,
			wantExpected:    constants.RHCOS8SSHKeyPath,
			wantNotExpected: constants.RHCOS9SSHKeyPath,
		},
		{
			name:            "RHCOS 9 uses new SSH key path",
			osRelease:       rhcos9OSReleaseContents,
			wantExpected:    constants.RHCOS9SSHKeyPath,
			wantNotExpected: constants.RHCOS8SSHKeyPath,
		},
		{
			name:            "RHCOS 10 / EL10 uses new SSH key path",
			osRelease:       rhcos10OSReleaseContents,
			wantExpected:    constants.RHCOS9SSHKeyPath,
			wantNotExpected: constants.RHCOS8SSHKeyPath,
		},
		{
			name:            "CentOS Stream CoreOS 10 / EL10 uses new SSH key path",
			osRelease:       centos10OSReleaseContents,
			wantExpected:    constants.RHCOS9SSHKeyPath,
			wantNotExpected: constants.RHCOS8SSHKeyPath,
		},
		{
			name:            "SCOS uses new SSH key path",
			osRelease:       scosOSReleaseContents,
			wantExpected:    constants.RHCOS9SSHKeyPath,
			wantNotExpected: constants.RHCOS8SSHKeyPath,
		},
		{
			name:            "FCOS uses new SSH key path",
			osRelease:       fcosOSReleaseContents,
			wantExpected:    constants.RHCOS9SSHKeyPath,
			wantNotExpected: constants.RHCOS8SSHKeyPath,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			os, err := osrelease.LoadOSRelease(testCase.osRelease, testCase.osRelease)
			require.NoError(t, err)

			sshPaths := GetSSHPaths(os)
			assert.Equal(t, testCase.wantExpected, sshPaths.Expected)
			assert.Equal(t, testCase.wantNotExpected, sshPaths.NotExpected)
		})
	}
}
