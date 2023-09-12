package resourcemerge

import (
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
)

func setBytesIfSet(modified *bool, existing *[]byte, required []byte) {
	if len(required) == 0 {
		return
	}
	if string(required) != string(*existing) {
		*existing = required
		*modified = true
	}
}

func setIPFamiliesIfSet(modified *bool, existing *mcfgv1.IPFamiliesType, required mcfgv1.IPFamiliesType) {
	if len(required) == 0 {
		return
	}
	if required != *existing {
		*existing = required
		*modified = true
	}
}
