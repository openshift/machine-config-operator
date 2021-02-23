package daemon

import (
	"fmt"
	"testing"
)

/*
 * This file contains test code for the rpm-ostree client. It is meant to be used when
 * testing the daemon and mocking the responses that would normally be executed by the
 * client.
 */

// GetBootedOSImageURLReturn is a structure used for testing. The fields correspond with the
// return values in GetBootedOSImageURL implementations.
type GetBootedOSImageURLReturn struct {
	OsImageURL string
	Version    string
	Error      error
}

// RpmOstreeClientMock is a testing implementation of NodeUpdaterClient. Fields presented here
// hold return values that will be returned when their corresponding methods are called.
type RpmOstreeClientMock struct {
	GetBootedOSImageURLReturns []GetBootedOSImageURLReturn
	ExpectKernelArgs           []string
	ExpectNewKernelArgs        []string
	runRpmOstreeFunc           rpmOstreeCommander
}

// GetBootedOSImageURL implements a test version of RpmOStreeClients GetBootedOSImageURL.
// It returns an OsImageURL, Version, and Error as defined in GetBootedOSImageURLReturns in order.
func (r RpmOstreeClientMock) GetBootedOSImageURL() (string, string, error) {
	returnValues := r.GetBootedOSImageURLReturns[0]
	if len(r.GetBootedOSImageURLReturns) > 1 {
		r.GetBootedOSImageURLReturns = r.GetBootedOSImageURLReturns[1:]
	}
	return returnValues.OsImageURL, returnValues.Version, returnValues.Error
}

// PullAndRebase is a mock
func (r RpmOstreeClientMock) Rebase(string, string) (bool, error) {
	return false, nil
}

func (r RpmOstreeClientMock) GetStatus() (string, error) {
	return "rpm-ostree mock: blah blah some status here", nil
}

func (r RpmOstreeClientMock) GetBootedDeployment() (*RpmOstreeDeployment, error) {
	return &RpmOstreeDeployment{}, nil
}

func (r RpmOstreeClientMock) GetKernelArgs() ([]string, error) {
	return r.ExpectKernelArgs, nil
}

func (r RpmOstreeClientMock) SetKernelArgs(args []KernelArgument) (string, error) {
	return "", nil
}

func (r RpmOstreeClientMock) RemovePendingDeployment() error {
	return nil
}

func (r RpmOstreeClientMock) RunRpmOstree(a string, b ...string) ([]byte, error) {
	return r.runRpmOstreeFunc(a, b...)
}

// TestValidateRPMOstreeCmds ensures that supported 'rpm-ostree' commands
// are properly validated.
func TestValidateRPMOstreeCmds(t *testing.T) {
	tests := []struct {
		wantErr bool
		noun    string
		args    []string
	}{
		// kargs
		{
			wantErr: false,
			noun:    "kargs",
			args:    nil,
		},
		{
			wantErr: true,
			noun:    "kargs",
			args:    []string{"this will error"},
		},
		{
			wantErr: false,
			noun:    "kargs",
			args:    []string{"--append=foo"},
		},
		{
			wantErr: false,
			noun:    "kargs",
			args:    []string{"--append=foo", "--delete=bob"},
		},
		{
			wantErr: true,
			noun:    "kargs",
			args:    []string{"--append=foo", "--delete=bob", "--replace=alice"},
		},

		// Test argless commands
		{
			wantErr: false,
			noun:    "rollback",
			args:    []string{},
		},
		{
			wantErr: true,
			noun:    "rollback",
			args:    []string{"no-args allowed"},
		},
		{
			wantErr: false,
			noun:    "cancel",
			args:    []string{},
		},
		{
			wantErr: true,
			noun:    "cancel",
			args:    []string{"no-args allowed"},
		},

		{
			wantErr: false,
			noun:    "override",
			args:    []string{"--any args go", "srly"},
		},
		{
			wantErr: true,
			noun:    "override",
			args:    []string{},
		},
		{
			wantErr: false,
			noun:    "uninstall",
			args:    []string{"--uninstall", "the", "world"},
		},
		{
			wantErr: true,
			noun:    "uninstall",
			args:    []string{},
		},
		{
			wantErr: false,
			noun:    "install",
			args:    []string{"all", "the", "pkgs"},
		},
		{
			wantErr: true,
			noun:    "install",
			args:    []string{},
		},

		// cleanup
		{
			wantErr: true,
			noun:    "cleanup",
			args:    []string{},
		},
		{
			wantErr: true,
			noun:    "cleanup",
			args:    []string{"-P"},
		},
		{
			wantErr: false,
			noun:    "cleanup",
			args:    []string{"-p"},
		},

		// rebase
		{
			wantErr: false,
			noun:    "rebase",
			args:    []string{"--experimental"},
		},
		{
			wantErr: true,
			noun:    "rebase",
			args:    []string{},
		},

		// status
		{
			wantErr: false,
			noun:    "status",
			args:    []string{"--json"},
		},
		{
			wantErr: false,
			noun:    "status",
			args:    []string{},
		},
		{
			wantErr: true,
			noun:    "status",
			args:    []string{"--JSON"},
		},
		{
			wantErr: false,
			noun:    "status",
			args:    []string{"--peer"},
		},
		{
			wantErr: false,
			noun:    "status",
			args:    []string{"--peer", "--json"},
		},
		{
			wantErr: false,
			noun:    "status",
			args:    []string{},
		},
		{
			wantErr: true,
			noun:    "status",
			args:    []string{"--PEER"},
		},

		// Error states
		{
			wantErr: true,
			noun:    "",
			args:    []string{},
		},
		{
			wantErr: true,
			noun:    "YOLO",
			args:    []string{},
		},
		{
			wantErr: true,
			noun:    "",
			args:    []string{"oof", "this", "is", "impossible"},
		},
	}
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			err := validateRpmOstreeCommand(test.noun, test.args...)
			if (err != nil && !test.wantErr) || (err == nil && test.wantErr) {
				t.Errorf("validating 'rpm-ostree %s %v':\nwant error: %v\n       got: %v",
					test.noun, test.args, test.wantErr, err)
			}
		})
	}
}
