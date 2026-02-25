package architecture

import (
	"fmt"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"k8s.io/apimachinery/pkg/util/sets"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

type Architecture int

const (
	AMD64 Architecture = iota
	ARM64
	PPC64LE
	S390X
	MULTI
	UNKNOWN
)

// String constants for architecture names
const (
	amd64String   = "amd64"
	arm64String   = "arm64"
	ppc64leString = "ppc64le"
	s390xString   = "s390x"
	multiString   = "multi"
	x86_64String  = "x86_64"
	aarch64String = "aarch64"
)

// FromString returns the Architecture value for the given string
func FromString(arch string) Architecture {
	switch arch {
	case amd64String:
		return AMD64
	case arm64String:
		return ARM64
	case ppc64leString:
		return PPC64LE
	case s390xString:
		return S390X
	case multiString:
		return MULTI
	default:
		e2e.Failf("Unknown architecture %s", arch)
	}
	return AMD64
}

// String returns the string value for the given Architecture
func (a Architecture) String() string {
	switch a {
	case AMD64:
		return amd64String
	case ARM64:
		return arm64String
	case PPC64LE:
		return ppc64leString
	case S390X:
		return s390xString
	case MULTI:
		return multiString
	default:
		e2e.Failf("Unknown architecture %d", a)
	}
	return ""
}

// GNUString returns the GNU-style architecture string (x86_64, aarch64, etc.)
func (a Architecture) GNUString() string {
	switch a {
	case AMD64:
		return x86_64String
	case ARM64:
		return aarch64String
	case PPC64LE:
		return ppc64leString
	case S390X:
		return s390xString
	case MULTI:
		return multiString
	default:
		e2e.Failf("Unknown architecture %d", a)
	}
	return ""
}

// ClusterArchitecture determines the architecture of the cluster
func ClusterArchitecture(oc *exutil.CLI) (architecture Architecture) {
	output, err := oc.WithoutNamespace().AsAdmin().Run("get").Args("nodes", "-o=jsonpath={.items[*].status.nodeInfo.architecture}").Output()
	if err != nil {
		e2e.Failf("unable to get the cluster architecture: %v", err)
	}
	if output == "" {
		e2e.Failf("the retrieved architecture is empty")
	}
	architectureList := strings.Split(output, " ")
	architecture = FromString(architectureList[0])
	for _, nodeArchitecture := range architectureList[1:] {
		if FromString(nodeArchitecture) != architecture {
			e2e.Logf("Found multi-arch node cluster")
			return MULTI
		}
	}
	return
}

// SkipNonAmd64SingleArch skips the test if the cluster architecture is not AMD64
func SkipNonAmd64SingleArch(oc *exutil.CLI) (architecture Architecture) {
	architecture = ClusterArchitecture(oc)
	if architecture != AMD64 {
		g.Skip(fmt.Sprintf("Skip for cluster architecture: %s", architecture.String()))
	}
	return
}

const (
	// NodeArchitectureLabel is the label used to identify the architecture of a node
	NodeArchitectureLabel = "kubernetes.io/arch"
)

// GetAvailableArchitecturesSet returns multi-arch node cluster's Architectures
func GetAvailableArchitecturesSet(oc *exutil.CLI) []Architecture {
	output, err := oc.WithoutNamespace().AsAdmin().Run("get").Args("nodes", "-o=jsonpath={.items[*].status.nodeInfo.architecture}").Output()
	if err != nil {
		e2e.Failf("unable to get the cluster architecture: %v", err)
	}
	if output == "" {
		e2e.Failf("the retrieved architecture is empty")
	}
	architectureList := strings.Split(output, " ")
	archMap := make(map[Architecture]bool, 0)
	var architectures []Architecture
	for _, nodeArchitecture := range architectureList {
		if _, ok := archMap[FromString(nodeArchitecture)]; !ok {
			archMap[FromString(nodeArchitecture)] = true
			architectures = append(architectures, FromString(nodeArchitecture))
		}
	}
	return architectures
}

// SkipIfNoNodeWithArchitectures skip the test if the cluster is one of the given architectures
func SkipIfNoNodeWithArchitectures(oc *exutil.CLI, architectures ...Architecture) {
	if sets.New(
		GetAvailableArchitecturesSet(oc)...).IsSuperset(
		sets.New(architectures...)) {
		return
	}
	g.Skip(fmt.Sprintf("Skip for no nodes with requested architectures"))
}
