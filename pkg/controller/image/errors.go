package image

import "fmt"

// ErrImage holds and wraps an error related to a specific image.
type ErrImage struct {
	msg string
	img string
	err error
}

// newErrImageWithMessage constructs a new ErrImage instance with a custom message,
// image pullspec, and wrapped error.
func NewErrImageWithMessage(msg, img string, err error) error {
	return &ErrImage{msg: msg, img: img, err: err}
}

// newErrImage constructs a new ErrImage instance with an image pullspec and
// wrapped error, without a custom message.
func NewErrImage(img string, err error) error {
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
