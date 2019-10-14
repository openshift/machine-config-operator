package kubeletconfig

type forgetError struct {
	Err error
}

func newForgetError(err error) *forgetError {
	return &forgetError{Err: err}
}

func (e *forgetError) Error() string {
	return e.Err.Error()
}
