package imagepruner

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/containers/image/v5/docker"
	"github.com/docker/distribution/registry/api/errcode"
	errcodev2 "github.com/docker/distribution/registry/api/v2"
)

// Determines if the returned error message during image deletion can be
// tolerated. Generally speaking, an error is tolerated under the following
// conditions:
//
// - The image cannot be located, which means that it is gone.
// - The provided credentials do not have deletion permissions.
//
// Ultimately, it is up to the caller to determine what to do with the error.
func IsTolerableDeleteErr(err error) bool {
	if err == nil {
		return false
	}

	// Any errors related to the actual image registry query are wrapped in an
	// ErrImage instance. This allows us to easily identify intolerable errors
	// such as not being able to write the authfile or certs, etc.
	var errImage *ErrImage
	if !errors.As(err, &errImage) {
		return false
	}

	if isTolerableErrorCode(err) {
		return true
	}

	if isTolerableUnexpectedHTTPStatusError(err) {
		return true
	}

	if isMaskedHTTP404(err) {
		return true
	}

	return false
}

// The way that the containers/image code handles an HTTP 404 leaves a lot to
// be desired since it does not return an ErrorCode that can be interrogated
// further. Instead, it returns a hard-coded error string with no other
// context. See:
// https://github.com/containers/image/blob/43d7bae0f71ce2fe5da6b561ee6d2716ac97cb3f/docker/docker_image_src.go#L697-L698
func isMaskedHTTP404(err error) bool {
	if err == nil {
		return false
	}

	return strings.Contains(err.Error(), "Image may not exist or is not stored with a v2 Schema in a v2 registry")
}

func isTolerableErrorCode(err error) bool {
	if err == nil {
		return false
	}

	var errCode errcode.Error
	if !errors.As(err, &errCode) {
		return false
	}

	code := errCode.ErrorCode()

	if code == errcodev2.ErrorCodeManifestUnknown {
		return true
	}

	// Quay.io returns this code whenever one is not authorized to delete an image.
	if code == errcode.ErrorCodeUnauthorized {
		return true
	}

	// Docker.io returns this code whenever one is not authorized to delete an image.
	if code == errcode.ErrorCodeDenied {
		return true
	}

	// Quay.io returns an HTTP 500 if an image was recently deleted and the
	// garbage collection has not run yet.
	if isQuayErrorCode(errCode) {
		return true
	}

	return false
}

// With Quay.io, if an image has been deleted but the garbage collection has
// not run yet, Quay will return an HTTP 500 along with the following error
// message:
//
// "Error: reading manifest latest in registry.hostname.com/org/repo: unknown: Tag latest was deleted or has expired. To pull, revive via time machine"
//
// In this situation, we should assume that the image has been deleted already.
func isQuayErrorCode(errCode errcode.Error) bool {
	if errCode.ErrorCode() != errcode.ErrorCodeUnknown {
		return false
	}

	desc := errCode.ErrorCode().Descriptor()
	if desc.HTTPStatusCode != http.StatusInternalServerError {
		return false
	}

	if strings.Contains(errCode.Message, "deleted") {
		return true
	}

	if strings.Contains(errCode.Message, "expired") {
		return true
	}

	return false
}

func isTolerableUnexpectedHTTPStatusError(err error) bool {
	if err == nil {
		return false
	}

	var unexpectedHTTPErr docker.UnexpectedHTTPStatusError
	if !errors.As(err, &unexpectedHTTPErr) {
		return false
	}

	if unexpectedHTTPErr.StatusCode == http.StatusUnauthorized {
		return true
	}

	if unexpectedHTTPErr.StatusCode == http.StatusForbidden {
		return true
	}

	return false
}

// Holds and wraps an error related to a specific image.
type ErrImage struct {
	msg string
	img string
	err error
}

// Constructs a new ErrImage instance.
func newErrImageWithMessage(msg, img string, err error) error {
	return &ErrImage{msg: msg, img: img, err: err}
}

func newErrImage(img string, err error) error {
	return &ErrImage{img: img, err: err}
}

// Returns the image pullspec that caused the error.
func (e *ErrImage) Image() string {
	return e.img
}

// Implements the error interface.
func (e *ErrImage) Error() string {
	if e.msg != "" && e.img != "" {
		// If both the message and image are not empty, include both.
		return fmt.Sprintf("%s: image %q: %s", e.msg, e.img, e.err.Error())
	}

	if e.msg == "" && e.img != "" {
		// If the message is empty and the image is not, only include the image.
		return fmt.Sprintf("image %q: %s", e.img, e.err.Error())
	}

	// If neither the message nor the image is populated, just return the error
	// string as-is.
	return e.err.Error()
}

// Implements the Unwrap interface to allow the nested error to be surfaced.
func (e *ErrImage) Unwrap() error {
	return e.err
}
