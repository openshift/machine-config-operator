// Package marketplace holds the AWS Marketplace RHCOS product catalog and the version-parsing/
// comparison logic used to identify and validate Marketplace AMIs. It is dependency-light
// (stdlib only) so it can be imported both by the boot image controller (pkg/controller/bootimage)
// and by standalone tooling (e.g. devex/cmd/check-aws-marketplace-skew) without pulling in
// client-go/controller-runtime.
package marketplace

import (
	"regexp"
	"strconv"
	"strings"
)

// ROSAProductID is the marketplace product ID for ROSA Classic.
const ROSAProductID = "34850061-abaf-402d-92df-94325c9e947f"

// Products maps AWS Marketplace product IDs to human-readable variant names.
// These IDs are stable — they are tied to marketplace listings and will not change.
var Products = map[string]string{
	// x86_64
	"59ead7de-2540-4653-a8b0-fa7926d5c845": "OCP x86_64",
	"963b36c3-de6f-48ed-b802-2b38b2a2cdeb": "OKE x86_64",
	"f5da01a6-d046-487c-9072-42fe53b1cad4": "OPP x86_64",
	// arm64
	"abc249f8-7440-45f7-a4b1-c026baff64c1": "OCP arm64",
	"d2d3ebcd-c1ca-43d8-bf0a-530433200f35": "OKE arm64",
	"be6d3e94-c8dc-4a3e-9218-4b449b11f06f": "OPP arm64",
	// x86_64 EMEA
	"962791c7-3ae5-46d1-ba62-c7a5ebac54fd": "OCP EMEA x86_64",
	"7026c8d7-392c-4010-b93c-f93f7bc5495f": "OKE EMEA x86_64",
	"628c9df3-0254-4f91-bc1f-8619d1b8eaa8": "OPP EMEA x86_64",
	// ROSA
	ROSAProductID: "ROSA",
}

// ProductName returns the human-readable variant name for a marketplace product ID,
// falling back to the product ID itself if it is not in the map.
func ProductName(productID string) string {
	if name, ok := Products[productID]; ok {
		return name
	}
	return productID
}

var productIDRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ExtractProductID returns the trailing UUID-format product ID from a marketplace AMI name, e.g.:
//
//	RHEL-9.4-RHCOS-9.6_HVM_GA-20260210-x86_64-0-{product-id}
//
// Returns an empty string if no valid product ID is found.
func ExtractProductID(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) < 5 {
		return ""
	}
	candidate := strings.Join(parts[len(parts)-5:], "-")
	if productIDRegex.MatchString(candidate) {
		return candidate
	}
	return ""
}

// descriptionVersionRe matches the full RHCOS release string embedded in marketplace AMI descriptions.
// The full match is the API-valid release string; group 1 is the N.M token for version comparison.
// Both description formats in use embed the full release string inline:
//   - RHEL marketplace: "RHEL CoreOS 9.6 9.6.20260210-0 x86_64"   → "9.6.20260210-0" / "9.6"
//   - ROSA:             "rhcos-9.6.20250701-0-x86_64"              → "9.6.20250701-0"  / "9.6"
var descriptionVersionRe = regexp.MustCompile(`(\d+\.\d+)\.(?:[0-9]{8}|[0-9]{12})-\d+`)

// ExtractVersionFromDescription parses the RHCOS release string from a marketplace AMI description.
// Returns the full release string (e.g. "9.6.20260210-0") suitable for ClusterBootImageAutomatic.RHCOSVersion,
// the N.M token (e.g. "9.6") for version comparison, and whether parsing succeeded.
func ExtractVersionFromDescription(description string) (fullVersion, token string, ok bool) {
	m := descriptionVersionRe.FindStringSubmatch(description)
	if m == nil {
		return "", "", false
	}
	return m[0], m[1], true
}

// CmpRHCOSVersion compares two full RHCOS release strings (e.g. "9.6.20260210-0") by their
// major.minor version only. Returns negative if a < b, zero if equal, positive if a > b.
func CmpRHCOSVersion(a, b string) int {
	tokenOf := func(v string) string {
		p := strings.SplitN(v, ".", 3)
		if len(p) < 2 {
			return v
		}
		return p[0] + "." + p[1]
	}
	return CmpVersionToken(tokenOf(a), tokenOf(b))
}

// CmpVersionToken compares two "major.minor" version tokens.
// Returns negative if a < b, zero if equal, positive if a > b.
func CmpVersionToken(a, b string) int {
	parse := func(s string) (int, int) {
		parts := strings.SplitN(s, ".", 2)
		if len(parts) != 2 {
			return 0, 0
		}
		major, _ := strconv.Atoi(parts[0])
		minor, _ := strconv.Atoi(parts[1])
		return major, minor
	}
	aMaj, aMin := parse(a)
	bMaj, bMin := parse(b)
	if aMaj != bMaj {
		return aMaj - bMaj
	}
	return aMin - bMin
}

// IsPreRHELAlignedToken reports whether a version token uses the pre-4.19 OCP-based RHCOS
// versioning scheme (e.g. "418.94") rather than the RHEL-aligned scheme (e.g. "9.6").
// Pre-RHEL-aligned tokens have a major component > 100 (encoding the OCP major version * 100 + minor).
func IsPreRHELAlignedToken(token string) bool {
	major, _ := strconv.Atoi(strings.SplitN(token, ".", 2)[0])
	return major > 100
}
