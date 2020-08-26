package utils

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
)

// runImpl is the actual shell execution implementation used by other functions.
func runImpl(command string, args ...string) ([]byte, error) {
	glog.Infof("Running: %s %s\n", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	// multiplex writes to std streams so we keep seeing logs in MCD/systemd
	// but we'll still be able to give out something here
	var b bytes.Buffer
	stderr := io.MultiWriter(os.Stderr, &b)
	stdout := io.MultiWriter(os.Stdout, &b)
	cmd.Stderr = stderr
	cmd.Stdout = stdout
	err := cmd.Run()
	if err != nil {
		return nil, errors.Wrapf(err, "running %s %s failed: %s", command, strings.Join(args, " "), b.String())
	}
	return b.Bytes(), nil
}

// runExtBackoff is an extension to runExt that supports configuring retries/duration/backoff.
func runExtBackoff(backoff wait.Backoff, command string, args ...string) (string, error) {
	var (
		output  string
		lastErr error
	)
	if err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		out, err := runImpl(command, args...)
		if err != nil {
			lastErr = err
			glog.Warningf("%s failed: %v; retrying...", command, err)
			return false, nil
		}
		output = strings.TrimSpace(string(out))
		return true, nil
	}); err != nil {
		if err == wait.ErrWaitTimeout {
			return "", errors.Wrapf(lastErr, "failed to run command %s (%d tries): %v", command, backoff.Steps, err)
		}
		return "", errors.Wrap(err, "failed to run command %s (%d tries): %v")
	}
	return output, nil
}

// RunExt executes a command, optionally capturing the output and retrying multiple
// times before exiting with a fatal error.
func RunExt(retries int, command string, args ...string) (string, error) {
	return runExtBackoff(wait.Backoff{
		Steps:    retries + 1,     // times to try
		Duration: 5 * time.Second, // sleep between tries
		Factor:   2,               // factor by which to increase sleep
	},
		command, args...)
}

// RunExtBackground is like RunExt, but queues the command for "nice" CPU and
// I/O scheduling.
func RunExtBackground(retries int, command string, args ...string) (string, error) {
	args = append([]string{"--", "ionice", "-c", "3", command}, args...)
	command = "nice"
	return runExtBackoff(wait.Backoff{
		Steps:    retries + 1,     // times to try
		Duration: 5 * time.Second, // sleep between tries
		Factor:   2,               // factor by which to increase sleep
	},
		command, args...)
}
