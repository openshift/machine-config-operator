package utils

import (
	"testing"
	"os"
	"time"
	"io/ioutil"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TestRunExt verifies that the wait machinery works, even though we're only
// just testing a single step here since it's tricky to test retries.
func TestRunExt(t *testing.T) {
	RunExt(false, 0, "echo", "echo", "from", "TestRunExt")

	result := RunExt(true, 0, "echo", "hello", "world")
	assert.Equal(t, "hello world", result)

	tmpdir, err := ioutil.TempDir("", "run_test")
	assert.Nil(t, err)
	defer os.RemoveAll(tmpdir)
	tmpf := tmpdir + "/t"
	runExtBackoff(false, wait.Backoff{Steps: 6,
		Duration: 1 * time.Second,
		Factor: 1.1},
		"sh", "-c", "echo -n x >> " + tmpf + " && test $(stat -c '%s' " + tmpf + ") = 3")
	s, err := os.Stat(tmpf)
	assert.Nil(t, err)
	assert.Equal(t, int64(3), s.Size())
}
