package daemon

/*
	mocks.go provides mocking functions two purpouses:
		- unit testability
		- functions that may be different between OKD and OCP
*/

import (
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"

	"github.com/golang/glog"
)

// userLookupFunc is any function that returns a user and an error
type userLookupFunc func(string) (*user.User, error)

var fakeRoot string

// userLookup defaults to user.Lookup. However to test
// the differences between OKD and OCP we use it indirectly
var (
	defaultUserLookup userLookupFunc = user.Lookup
	userLookup                       = defaultUserLookup
)

// nooperFunc does nothing
func nooperFunc() error { return nil }

// setTmpRoot sets a temp root and returns a cleanup func for use
// in a defer function.
func setTmpRoot() (func(), error) {
	d, err := ioutil.TempDir("", "fakeroot")
	if err != nil {
		return nil, err
	}
	fakeRoot = d
	return func() {
		fakeRoot = ""
		if err := os.RemoveAll(d); err != nil {
			glog.Warningf("Failed to remove fakeRoot: %v", err)
		}
	}, nil
}

// fakeUserLookup is used to test user lookups by using the current
// user's information.
func fakeUserLookup(string) (*user.User, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	return &user.User{
		Uid:     u.Uid,
		Gid:     u.Gid,
		Name:    u.Name,
		HomeDir: filepath.Join(fakeRoot, u.HomeDir),
	}, nil
}
