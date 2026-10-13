package manifests

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed *
var f embed.FS

// ReadFile reads and returns the content of the named file.
func ReadFile(name string) ([]byte, error) {
	return f.ReadFile(strings.TrimPrefix(name, "manifests/"))
}

func AllManifests() ([]string, error) {
	manifests := []string{}

	err := fs.WalkDir(f, ".", func(path string, d fs.DirEntry, _ error) error {
		if d.IsDir() {
			return nil
		}

		manifests = append(manifests, path)

		return nil
	})

	return manifests, err
}
