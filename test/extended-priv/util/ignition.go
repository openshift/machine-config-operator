package util

import (
	"fmt"

	"github.com/coreos/go-semver/semver"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign30types "github.com/coreos/ignition/v2/config/v3_0/types"
	ign31types "github.com/coreos/ignition/v2/config/v3_1/types"
	ign32types "github.com/coreos/ignition/v2/config/v3_2/types"
	ign33types "github.com/coreos/ignition/v2/config/v3_3/types"
	ign34types "github.com/coreos/ignition/v2/config/v3_4/types"
	ign35types "github.com/coreos/ignition/v2/config/v3_5/types"
	ign36types "github.com/coreos/ignition/v2/config/v3_6/types"
)

// supportedIgnitionSpecs lists the Ignition config specs that the MCO is expected to accept: the legacy spec 2.2
// and every stable spec 3 version. The versions are taken from the upstream Ignition config packages instead of
// MCO code, so that tests check the MCO against an independent expectation. It mirrors the specs handled by the
// MCO Ignition converter (buildConverterList in pkg/controller/common/ignition_converter.go): when the MCO starts
// supporting a new Ignition spec, add the upstream types package of that spec here. Until then, the e2e tests that
// check the supported spec versions reported by the MCO (PolarionIDs 43124, 42390/45318 and 80434) fail.
var supportedIgnitionSpecs = []semver.Version{
	ign2types.MaxVersion,
	ign30types.MaxVersion,
	ign31types.MaxVersion,
	ign32types.MaxVersion,
	ign33types.MaxVersion,
	ign34types.MaxVersion,
	ign35types.MaxVersion,
	ign36types.MaxVersion,
}

// GetSupportedIgnitionSpecVersions returns the Ignition spec versions that the MCO is expected to accept, as
// "major.minor" strings in ascending version order. This is the format and order of the list of supported spec
// versions that the MCO reports when it rejects an Ignition config version.
func GetSupportedIgnitionSpecVersions() []string {
	versions := make([]*semver.Version, 0, len(supportedIgnitionSpecs))
	for i := range supportedIgnitionSpecs {
		version := supportedIgnitionSpecs[i]
		versions = append(versions, &version)
	}
	semver.Sort(versions)

	minorVersions := make([]string, 0, len(versions))
	for _, version := range versions {
		minorVersions = append(minorVersions, fmt.Sprintf("%d.%d", version.Major, version.Minor))
	}
	return minorVersions
}
