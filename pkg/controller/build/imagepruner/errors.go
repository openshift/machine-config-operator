package imagepruner

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/containers/image/v5/docker"
	"github.com/docker/distribution/registry/api/errcode"
	v2 "github.com/docker/distribution/registry/api/v2"
)

// IsTolerableDeleteErr determines if the returned error message during image deletion can be
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

	if IsImageNotFoundErr(err) {
		return true
	}

	if IsAccessDeniedErr(err) {
		return true
	}

	return false
}

// IsImageNotFoundErr determines if the returned error message indicates that
// the image is not found. This assumes that the image registry returns such an
// error code, which is not always the case. Some image registries return an
// unauthorized or forbidden error which this function does not take into
// account. For that, use IsAccessDeniedErr below as an additional check.
func IsImageNotFoundErr(err error) bool {
	if err == nil {
		return false
	}

	if !isErrImage(err) {
		return false
	}

	if isMaskedHTTP404(err) {
		return true
	}

	if isImageNotFoundErrorCode(err) {
		return true
	}

	return false
}

// IsAccessDeniedErr determines if the returned error indicates that the image
// cannot be accessed or the operation cannot be performed due to permissions
// issue. Some image registries use this as a proxy for the image not existing.
func IsAccessDeniedErr(err error) bool {
	if err == nil {
		return false
	}

	if !isErrImage(err) {
		return false
	}

	if isAccessDeniedErrorCode(err) {
		return true
	}

	// Check for ErrUnauthorizedForCredentials directly
	var unauthedForCreds docker.ErrUnauthorizedForCredentials
	if errors.As(err, &unauthedForCreds) {
		return true
	}

	return false
}

// isMaskedHTTP404 checks if the error indicates a masked HTTP 404.
// The containers/image code does not return an ErrorCode that can be interrogated
// further. Instead, it returns a hard-coded error string for a 404.
// See: https://github.com/containers/image/blob/43d7bae0f71ce2fe5da6b561ee6d2716ac97cb3f/docker/docker_image_src.go#L697-L698
func isMaskedHTTP404(err error) bool {
	if err == nil {
		return false
	}

	return strings.Contains(err.Error(), "Image may not exist or is not stored with a v2 Schema in a v2 registry")
}

// isImageNotFoundErrorCode checks if the error is an ErrorCode instance
// indicating that a given manifest is not found or the repo name is unknown.
// This also handles the Quay.io edgecase of an image being deleted and Quay
// returning an HTTP 500 indicating that.
func isImageNotFoundErrorCode(err error) bool {
	if err == nil {
		return false
	}

	if isManifestUnknownError(err) {
		return true
	}

	if isNameUnknownError(err) {
		return true
	}

	var errCode errcode.Error
	if errors.As(err, &errCode) {
		return isQuayErrorCode(errCode)
	}

	return false
}

// Determines if the error is due to the repo name being unknown.
func isNameUnknownError(err error) bool {
	var ec errcode.ErrorCoder
	if errors.As(err, &ec) && ec.ErrorCode() == v2.ErrorCodeNameUnknown {
		return true
	}

	return false
}

// Adapted from: https://github.com/containers/image/blob/52ee4dff559a09ffa45783c50bcb7b3f7faebb04/docker/docker_client.go#L1109-L1133
func isManifestUnknownError(err error) bool {
	// docker/distribution, and as defined in the spec
	var ec errcode.ErrorCoder
	if errors.As(err, &ec) && ec.ErrorCode() == v2.ErrorCodeManifestUnknown {
		return true
	}
	// registry.redhat.io as of October 2022
	var e errcode.Error
	if errors.As(err, &e) && e.ErrorCode() == errcode.ErrorCodeUnknown && e.Message == "Not Found" {
		return true
	}
	// Harbor v2.10.2
	if errors.As(err, &e) && e.ErrorCode() == errcode.ErrorCodeUnknown && strings.Contains(strings.ToLower(e.Message), "not found") {
		return true
	}
	// registry.access.redhat.com as of August 2025
	if errors.As(err, &e) && e.ErrorCode() == v2.ErrorCodeNameUnknown {
		return true
	}

	return false
}

// isAccessDeniedErrorCode checks if the error is an ErrorCode instance and
// then checks the known status codes.
func isAccessDeniedErrorCode(err error) bool {
	if err == nil {
		return false
	}

	var ec errcode.ErrorCoder
	// Quay.io returns this code whenever one is not authorized to delete an image.
	if errors.As(err, &ec) && ec.ErrorCode() == errcode.ErrorCodeUnauthorized {
		return true
	}

	// Docker.io returns this code whenever one is not authorized to delete an image.
	if errors.As(err, &ec) && ec.ErrorCode() == errcode.ErrorCodeDenied {
		return true
	}

	return false
}

// isQuayErrorCode checks for specific Quay.io error scenarios where an HTTP 500
// might indicate that an image has already been deleted but garbage collection
// has not yet run.
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

// ErrImage holds and wraps an error related to a specific image.
type ErrImage struct {
	msg string
	img string
	err error
}

// newErrImageWithMessage constructs a new ErrImage instance with a custom message,
// image pullspec, and wrapped error.
func newErrImageWithMessage(msg, img string, err error) error {
	return &ErrImage{msg: msg, img: img, err: err}
}

// newErrImage constructs a new ErrImage instance with an image pullspec and
// wrapped error, without a custom message.
func newErrImage(img string, err error) error {
	return &ErrImage{img: img, err: err}
}

// Image returns the image pullspec that caused the error.
func (e *ErrImage) Image() string {
	return e.img
}

// Error implements the error interface, providing a formatted error string
// including the message (if present), image (if present), and the wrapped error's string.
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

// Unwrap implements the Unwrap interface, allowing the nested error to be surfaced.
func (e *ErrImage) Unwrap() error {
	return e.err
}

// isErrImage determines whether the given error is an instance of the ErrImage
// type defined above.
func isErrImage(err error) bool {
	if err == nil {
		return false
	}

	// Any errors related to the actual image registry query are wrapped in an
	// ErrImage instance. This allows us to easily identify intolerable errors
	// such as not being able to write the authfile or certs, etc.
	var errImage *ErrImage
	return errors.As(err, &errImage)
}
