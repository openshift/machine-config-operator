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

const (
	NodeArchitectureLabel = "kubernetes.io/arch"
)

// FromString returns the Architecture value for the given string
func FromString(arch string) Architecture {
	switch arch {
	case "amd64":
		return AMD64
	case "arm64":
		return ARM64
	case "ppc64le":
		return PPC64LE
	case "s390x":
		return S390X
	case "multi":
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
		return "amd64"
	case ARM64:
		return "arm64"
	case PPC64LE:
		return "ppc64le"
	case S390X:
		return "s390x"
	case MULTI:
		return "multi"
	default:
		e2e.Failf("Unknown architecture %d", a)
	}
	return ""
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
		e2e.Failf("Unknown architecture %d", a)
	}
	return ""
}
