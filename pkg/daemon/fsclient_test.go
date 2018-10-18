package daemon

import "os"

// CreateReturn is a structure used for testing. It holds a single return value
// set for a mocked Create call.
type CreateReturn struct {
	OsFilePointer *os.File
	Error         error
}

// StatReturn is a structure used for testing. It holds a single return value
// set for a mocked Stat call.
type StatReturn struct {
	OsFileInfo os.FileInfo
	Error      error
}

// ReadFileReturn is a structure used for testing. It holds a single return value
// set for a mocked ReadFile call.
type ReadFileReturn struct {
	Bytes []byte
	Error error
}

// FsClientMockMock is used as a mock of FsClientMock for testing.
type FsClientMock struct {
	CreateReturns    []CreateReturn
	RemoveReturns    []error
	RemoveAllReturns []error
	MkdirAllReturns  []error
	StatReturns      []StatReturn
	SymlinkReturns   []error
	ChmodReturns     []error
	ChownReturns     []error
	WriteFileReturns []error
	ReadFileReturns  []ReadFileReturn
}

// updateErrorReturns is a shortcut to pop out the error and shift
// around the Returns array.
func updateErrorReturns(returns *[]error) error {
	r := *returns
	returnValues := r[0]
	if len(r) > 0 {
		*returns = r[1:]
	}
	return returnValues
}

// Create provides a mocked implemention
func (f FsClientMock) Create(name string) (*os.File, error) {
	returnValues := f.CreateReturns[0]
	if len(f.RemoveReturns) > 0 {
		f.CreateReturns = f.CreateReturns[1:]
	}
	return returnValues.OsFilePointer, returnValues.Error
}

// Remove provides a mocked implemention
func (f FsClientMock) Remove(name string) error {
	return updateErrorReturns(&f.RemoveReturns)
}

// RemoveAll provides a mocked implemention
func (f FsClientMock) RemoveAll(path string) error {
	return updateErrorReturns(&f.RemoveReturns)
}

// MkdirAll provides a mocked implemention
func (f FsClientMock) MkdirAll(name string, perm os.FileMode) error {
	return updateErrorReturns(&f.MkdirAllReturns)
}

// Stat provides a mocked implemention
func (f FsClientMock) Stat(name string) (os.FileInfo, error) {
	returnValues := f.StatReturns[0]
	if len(f.RemoveReturns) > 0 {
		f.StatReturns = f.StatReturns[1:]
	}
	return returnValues.OsFileInfo, returnValues.Error
}

// Symlink provides a mocked implemention
func (f FsClientMock) Symlink(oldname, newname string) error {
	return updateErrorReturns(&f.SymlinkReturns)
}

// Chmod provides a mocked implemention
func (f FsClientMock) Chmod(name string, mode os.FileMode) error {
	return updateErrorReturns(&f.ChmodReturns)
}

// Chown provides a mocked implemention
func (f FsClientMock) Chown(name string, uid, gid int) error {
	return updateErrorReturns(&f.ChownReturns)
}

// WriteFile provides a mocked implemention
func (f FsClientMock) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return updateErrorReturns(&f.WriteFileReturns)
}

// ReadFile provides a mocked implemention
func (f FsClientMock) ReadFile(filename string) ([]byte, error) {
	returnValues := f.ReadFileReturns[0]
	if len(f.ReadFileReturns) > 0 {
		f.ReadFileReturns = f.ReadFileReturns[1:]
	}
	return returnValues.Bytes, returnValues.Error
}
