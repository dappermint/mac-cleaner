package exitcode

import (
	"errors"
	"fmt"
)

const (
	Failure   = 1
	Refused   = 3
	NeedsRoot = 4
)

type Error struct {
	code int
	err  error
}

func (e *Error) Error() string { return e.err.Error() }
func (e *Error) Unwrap() error { return e.err }
func (e *Error) Code() int     { return e.code }

func With(code int, err error) error {
	if err == nil {
		return nil
	}
	return &Error{code: code, err: err}
}

func Errorf(code int, format string, arguments ...any) error {
	return With(code, fmt.Errorf(format, arguments...))
}

func Code(err error) int {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return Failure
}
