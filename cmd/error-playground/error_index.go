package main

import (
	_ "embed"

	"github.com/ghodss/yaml"
	mcoErrors "github.com/openshift/machine-config-operator/pkg/errors"
)

// This gets prefixed onto each numerical error code. For example, 1000 becomes MCOError1000.
const (
	ErrorCodePrefix string = "MCOError"
)

// Use standard incremented error codes. These are automatically incremented as
// additional error codes are added (via the iota keyword).
//
// Each of these is assigned a numerical value, starting from 1000.
// These do not have to be constants, but using constants makes looking up
// errors much easier as well as making the error source in the codebase much
// more searchable.
//
// This approach was inspired by the Golang stdlib HTTP status code file:
// https://cs.opensource.google/go/go/+/refs/tags/go1.19.2:src/net/http/status.go
const (
	MCOError1000 int = iota + 1000 // 1000
	MCOError1001                   // 1001
	MCOError1002                   // 1002
	MCOError1003                   // 1003
	MCOError1004                   // 1004
)

// The ErrorIndex object allows us to keep our error codes in a single place
// and easily refer to them throughout our codebase.
var errorIndex *mcoErrors.ErrorIndex = mcoErrors.NewErrorIndex(ErrorCodePrefix, mcoErrors.ErrorCodes{
	// There are two approaches for how the ErrorCodes map can be used:
	//
	// 1. Individual Golang packages can define their own error codes with
	// different prefixes. This allows one to effectively namespace their error
	// codes and add additional context using the prefix. For example, the MCD
	// could have its own set of errors beginning with "MCDError" and the MCO
	// itself could have "MCOError".
	//
	// 2. There can be a centralized error code package (within the MCO) that
	// contains all error codes for the MCO.
	MCOError1000: mcoErrors.ErrorCode{
		Description: "MalformedMachineConfig",
		Resolution: &mcoErrors.Resolution{
			DocURL: "https://link.to.kb.article/123",
		},
	},
	MCOError1001: mcoErrors.ErrorCode{
		Description: "DesiredMachineConfigCouldNotBeFound",
		Resolution: &mcoErrors.Resolution{
			DocURL: "https://link.to.kb.article/456",
		},
	},
	// Descriptions are optional.
	MCOError1002: mcoErrors.ErrorCode{
		Resolution: &mcoErrors.Resolution{
			Instructions: "Do this to resolve...",
		},
	},
	// So are resolutions.
	MCOError1003: mcoErrors.ErrorCode{
		Description: "IHaveNoResolution",
	},
	// Both descriptions and resolutions are optional. So in this manner, we
	// could use this as a placeholder for a specific error that we haven't
	// populated yet.
	MCOError1004: mcoErrors.ErrorCode{},
})

// This shows a way of representing the errors as an embedded YAML file. With
// this technique, we can make it easier to add additional error codes and
// cases. Additionally, the file can be processed by other tooling that we
// (MCO) may not necessarily own, such as tooling that can auto-generate
// documentation. Alternatively, this file could be generated by said other
// tooling *from* documentation.
//
// This embeds the YAML file into the binary. It should be noted that the file
// contents are only read when the binary is compiled.
//go:embed error_codes.yaml
var errorIndexYAML []byte

// init functions have a special purpose in Golang. They get evaluated the
// first time a package is imported. Subsequent imports of the same package
// within the same binary will not run the init function.
//
// Because of this, we only unmarshal the YAML once when the package is
// imported.
func init() {
	if err := yaml.Unmarshal(errorIndexYAML, errorIndexFromYAML); err != nil {
		// This may not be the best way to handle this. However, if the package has
		// a test suite, this init function will run when the test does.
		panic(err)
	}
}

var errorIndexFromYAML *mcoErrors.ErrorIndex = &mcoErrors.ErrorIndex{}
