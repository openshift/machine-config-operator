package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"

	"github.com/davecgh/go-spew/spew"
	"github.com/ghodss/yaml"
	mcoErrors "github.com/openshift/machine-config-operator/pkg/errors"
)

func doSomethingThatErrors() error {
	jsonBytes := []byte(`oiajdsfiopadsfj`)
	dst := map[string]interface{}{}

	// In this case, I try to unmarshal invalid JSON.
	if err := json.Unmarshal(jsonBytes, &dst); err != nil {
		return errorIndex.Wrap(MCOError1000, err)
	}

	return nil
}

func doSomethingElseThatErrors() error {
	// In this case, I try to read a non-existant file.
	if _, err := ioutil.ReadFile("/i/dont/exist"); err != nil {
		return errorIndex.Wrap(MCOError1001, err)
	}

	return nil
}

func printCodedErrorInfo(codedErr *mcoErrors.CodedError) {
	fmt.Println("Error String:", codedErr.Error())
	spew.Dump(codedErr.ErrorCode())
	spew.Dump(codedErr.Unwrap())
	fmt.Printf("\n")
}

func getDetailedErrorInfo(err error) {
	// This shows how we can determine if an error is one of our CodedError
	// objects. When it is, we can access the additional methods on the object.
	var cErr *mcoErrors.CodedError = nil
	if errors.As(err, &cErr) {
		printCodedErrorInfo(cErr)
	} else {
		fmt.Println("not a CodedError, ignoring")
	}
}

func roundTripMarshalling() {
	// We can export the error index to a YAML file so that they can be ingested
	// by another tool, perhaps one that can auto-generate docs from it.
	errIndexYAML, err := yaml.Marshal(errorIndex)
	if err != nil {
		panic(err)
	}

	errIndexFromYAML := &mcoErrors.ErrorIndex{}
	if err := yaml.Unmarshal(errIndexYAML, &errIndexFromYAML); err != nil {
		panic(err)
	}
}

func run() {
	getDetailedErrorInfo(doSomethingThatErrors())
	getDetailedErrorInfo(doSomethingElseThatErrors())
	getDetailedErrorInfo(errorIndex.Wrap(MCOError1002, fmt.Errorf("a description-less error")))
	getDetailedErrorInfo(errorIndex.Wrap(MCOError1003, fmt.Errorf("a resolution-less error")))
	getDetailedErrorInfo(errorIndex.Wrap(MCOError1004, fmt.Errorf("a description-less and resolution-less error")))
}

func main() {
	fmt.Println("Running round-trip marshalling")
	roundTripMarshalling()

	fmt.Println("Getting errors from hard-coded error index")
	run()

	// Swap the hard-coded error index with the error index we loaded from the YAML file.
	errorIndex = errorIndexFromYAML

	fmt.Println("Getting errors from YAML error index")
	run()
}
