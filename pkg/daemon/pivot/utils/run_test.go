package utils

import (
	"testing"
	"os"
	"time"
	"io/ioutil"

	"k8s.io/apimachinery/pkg/util/wait"
)

// TestRun should always pass. The function will panic if it is unable to
// execute the shell command(s) or the command returns non-zero.
func TestRun(t *testing.T) {
	Run("echo", "echo", "from", "TestRun")
}

// TestRunGetOut verifies the output of running a command is
// its output, trimmed of whitespace.
func TestRunGetOut(t *testing.T) {
	if result := RunGetOut("echo", "hello", "world"); result != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", result)
	}
}

// TestRunIgnoreErr verifies the a failed command doesn't cause exit
func TestRunIgnoreErr(t *testing.T) {
	// Should succeed and cause no exit
	RunIgnoreErr("echo", "test")
	// Should fail and cause no exit
	RunIgnoreErr("acommandthatdoesNOTEXIST")
}

// TestRunExt verifies that the wait machinery works, even though we're only
// just testing a single step here since it's tricky to test retries.
func TestRunExt(t *testing.T) {
	RunExt(false, 0, "echo", "echo", "from", "TestRunExt")

	if result := RunExt(true, 0, "echo", "hello", "world"); result != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", result)
	}

	tmpdir, err := ioutil.TempDir("", "run_test")
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer os.RemoveAll(tmpdir)
	tmpf := tmpdir + "/t"
	runExtBackoff(false, wait.Backoff{Steps: 6,
		Duration: 1 * time.Second,
		Factor: 1.1},
		"sh", "-c", "echo -n x >> " + tmpf + " && test $(stat -c '%s' " + tmpf + ") = 3")
	s, err := os.Stat(tmpf)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if s.Size() != 3 {
		t.Fatalf("Expected size 3")
	}
}
