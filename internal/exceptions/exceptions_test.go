package exceptions

import (
	"errors"
	"io"
	"net"
	"testing"
)

// TestFormatsAndUnwrap checks the message formats and that wrapped errors stay matchable.
func TestFormatsAndUnwrap(t *testing.T) {
	sentinel := errors.New("sentinel")
	tests := []struct {
		name    string
		err     error
		message string
		is      error
	}{
		{"new", New("a ", 1, " b ", uint16(2)), "a 1 b 2", nil},
		{"cause", Cause(sentinel, "read ", 3), "read 3: sentinel", sentinel},
		{"extend", Extend(sentinel, "detail"), "sentinel: detail", sentinel},
		{"cause1", Cause1(sentinel, io.EOF), "sentinel: EOF", io.EOF},
		{"cause1 sentinel", Cause1(sentinel, io.EOF), "sentinel: EOF", sentinel},
	}
	for _, test := range tests {
		if test.err.Error() != test.message {
			t.Errorf("%s: message %q, want %q", test.name, test.err.Error(), test.message)
		}
		if test.is != nil && !errors.Is(test.err, test.is) {
			t.Errorf("%s: errors.Is failed", test.name)
		}
	}
}

// TestErrorsAndClosed checks Errors' nil handling and the closed classification.
func TestErrorsAndClosed(t *testing.T) {
	if Errors(nil, nil) != nil {
		t.Fatal("Errors of nils is not nil")
	}
	if err := Errors(nil, io.EOF); err != io.EOF {
		t.Fatalf("Errors of one = %v", err)
	}
	joined := Errors(io.EOF, net.ErrClosed)
	if !errors.Is(joined, io.EOF) || !errors.Is(joined, net.ErrClosed) {
		t.Fatal("joined error lost a member")
	}
	if !IsClosed(Cause(net.ErrClosed, "write")) || IsClosed(errors.New("other")) {
		t.Fatal("IsClosed misclassified")
	}
}
