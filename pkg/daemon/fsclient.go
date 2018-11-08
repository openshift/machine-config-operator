package daemon

import (
	"io"
	"io/ioutil"
	"os"
)

// FileSystemClient abstracts file/directory manipulation operations
type FileSystemClient interface {
	Create(string) (*os.File, error)
	Remove(string) error
	RemoveAll(string) error
	MkdirAll(string, os.FileMode) error
	Stat(string) (os.FileInfo, error)
	Symlink(string, string) error
	Chmod(string, os.FileMode) error
	Chown(string, int, int) error
	WriteFile(filename string, data []byte, perm os.FileMode) error
	ReadAll(reader io.Reader) ([]byte, error)
	ReadFile(filename string) ([]byte, error)
}

// FsClient is used to hang the FileSystemClient functions on.
type FsClient struct{}

// Create implements os.Create
func (f FsClient) Create(name string) (*os.File, error) {
	return os.Create(name)
}

// Remove implements os.Remove
func (f FsClient) Remove(name string) error {
	return os.Remove(name)
}

// RemoveAll implements os.RemoveAll
func (f FsClient) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// MkdirAll implements os.MkdirAll
func (f FsClient) MkdirAll(name string, perm os.FileMode) error {
	return os.MkdirAll(name, perm)
}

// Stat implements os.Stat
func (f FsClient) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}

// Symlink implements os.Symlink
func (f FsClient) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, newname)
}

// Chmod implements os.Chmod
func (f FsClient) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(name, mode)
}

// Chown implements os.Chown
func (f FsClient) Chown(name string, uid, gid int) error {
	return os.Chown(name, uid, gid)
}

// WriteFile implements ioutil.WriteFile
func (f FsClient) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return ioutil.WriteFile(filename, data, perm)
}

// ReadFile implements ioutil.WriteFile
func (f FsClient) ReadFile(filename string) ([]byte, error) {
	return ioutil.ReadFile(filename)
}

// ReadAll implements ioutil.ReadAll
func (f FsClient) ReadAll(reader io.Reader) ([]byte, error) {
	return ioutil.ReadAll(reader)
}

// NewFileSystemClient creates a new file system client using the default
// implementations provided by the os package.
func NewFileSystemClient() FileSystemClient {
	return FsClient{}
}
