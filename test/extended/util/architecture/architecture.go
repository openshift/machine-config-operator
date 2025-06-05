package architecture

import (
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

type Architecture string

const (
	AMD64   Architecture = "amd64"
	ARM64   Architecture = "arm64"
	PPC64LE Architecture = "ppc64le"
	S390X   Architecture = "s390x"
	MULTI   Architecture = "multi"
	UNKNOWN Architecture = "unknown"
)

const (
	NodeArchitectureLabel = "kubernetes.io/arch"
)

// FromString returns the Architecture value for the given string
func FromString(arch string) Architecture {
	switch arch {
	case AMD64.String():
		return AMD64
	case ARM64.String():
		return ARM64
	case PPC64LE.String():
		return PPC64LE
	case S390X.String():
		return S390X
	case MULTI.String():
		return MULTI
	default:
		e2e.Failf("Unknown architecture %s", arch)
	}
	return AMD64
}

// String returns the string value for the given Architecture
func (a Architecture) String() string {
	archString := string(a)
	if a == UNKNOWN {
		e2e.Failf("Unknown architecture %s", archString)
	}
	return archString
}

func (a Architecture) GNUString() string {
	switch a {
	case AMD64:
		return "x86_64"
	case ARM64:
		return "aarch64"
	case PPC64LE:
		return "ppc64le"
	case S390X:
		return "s390x"
	case MULTI:
		return "multi"
	default:
		e2e.Failf("Unknown architecture %s", a)
	}
	return ""
}
