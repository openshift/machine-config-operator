package utils

import (
	"os"

	"github.com/golang/glog"
)

// FileExists checks if the file exists, gracefully handling ENOENT.
func FileExists(path string) bool {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false
		}
		glog.Fatalf("Failed to stat %s: %v", path, err)
	}
	return true
}
