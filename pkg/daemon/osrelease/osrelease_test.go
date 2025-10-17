package osrelease

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsLikeTraditionalRHEL7(t *testing.T) {
	t.Parallel()

	var testOS OperatingSystem
	testOS.version = "7"
	assert.True(t, testOS.IsLikeTraditionalRHEL7())
	testOS.version = "7.5"
	assert.True(t, testOS.IsLikeTraditionalRHEL7())
	testOS.version = "8"
	assert.False(t, testOS.IsLikeTraditionalRHEL7())
	testOS.version = "6.8"
	assert.False(t, testOS.IsLikeTraditionalRHEL7())
}

func TestOSRelease(t *testing.T) {
	t.Parallel()

	rhcos86OSReleaseContents := `NAME="Red Hat Enterprise Linux CoreOS"
ID="rhcos"
ID_LIKE="rhel fedora"
VERSION="412.86.202301311551-0"
VERSION_ID="4.12"
PLATFORM_ID="platform:el8"
PRETTY_NAME="Red Hat Enterprise Linux CoreOS 412.86.202301311551-0 (Ootpa)"
ANSI_COLOR="0;31"
CPE_NAME="cpe:/o:redhat:enterprise_linux:8::coreos"
HOME_URL="https://www.redhat.com/"
DOCUMENTATION_URL="https://docs.openshift.com/container-platform/4.12/"
BUG_REPORT_URL="https://access.redhat.com/labs/rhir/"
REDHAT_BUGZILLA_PRODUCT="OpenShift Container Platform"
REDHAT_BUGZILLA_PRODUCT_VERSION="4.12"
REDHAT_SUPPORT_PRODUCT="OpenShift Container Platform"
REDHAT_SUPPORT_PRODUCT_VERSION="4.12"
OPENSHIFT_VERSION="4.12"
RHEL_VERSION="8.6"
OSTREE_VERSION="412.86.202301311551-0"`

	rhcos90OSReleaseContents := `NAME="Red Hat Enterprise Linux CoreOS"
ID="rhcos"
ID_LIKE="rhel fedora"
VERSION="413.90.202212151724-0"
VERSION_ID="4.13"
VARIANT="CoreOS"
VARIANT_ID=coreos
PLATFORM_ID="platform:el9"
PRETTY_NAME="Red Hat Enterprise Linux CoreOS 413.90.202212151724-0 (Plow)"
ANSI_COLOR="0;31"
CPE_NAME="cpe:/o:redhat:enterprise_linux:9::coreos"
HOME_URL="https://www.redhat.com/"
DOCUMENTATION_URL="https://docs.openshift.com/container-platform/4.13/"
BUG_REPORT_URL="https://bugzilla.redhat.com/"
REDHAT_BUGZILLA_PRODUCT="OpenShift Container Platform"
REDHAT_BUGZILLA_PRODUCT_VERSION="4.13"
REDHAT_SUPPORT_PRODUCT="OpenShift Container Platform"
REDHAT_SUPPORT_PRODUCT_VERSION="4.13"
OPENSHIFT_VERSION="4.13"
RHEL_VERSION="9.0"
OSTREE_VERSION="413.90.202212151724-0"`

	fedora37ServerOSReleaseContents := `NAME="Fedora Linux"
VERSION="37 (Server Edition)"
ID=fedora
VERSION_ID=37
VERSION_CODENAME=""
PLATFORM_ID="platform:f37"
PRETTY_NAME="Fedora Linux 37 (Server Edition)"
ANSI_COLOR="0;38;2;60;110;180"
LOGO=fedora-logo-icon
CPE_NAME="cpe:/o:fedoraproject:fedora:37"
HOME_URL="https://fedoraproject.org/"
DOCUMENTATION_URL="https://docs.fedoraproject.org/en-US/fedora/f37/system-administrators-guide/"
SUPPORT_URL="https://ask.fedoraproject.org/"
BUG_REPORT_URL="https://bugzilla.redhat.com/"
REDHAT_BUGZILLA_PRODUCT="Fedora"
REDHAT_BUGZILLA_PRODUCT_VERSION=37
REDHAT_SUPPORT_PRODUCT="Fedora"
REDHAT_SUPPORT_PRODUCT_VERSION=37
SUPPORT_END=2023-11-14
VARIANT="Server Edition"
VARIANT_ID=server`

	scosOSReleaseContents := `NAME="CentOS Stream CoreOS"
ID="scos"
ID_LIKE="rhel fedora"
VERSION="412.9.202211241749-0"
VERSION_ID="4.12"
VARIANT="CoreOS"
VARIANT_ID=coreos
PLATFORM_ID="platform:el9"
PRETTY_NAME="CentOS Stream CoreOS 412.9.202211241749-0"
ANSI_COLOR="0;31"
CPE_NAME="cpe:/o:centos:centos:9::coreos"
HOME_URL="https://centos.org/"
DOCUMENTATION_URL="https://docs.okd.io/latest/welcome/index.html"
BUG_REPORT_URL="https://access.redhat.com/labs/rhir/"
REDHAT_BUGZILLA_PRODUCT="OpenShift Container Platform"
REDHAT_BUGZILLA_PRODUCT_VERSION="4.12"
REDHAT_SUPPORT_PRODUCT="OpenShift Container Platform"
REDHAT_SUPPORT_PRODUCT_VERSION="4.12"
OPENSHIFT_VERSION="4.12"
OSTREE_VERSION="412.9.202211241749-0"`

	fcosOSReleaseContents := `NAME="Fedora Linux"
VERSION="37.20230126.20.0 (CoreOS)"
ID=fedora
VERSION_ID=37
VERSION_CODENAME=""
PLATFORM_ID="platform:f37"
PRETTY_NAME="Fedora CoreOS 37.20230126.20.0"
ANSI_COLOR="0;38;2;60;110;180"
LOGO=fedora-logo-icon
CPE_NAME="cpe:/o:fedoraproject:fedora:37"
HOME_URL="https://getfedora.org/coreos/"
DOCUMENTATION_URL="https://docs.fedoraproject.org/en-US/fedora-coreos/"
SUPPORT_URL="https://github.com/coreos/fedora-coreos-tracker/"
BUG_REPORT_URL="https://github.com/coreos/fedora-coreos-tracker/"
REDHAT_BUGZILLA_PRODUCT="Fedora"
REDHAT_BUGZILLA_PRODUCT_VERSION=37
REDHAT_SUPPORT_PRODUCT="Fedora"
REDHAT_SUPPORT_PRODUCT_VERSION=37
SUPPORT_END=2023-11-14
VARIANT="CoreOS"
VARIANT_ID=coreos
OSTREE_VERSION='37.20230126.20.0'`

	rhcos101OSReleaseContents := `NAME="Red Hat Enterprise Linux CoreOS"
VERSION="10.1.20251005-0 (Coughlan)"
ID="rhel"
ID_LIKE="centos fedora"
VERSION_ID="10.1"
PLATFORM_ID="platform:el10"
PRETTY_NAME="Red Hat Enterprise Linux CoreOS 10.1.20251005-0 (Coughlan)"
ANSI_COLOR="0;31"
LOGO="fedora-logo-icon"
CPE_NAME="cpe:/o:redhat:enterprise_linux:10.1"
HOME_URL="https://www.redhat.com/"
VENDOR_NAME="Red Hat"
VENDOR_URL="https://www.redhat.com/"
DOCUMENTATION_URL="https://access.redhat.com/documentation/en-us/red_hat_enterprise_linux/10"
BUG_REPORT_URL="https://issues.redhat.com/"
REDHAT_BUGZILLA_PRODUCT="Red Hat Enterprise Linux 10"
REDHAT_BUGZILLA_PRODUCT_VERSION=10.1
REDHAT_SUPPORT_PRODUCT="Red Hat Enterprise Linux"
REDHAT_SUPPORT_PRODUCT_VERSION="10.1 Beta"
OSTREE_VERSION='10.1.20251005-0'
VARIANT=CoreOS
VARIANT_ID=coreos
OPENSHIFT_VERSION="4.20"`

	testCases := []struct {
		Name                   string
		OSReleaseContents      string
		IsEL                   bool
		IsEL9                  bool
		IsEL10                 bool
		IsFCOS                 bool
		IsSCOS                 bool
		IsCoreOSVariant        bool
		IsLikeTraditionalRHEL7 bool
		ToPrometheusLabel      string
	}{
		{
			Name:                   "RHCOS 8.6",
			OSReleaseContents:      rhcos86OSReleaseContents,
			IsEL:                   true,
			IsEL9:                  false,
			IsEL10:                 false,
			IsFCOS:                 false,
			IsSCOS:                 false,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "RHCOS",
		},
		{
			Name:                   "RHCOS 9.0",
			OSReleaseContents:      rhcos90OSReleaseContents,
			IsEL:                   true,
			IsEL9:                  true,
			IsFCOS:                 false,
			IsSCOS:                 false,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "RHCOS",
		},
		{
			Name:                   "RHCOS 10.1",
			OSReleaseContents:      rhcos101OSReleaseContents,
			IsEL:                   true,
			IsEL9:                  false,
			IsEL10:                 true,
			IsFCOS:                 false,
			IsSCOS:                 false,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "RHEL",
		},
		{
			Name:                   "Fedora 37 Server",
			OSReleaseContents:      fedora37ServerOSReleaseContents,
			IsEL:                   false,
			IsEL9:                  false,
			IsEL10:                 false,
			IsFCOS:                 false,
			IsSCOS:                 false,
			IsCoreOSVariant:        false,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "FEDORA",
		},
		{
			Name:                   "SCOS",
			OSReleaseContents:      scosOSReleaseContents,
			IsEL:                   true,
			IsEL9:                  true,
			IsEL10:                 false,
			IsFCOS:                 false,
			IsSCOS:                 true,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "SCOS",
		},
		{
			Name:                   "FCOS",
			OSReleaseContents:      fcosOSReleaseContents,
			IsEL:                   false,
			IsEL9:                  false,
			IsEL10:                 false,
			IsFCOS:                 true,
			IsSCOS:                 false,
			IsCoreOSVariant:        true,
			IsLikeTraditionalRHEL7: false,
			ToPrometheusLabel:      "FEDORA",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			t.Parallel()
			os, err := LoadOSRelease(testCase.OSReleaseContents, testCase.OSReleaseContents)
			require.NoError(t, err)

			assert.Equal(t, testCase.IsEL, os.IsEL(), "expected IsEL() to be %v", testCase.IsEL)
			assert.Equal(t, testCase.IsEL9, os.IsEL9(), "expected IsEL9() to be %v", testCase.IsEL9)
			assert.Equal(t, testCase.IsEL10, os.IsEL10(), "expected IsEL10() to be %v", testCase.IsEL9)
			assert.Equal(t, testCase.IsCoreOSVariant, os.IsCoreOSVariant(), "expected IsCoreOSVariant() to be %v", testCase.IsCoreOSVariant)
			assert.Equal(t, testCase.IsFCOS, os.IsFCOS(), "expected IsFCOS() to be %v", testCase.IsFCOS)
			assert.Equal(t, testCase.IsSCOS, os.IsSCOS(), "expected IsSCOS() to be %v", testCase.IsSCOS)
			assert.Equal(t, testCase.IsLikeTraditionalRHEL7, os.IsLikeTraditionalRHEL7(), "expected IsLikeTraditionalRHEL7() to be %v", testCase.IsLikeTraditionalRHEL7)
			assert.Equal(t, testCase.ToPrometheusLabel, os.ToPrometheusLabel(), "expected ToPrometheusLabel() to be %s, got %s", testCase.ToPrometheusLabel, os.ToPrometheusLabel())
		})
	}
}
