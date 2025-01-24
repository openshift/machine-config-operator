package certrotationcontroller

import (
	"context"

	"github.com/openshift/library-go/pkg/operator/certrotation"
)

func NewCertRotationStatusReporter() *CertRotationStatusReporter {
	return &CertRotationStatusReporter{}
}

type CertRotationStatusReporter struct {
}

func (h *CertRotationStatusReporter) Report(_ context.Context, _ string, _ error) (updated bool, updateErr error) {

	// TODO: Update operator conditions for cert rotation in case of a sync error
	// It seems unnecessary to cause a degrade since this cert will be updated so rarely.
	return true, nil
}

var _ certrotation.StatusReporter = (*CertRotationStatusReporter)(nil)
