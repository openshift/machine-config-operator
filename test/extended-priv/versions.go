package extended

import (
	"strings"

	"github.com/Masterminds/semver/v3"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// padToSemver pads versions with less than 3 identifiers to 3 using zeros. For example: 2.4 -> 2.4.0
func padToSemver(version string) string {
	parts := strings.Split(version, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts, ".")
}

// CompareVersions returns the result of comparing 2 versions using the given operator to compare
// i.e CompareVersions("3.1", ">", "3.0") return true
func CompareVersions(l, operator, r string) bool {
	paddedL := padToSemver(l)
	paddedR := padToSemver(r)

	constraint, err := semver.NewConstraint(operator + paddedR)
	if err != nil {
		e2e.Failf("Error parsing version constraint %s%s: %v", operator, r, err)
	}

	version, err := semver.NewVersion(paddedL)
	if err != nil {
		e2e.Failf("Error parsing version %s (padded from %s): %v", paddedL, l, err)
	}

	return constraint.Check(version)
}
