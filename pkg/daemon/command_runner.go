package daemon

import (
	"fmt"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

// CommandRunner abstracts command execution for testing and flexibility.
type CommandRunner interface {
	// RunGetOut executes a command and returns its stdout output.
	// On error, stderr is included in the error message.
	RunGetOut(command string, args ...string) ([]byte, error)
}

// CommandRunnerOS is the production implementation that executes real OS commands.
type CommandRunnerOS struct{}

// RunGetOut executes a command, logs it, and returns stdout.
// On error, stderr is included in the error message (truncated to 256 chars).
func (r *CommandRunnerOS) RunGetOut(command string, args ...string) ([]byte, error) {
	klog.Infof("Running captured: %s %s", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	rawOut, err := cmd.Output()
	if err != nil {
		errtext := ""
		if e, ok := err.(*exec.ExitError); ok {
			// Trim to max of 256 characters
			errtext = fmt.Sprintf("\n%s", truncate(string(e.Stderr), 256))
		}
		return nil, fmt.Errorf("error running %s %s: %s%s", command, strings.Join(args, " "), err, errtext)
	}
	return rawOut, nil
}

// MockCommandRunner is a test implementation that returns pre-configured outputs.
type MockCommandRunner struct {
	outputs map[string][]byte
	errors  map[string]error
}

// RunGetOut returns pre-configured output or error based on the command string.
// It matches commands using "command arg1 arg2..." as the key.
// Returns an error if no matching output or error is found.
func (m *MockCommandRunner) RunGetOut(command string, args ...string) ([]byte, error) {
	key := command + " " + strings.Join(args, " ")
	if out, ok := m.outputs[key]; ok {
		return out, nil
	}
	if err, ok := m.errors[key]; ok {
		return nil, err
	}
	return nil, fmt.Errorf("no output for command %s found", command)
}
