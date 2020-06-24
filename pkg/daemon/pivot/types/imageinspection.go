package types

const (
	// PivotNamePrefix is literally the name prefix of the new pivot
	PivotNamePrefix = "ostree-container-pivot-"

	// PivotFailurePath is the path pivot writes its error to when it fails. We need this to bubble
	// up any error happning there as we just log those errors in MCD and are useful unless we report
	// them up the stack
	PivotFailurePath = "/run/pivot/failure"
)
