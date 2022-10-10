package mco

import (
	mcoErrors "github.com/openshift/machine-config-operator/pkg/errors"
)

// This is an example of a singular centralized ErrorIndex instance that other
// packages can import and use. The intent behind this is to provide a central
// source of error codes and resolutions that other packages can import and
// use.
const (
	MCOError1000Degraded int = iota + 1000
)

const mcoErrorPrefix string = "MCOError"

// errs is private to this package on purpose.
var errs *mcoErrors.ErrorIndex = mcoErrors.NewErrorIndex(mcoErrorPrefix, mcoErrors.ErrorCodes{
	MCOError1000Degraded: {
		Description: "Degraded",
		Resolution: &mcoErrors.Resolution{
			DocURL: "https://link.to.kb.artcle/123",
		},
	},
})

// Exposed so that we can easily add error codes to a provided error.
func Wrap(code int, err error) error {
	return errs.Wrap(code, err)
}
