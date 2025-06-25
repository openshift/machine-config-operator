package testdata

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	pathsPrefix = "files/"
)

//go:embed all:files
var embeddedFS embed.FS

// DumpFile copies a file from embedded storage to the target directory in the FS
func DumpFile(basePath, name string) (err error) {
	path := filepath.Join(pathsPrefix, name)
	fsFile, err := embeddedFS.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if fErr := fsFile.Close(); fErr != nil {
			err = errors.Join(err, fErr)
		}
	}()
	fsFileStat, err := fsFile.Stat()
	if err != nil {
		return err
	}

	if fsFileStat.Mode().IsDir() {
		return fs.WalkDir(embeddedFS, path, func(path string, d fs.DirEntry, err error) error {
			if d.IsDir() {
				return nil
			}
			dirFile, err := embeddedFS.Open(path)
			if err != nil {
				return err
			}
			defer func() {
				if fErr := dirFile.Close(); fErr != nil {
					err = errors.Join(err, fErr)
				}
			}()
			return write(basePath, strings.TrimPrefix(path, pathsPrefix), dirFile)
		})

	}
	return write(basePath, name, fsFile)
}

func write(basePath string, name string, source fs.File) (err error) {
	dirPath := _filePath(basePath, filepath.Dir(name))
	err = os.MkdirAll(dirPath, os.FileMode(0o750))
	if err != nil {
		return err
	}

	filePath := _filePath(basePath, name)
	destFile, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer func() {
		if fErr := destFile.Close(); fErr != nil {
			err = errors.Join(err, fErr)
		}
	}()
	_, err = io.Copy(destFile, source)
	return err
}

func _filePath(dir, name string) string {
	canonicalName := strings.Replace(name, "\\", "/", -1)
	return filepath.Join(append([]string{dir}, strings.Split(canonicalName, "/")...)...)
}
