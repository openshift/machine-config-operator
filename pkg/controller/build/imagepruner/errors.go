package imagepruner

import (
	"errors"
	"net/http"
	"strings"

	"github.com/containers/image/v5/docker"
	"github.com/docker/distribution/registry/api/errcode"
	errcodev2 "github.com/docker/distribution/registry/api/v2"
	"github.com/openshift/machine-config-operator/pkg/controller/image"
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

	// Any errors related to the actual image registry query are wrapped in an
	// ErrImage instance. This allows us to easily identify intolerable errors
	// such as not being able to write the authfile or certs, etc.
	var errImage *image.ErrImage
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

// isTolerableErrorCode checks if the error code from the registry is tolerable for deletion.
// This includes cases where the manifest is unknown, or authorization is denied.
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

// isTolerableUnexpectedHTTPStatusError checks if an unexpected HTTP status error
// is tolerable, specifically if it's an Unauthorized (401) or Forbidden (403) status.
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
