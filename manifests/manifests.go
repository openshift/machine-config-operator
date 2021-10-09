package manifests

import (
	"embed"
	"strings"
)

//go:embed *
var f embed.FS

// ReadFile reads and returns the content of the named file.
func ReadFile(name string) ([]byte, error) {
	return f.ReadFile(strings.TrimPrefix(name, "manifests/"))
}
