//go:build !android || !cgo

package androidhost

import "errors"

// VideoDecoder is a stub on non-Android platforms (desktops use kyokai-core's
// own decoders).
type VideoDecoder struct{}

// OpenVideoDecoder returns errors.ErrUnsupported off Android.
func OpenVideoDecoder(codec, codecName string, hardware bool, width, height int) (*VideoDecoder, error) {
	return nil, errors.ErrUnsupported
}

// Info returns empty info off Android.
func (d *VideoDecoder) Info() (string, bool) { return "", false }

// Decode returns errors.ErrUnsupported off Android.
func (d *VideoDecoder) Decode(frame []byte, key bool, dst []byte, skipCopy bool) (int, int, bool, error) {
	return 0, 0, false, errors.ErrUnsupported
}

// Close is a no-op off Android.
func (d *VideoDecoder) Close() {}
