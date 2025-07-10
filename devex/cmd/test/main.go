package main

import (
	"io/fs"
	"path/filepath"
	"strings"
)

func main() {
	path := "/home/pabrodri/test-must-gather-clean/"
	_ = filepath.WalkDir(path, func(path2 string, dirEntry fs.DirEntry, err error) error {
		if !strings.HasSuffix(path2, ".yaml") && !strings.HasSuffix(path2, ".yml") && !strings.HasSuffix(path2, ".json") {
			return nil
		}
		println(path2)
		return nil
	})
}
