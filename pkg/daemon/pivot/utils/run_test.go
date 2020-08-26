package utils

import (
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TestRunExt verifies that the wait machinery works, even though we're only
// just testing a single step here since it's tricky to test retries.
func TestRunExt(t *testing.T) {
	RunExt(0, "echo", "echo", "from", "TestRunExt")

	tmpdir, err := ioutil.TempDir("", "run_test")
	assert.Nil(t, err)
	defer os.RemoveAll(tmpdir)
	tmpf := tmpdir + "/t"
	runExtBackoff(wait.Backoff{Steps: 6,
		Duration: 1 * time.Second,
		Factor:   1.1},
		"sh", "-c", "printf x >> "+tmpf+" && test $(wc -c < "+tmpf+") = 3")
	s, err := os.Stat(tmpf)
	assert.Nil(t, err)
	assert.Equal(t, int64(3), s.Size())
}

func TestRunExtBackground(t *testing.T) {
	o, err := RunExtBackground(0, "echo", "echo", "from", "TestRunExtBackground")
	assert.Nil(t, err)
	assert.Equal(t, o, "echo from TestRunExtBackground")
}
