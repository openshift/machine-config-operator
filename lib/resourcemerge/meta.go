package resourcemerge

import (
	"bytes"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

func setBytesIfSet(modified *bool, existing *[]byte, required []byte) {
	// Check if the current required bytes are empty
	if len(required) == 0 {
		// If existing is not already empty, update it and set modified to true
		if len(*existing) != 0 {
			*existing = nil
			*modified = true
		}
		return
	}
	// Update only if there is a change in the content
	if !bytes.Equal(*existing, required) {
		*existing = make([]byte, len(required))
		copy(*existing, required)
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
