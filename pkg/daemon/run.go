package daemon

import (
	"os"
	"os/exec"
	"strings"

	"github.com/golang/glog"
)

// ProcessClient handles running and getting results from commands on the host.
type ProcessClient interface {
	Run(string, ...string) error
	RunGetOut(string, ...string) ([]byte, error)
}

// NewProcessClient returns the default client for running commands on the host
func NewProcessClient() ProcessClient {
	return &DefaultProcessClient{}
}

// DefaultProcessClient implements the default ProcessClient
type DefaultProcessClient struct{}

// Run executes a command, logging it.
func (p *DefaultProcessClient) Run(command string, args ...string) error {
	glog.Infof("Running: %s %s\n", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// RunGetOut executes a command, logging it, and return the stdout output.
func (p *DefaultProcessClient) RunGetOut(command string, args ...string) ([]byte, error) {
	glog.Infof("Running captured: %s %s\n", command, strings.Join(args, " "))
	cmd := exec.Command(command, args...)
	cmd.Stderr = os.Stderr
	rawOut, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return rawOut, nil
}
