package daemon

import (
	mcoErrors "github.com/openshift/machine-config-operator/pkg/errors"
)

// This is an example of a package-specific ErrorIndex that solely exists for
// the daemon package. It defines its own numerical error constants and creates
// its own ErrorIndex instance.

// Numerical MCD Error Codes
const (
	mcdError1000ConfigDrift int = iota + 1000
	mcdError1001Unreconcilable

	mcdErrorPrefix string = "MCDError"
)

// These represent MCD-specific error codes.
var mcdErrorIndex *mcoErrors.ErrorIndex = mcoErrors.NewErrorIndex(mcdErrorPrefix, mcoErrors.ErrorCodes{
	mcdError1000ConfigDrift: {
		Description: "ConfigDrift",
		Resolution: &mcoErrors.Resolution{
			DocURL: "https://link.to.kb.article/123",
		},
	},
	mcdError1001Unreconcilable: {
		Description: "Unreconcilable",
		Resolution: &mcoErrors.Resolution{
			DocURL: "https://link.to.kb.article/456",
		},
	},
})
