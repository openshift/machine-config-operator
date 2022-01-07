package resourcemerge

func setBytesIfSet(modified *bool, existing *[]byte, required []byte) {
	if len(required) == 0 {
		return
	}
	if string(required) != string(*existing) {
		*existing = required
		*modified = true
	}
}
