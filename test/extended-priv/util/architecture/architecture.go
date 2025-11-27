package architecture

import (
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
