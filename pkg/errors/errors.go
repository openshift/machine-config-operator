package errors

import (
	"encoding/json"
	"fmt"
)

// Holds the optional instructions and documentation URL.
type Resolution struct {
	Instructions string `json:"instructions,omitempty"`
	DocURL       string `json:"docURL,omitempty"`
}

// Holds the error description, numerical error code, and what (if any)
// resolution is available.
type ErrorCode struct {
	Description string      `json:"description,omitempty"`
	Resolution  *Resolution `json:"resolution,omitempty"`
	prefix      string
	code        int
}

// We want to convert this to a string, but don't want it to implement the
// Stringer interface right now because spew.Dump() (and possibly fmt.Fprintf)
// defaults to using the Stringer interface.
func (e *ErrorCode) ToString() string {
	if e.Description != "" {
		// If we have a description, include it.
		return fmt.Sprintf("%s - %s", e.getHumanErrorCode(), e.Description)
	}

	// Otherwise, just use the human-readable error code.
	return e.getHumanErrorCode()
}

// Concatenates the error code and prefix together.
// If prefix is "MCOError" and error code is 1000, returns "MCOError1000".
func (e *ErrorCode) getHumanErrorCode() string {
	return fmt.Sprintf("%s%d", e.prefix, e.code)
}

// Represents the library end-user ingestion format.
type ErrorCodes map[int]ErrorCode

// Holds the ingested error codes.
type ErrorIndex struct {
	errCodes map[int]ErrorCode
	prefix   string
}

// Holds a JSON representation of our error index.
// This is not strictly required. It might help maintainability.
type jsonErrorIndex struct {
	ErrorCodes map[int]ErrorCode `json:"error_codes"`
	Prefix     string            `json:"error_code_prefix"`
}

// Ingests the provided error code objects, populating the prefix for each one
// as well as populating the code number from the map key. This frees the
// end-user of this library from having to manage those on their own.
// Furthermore, this allows the developer to quickly and easily wrap a provided
// error with the additional context provided by the ErrorCode object.
func NewErrorIndex(prefix string, codes ErrorCodes) *ErrorIndex {
	out := &ErrorIndex{
		errCodes: map[int]ErrorCode{},
		prefix:   prefix,
	}

	for codeNum := range codes {
		// The user-provided ErrorCodes do not have the prefix and numerical error
		// codes contained therein. So, we populate those based upon the provided
		// prefix value as well as the numerical error code the user-provided
		// ErrorCodes map uses. It is the end-user of this library's responsibility
		// to ensure that the numerical error codes are unique.
		code := codes[codeNum]
		code.prefix = prefix
		code.code = codeNum
		out.errCodes[codeNum] = code
	}

	return out
}

// Looks up an ErrorCode and wraps it in a CodedError.
func (e *ErrorIndex) Wrap(code int, err error) error {
	return &CodedError{
		errCode: e.errCodes[code],
		err:     err,
	}
}

// This is not necessary, but could be useful in the future for the following
// reasons:
// 1. Surfacing this error to a cluster-admin via the GUI could be made easier.
// 2. Auto-generate documentation about the error codes using a binary that
// ingests them all and returns them back.
func (e *ErrorIndex) MarshalJSON() ([]byte, error) {
	return json.Marshal(&jsonErrorIndex{
		Prefix:     e.prefix,
		ErrorCodes: e.errCodes,
	})
}

// This is also not necessary, but could be useful in the future if we wanted
// to decouple the error code definitions from our code.
func (e *ErrorIndex) UnmarshalJSON(jsonBytes []byte) error {
	jei := &jsonErrorIndex{}

	if err := json.Unmarshal(jsonBytes, jei); err != nil {
		return fmt.Errorf("could not parse error index: %w", err)
	}

	// Use the constructor func to complete initialization
	tmp := NewErrorIndex(jei.Prefix, jei.ErrorCodes)
	e.prefix = tmp.prefix
	e.errCodes = tmp.errCodes

	return nil
}

// Holds our custom error type and implements the error interface.
type CodedError struct {
	errCode ErrorCode
	err     error
}

// Implements the unwrap interface so the original error can be reached, if necessary.
func (c *CodedError) Unwrap() error {
	return c.err
}

// Implements the error interface
func (c *CodedError) Error() string {
	// For now, we just inject the prefix, error code, description, and the
	// original error into the error string. For brevity, the Resolution, if any,
	// will not be output as part of the error string; however that information
	// is still contained within the CodedError object.
	//
	// To retrieve the Resolution, one can do a type assertion and call the
	// ErrorCode() method below.
	return fmt.Sprintf("%s: %s", c.errCode.ToString(), c.err)
}

// Include a way to get the ErrorCode object back.
func (c *CodedError) ErrorCode() ErrorCode {
	return c.errCode
}
