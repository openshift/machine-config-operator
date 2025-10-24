package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCommandRunnerOS_RunGetOut(t *testing.T) {
	runner := &CommandRunnerOS{}

	// Test successful command with no output
	o, err := runner.RunGetOut("true")
	assert.Nil(t, err)
	assert.Equal(t, len(o), 0)

	// Test failed command
	o, err = runner.RunGetOut("false")
	assert.NotNil(t, err)

	// Test successful command with output
	o, err = runner.RunGetOut("echo", "hello")
	assert.Nil(t, err)
	assert.Equal(t, string(o), "hello\n")

	// Test command that outputs to stderr and exits with error
	// base64 encode "oops" so we can't match on the command arguments
	o, err = runner.RunGetOut("/bin/sh", "-c", "echo hello; echo b29vcwo== | base64 -d 1>&2; exit 1")
	assert.Error(t, err)
	errtext := err.Error()
	assert.Contains(t, errtext, "exit status 1\nooos\n")

	// Test command that doesn't exist
	o, err = runner.RunGetOut("/usr/bin/test-failure-to-exec-this-should-not-exist", "arg")
	assert.Error(t, err)
}

func TestMockCommandRunner_RunGetOut(t *testing.T) {
	// Test mock with pre-configured output
	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"echo hello":        []byte("hello\n"),
			"rpm-ostree status": []byte("State: idle\n"),
		},
		errors: map[string]error{},
	}

	o, err := mock.RunGetOut("echo", "hello")
	assert.Nil(t, err)
	assert.Equal(t, []byte("hello\n"), o)

	o, err = mock.RunGetOut("rpm-ostree", "status")
	assert.Nil(t, err)
	assert.Equal(t, []byte("State: idle\n"), o)

	// Test mock with pre-configured error
	mock = &MockCommandRunner{
		outputs: map[string][]byte{},
		errors: map[string]error{
			"cmd --fail": assert.AnError,
		},
	}

	o, err = mock.RunGetOut("cmd", "--fail")
	assert.Error(t, err)
	assert.Equal(t, assert.AnError, err)

	// Test mock with no matching command (should return error)
	mock = &MockCommandRunner{
		outputs: map[string][]byte{},
		errors:  map[string]error{},
	}

	o, err = mock.RunGetOut("unknown", "command")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no output for command")
}

func TestMockCommandRunner_Priority(t *testing.T) {
	// Test that outputs take priority over errors
	mock := &MockCommandRunner{
		outputs: map[string][]byte{
			"test cmd": []byte("output\n"),
		},
		errors: map[string]error{
			"test cmd": assert.AnError,
		},
	}

	o, err := mock.RunGetOut("test", "cmd")
	assert.Nil(t, err)
	assert.Equal(t, []byte("output\n"), o)
}
