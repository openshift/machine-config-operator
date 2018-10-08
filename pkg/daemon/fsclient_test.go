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
	returnValue := f.RemoveReturns[0]
	if len(f.RemoveReturns) > 0 {
		f.RemoveReturns = f.RemoveReturns[1:]
	}
	return returnValue
}

// RemoveAll provides a mocked implemention
func (f FsClientMock) RemoveAll(path string) error {
	returnValue := f.RemoveAllReturns[0]
	if len(f.RemoveAllReturns) > 0 {
		f.RemoveAllReturns = f.RemoveAllReturns[1:]
	}
	return returnValue
}

// MkdirAll provides a mocked implemention
func (f FsClientMock) MkdirAll(name string, perm os.FileMode) error {
	returnValue := f.MkdirAllReturns[0]
	if len(f.MkdirAllReturns) > 0 {
		f.MkdirAllReturns = f.MkdirAllReturns[1:]
	}
	return returnValue
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
	returnValue := f.SymlinkReturns[0]
	if len(f.SymlinkReturns) > 0 {
		f.SymlinkReturns = f.SymlinkReturns[1:]
	}
	return returnValue
}

// Chmod provides a mocked implemention
func (f FsClientMock) Chmod(name string, mode os.FileMode) error {
	returnValue := f.ChmodReturns[0]
	if len(f.ChmodReturns) > 0 {
		f.ChmodReturns = f.ChmodReturns[1:]
	}
	return returnValue
}

// Chown provides a mocked implemention
func (f FsClientMock) Chown(name string, uid, gid int) error {
	returnValue := f.ChownReturns[0]
	if len(f.RemoveReturns) > 0 {
		f.ChmodReturns = f.ChownReturns[1:]
	}
	return returnValue
}
