package util

import (
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/openshift/machine-config-operator/test/extended/testdata"
)

var (
	fixtureDirLock sync.Once
	fixtureDir     string
)

// FixturePath returns an absolute path to a local copy of a fixture file
func FixturePath(elem ...string) string {
	fixtureDirLock.Do(func() {
		dir, err := os.MkdirTemp("", "fixture-testdata-dir")
		if err != nil {
			panic(err)
		}
		fixtureDir = dir
	})
	relativePath := path.Join(elem...)
	fullPath := path.Join(fixtureDir, relativePath)
	if err := testdata.DumpFile(fixtureDir, relativePath); err != nil {
		panic(err)
	}

	p, err := filepath.Abs(fullPath)
	if err != nil {
		panic(err)
	}
	return p
}
