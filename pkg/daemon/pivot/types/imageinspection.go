package types

const (
	// PivotNamePrefix is literally the name prefix of the new pivot
	PivotNamePrefix = "ostree-container-pivot-"

	// PivotFailurePath is the path pivot writes its error to when it fails. We need this to bubble
	// up any errors since we only log those errors in MCD. If we don't do this then the error messages
	// don't get visibility up the stack.
	PivotFailurePath = "/run/pivot/failure"
)
