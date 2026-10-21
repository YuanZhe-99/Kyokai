// Package exceptions builds and classifies the errors of the vendored RDP and RFB clients.
//
// It replaces the small part of github.com/sagernet/sing/common/exceptions those clients used, with
// the same message formats, so call sites keep the conventional import alias E.
package exceptions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
)

// New returns an error whose message is the arguments concatenated without separators.
func New(message ...any) error {
	return errors.New(text(message))
}

// Cause wraps cause under a message: "message: cause".
// Notes: errors.Is and errors.As see cause; cause must not be nil.
func Cause(cause error, message ...any) error {
	if cause == nil {
		panic("exceptions: Cause on a nil error")
	}
	return &causeError{message: text(message), cause: cause}
}

// Cause1 joins a sentinel error and its cause: "err: cause".
// Notes: errors.Is matches both err and cause; cause must not be nil.
func Cause1(err error, cause error) error {
	if cause == nil {
		panic("exceptions: Cause1 on a nil error")
	}
	return &causeError1{err: err, cause: cause}
}

// Extend appends detail to an error: "cause: message".
// Notes: errors.Is and errors.As see cause; cause must not be nil.
func Extend(cause error, message ...any) error {
	if cause == nil {
		panic("exceptions: Extend on a nil error")
	}
	return &causeError{message: text(message), cause: cause, extend: true}
}

// Errors joins the non-nil errors.
// Returns: nil when none are set, the error itself when one is, otherwise an errors.Join of them.
func Errors(errs ...error) error {
	var kept []error
	for _, err := range errs {
		if err != nil {
			kept = append(kept, err)
		}
	}
	switch len(kept) {
	case 0:
		return nil
	case 1:
		return kept[0]
	}
	return errors.Join(kept...)
}

// IsMulti reports whether err matches any of targets under errors.Is.
func IsMulti(err error, targets ...error) bool {
	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// closedErrors are the errors that mean the connection is gone; platform files append to it.
var closedErrors = []error{
	io.EOF,
	net.ErrClosed,
	io.ErrClosedPipe,
	os.ErrClosed,
	syscall.EPIPE,
	syscall.ECONNRESET,
	syscall.ENOTCONN,
}

// IsClosed reports whether err means the connection was closed or reset.
func IsClosed(err error) bool {
	return IsMulti(err, closedErrors...)
}

// IsClosedOrCanceled reports whether err means the connection closed, its context ended, or an
// I/O deadline passed.
func IsClosedOrCanceled(err error) bool {
	if IsClosed(err) || IsMulti(err, context.Canceled, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// text concatenates values as fmt.Sprint renders each one, without separators.
func text(values []any) string {
	var b strings.Builder
	for _, value := range values {
		if value == nil {
			b.WriteString("nil")
			continue
		}
		fmt.Fprint(&b, value)
	}
	return b.String()
}

// causeError is an error with a message in front of (or, extended, after) its cause.
type causeError struct {
	message string
	cause   error
	extend  bool
}

// Error formats the message and the cause in their order.
func (e *causeError) Error() string {
	if e.extend {
		return e.cause.Error() + ": " + e.message
	}
	return e.message + ": " + e.cause.Error()
}

// Unwrap returns the cause.
func (e *causeError) Unwrap() error {
	return e.cause
}

// causeError1 is a sentinel error paired with the error that caused it.
type causeError1 struct {
	err   error
	cause error
}

// Error formats the sentinel and the cause.
func (e *causeError1) Error() string {
	return e.err.Error() + ": " + e.cause.Error()
}

// Unwrap returns the sentinel and the cause.
func (e *causeError1) Unwrap() []error {
	return []error{e.err, e.cause}
}
