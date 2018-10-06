package daemon

import (
	"fmt"
	"testing"
)

// ProcessClientStub provides a testing interface for operations that run system commands
type ProcessClientStub struct {
	RunReturns            []error
	RunGetOutByteReturns  [][]byte
	RunGetOutErrorReturns []error
}

// Run implements the ProcessClient Run command but returns the 0th element from
// the structures RunReturns field.
func (p *ProcessClientStub) Run(command string, args ...string) error {
	err := p.RunReturns[0]
	p.RunReturns = p.RunReturns[1:]
	return err
}

// RunGetOut implements the ProcessClient RunGetOut command but returns the 0th
// elements from the structures RunGetOutByteReturns and RunGetOutErrorReturns fields.
func (p *ProcessClientStub) RunGetOut(command string, args ...string) ([]byte, error) {
	err := p.RunGetOutErrorReturns[0]
	b := p.RunGetOutByteReturns[0]
	p.RunGetOutErrorReturns = p.RunGetOutErrorReturns[1:]
	p.RunGetOutByteReturns = p.RunGetOutByteReturns[1:]
	return b, err
}

func TestRunPivot(t *testing.T) {
	pc := ProcessClientStub{
		RunReturns: []error{nil, fmt.Errorf("expected failure")},
	}
	client := NewNodeUpdaterClient(&pc)

	err := client.RunPivot("")
	if err != nil {
		t.Errorf("Expected no errors, got: %s", err)
	}

	err = client.RunPivot("")
	if err == nil {
		t.Errorf("Expected errors. Got none.")
	}
}

// TODO
/*
func TestGetBootedDeployment(t *testing.T) {
	pc := ProcessClientStub{
		RunGetOutErrorReturns: []error{nil},
		RunGetOutByteReturns: // TODO: We would load a valid rpm-ostree status --json output
	}
	client := NewNodeUpdaterClient(&pc)

	rpmOstreeDeployment, err := client.GetBootedDeployment("/tmp")
	if err != nil {
		t.Errorf("Expected no errors, got: %s", err)
	}
}
*/
