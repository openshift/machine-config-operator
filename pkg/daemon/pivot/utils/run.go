package utils

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/golang/glog"
	errors "github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
)

// runImpl is the actual shell execution implementation used by other functions.
func runImpl(capture bool, command string, args ...string) ([]byte, error) {
	glog.Infof("Running: %s %s\n", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	cmd.Stderr = os.Stderr
	var stdout bytes.Buffer
	if !capture {
		cmd.Stdout = os.Stdout
	} else {
		cmd.Stdout = &stdout
	}
	err := cmd.Run()
	if err != nil {
		return nil, err
	}
	if capture {
		return stdout.Bytes(), nil
	}
	return []byte{}, nil
}

// runExtBackoff is an extension to runExt that supports configuring retries/duration/backoff.
func runExtBackoff(capture bool, backoff wait.Backoff, command string, args ...string) string {
	var output string
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		if out, e := runImpl(capture, command, args...); e != nil {
			glog.Warningf("%s failed: %v; retrying...", command, e)
			return false, nil
		} else if capture {
			output = strings.TrimSpace(string(out))
		}
		return true, nil
	})
	if err != nil {
		glog.Fatalf("%s: %s", command, err)
	}
	return output
}

// RunExt executes a command, optionally capturing the output and retrying multiple
// times before exiting with a fatal error.
func RunExt(capture bool, retries int, command string, args ...string) string {
	return runExtBackoff(capture, wait.Backoff{
		Steps:    retries + 1,     // times to try
		Duration: 5 * time.Second, // sleep between tries
		Factor:   2,               // factor by which to increase sleep
	},
		command, args...)
}

// Run executes a command, logging it
func Run(command string, args ...string) error {
	if _, err := runImpl(false, command, args...); err != nil {
		return errors.Wrapf(err, "%s failed", command)
	}
	return nil
}

// RunIgnoreErr is like Run(..), but doesn't exit on errors
func RunIgnoreErr(command string, args ...string) {
	if _, err := runImpl(false, command, args...); err != nil {
		glog.Warningf("(ignored) %s: %s", command, err)
	}
}

// RunGetOut is like Run(..), but get the output as a string
func RunGetOut(command string, args ...string) (string, error) {
	var err error
	var out []byte
	if out, err = runImpl(true, command, args...); err != nil {
		return "", errors.Wrapf(err, "%s failed", command)
	}
	return strings.TrimSpace(string(out)), nil
}
