package gui

import (
	"errors"
	"image"
	"testing"

	"github.com/YuanZhe-99/Kyokai/internal/core"
	L "github.com/YuanZhe-99/Kyokai/locales"
)

// TestGuardEndsOnlyThePane is the regression test for a server crashing the
// app: a panic in a client's dial or session becomes errUnreadable, which
// ends its tab with a note.
func TestGuardEndsOnlyThePane(t *testing.T) {
	err := guard(func() error {
		var pix []byte
		_ = pix[7] // what an unchecked rectangle did in the VNC decoder
		return nil
	})
	if !errors.Is(err, errUnreadable) {
		t.Fatalf("guard after a panic = %v, want errUnreadable", err)
	}
	want := errors.New("plain")
	if err := guard(func() error { return want }); err != want {
		t.Errorf("guard passed %v, want %v", err, want)
	}
	s := &serviceTab{service: serviceVNC}
	s.settle(nil, err, nil)
	if s.phase != svcEnded || s.note != L.ServiceUnreadable() {
		t.Errorf("settle(errUnreadable): phase %q, note %q", s.phase, s.note)
	}
}

// TestNoSecureSignInAsks checks that a password that met a server offering
// no secure sign-in brings the form back with a note naming the method to
// choose: "On the remote screen" for Remote Desktop, "Password only" for VNC.
func TestNoSecureSignInAsks(t *testing.T) {
	for _, c := range []struct{ service, note string }{
		{serviceRDP, L.ServiceRDPNoNLA(L.ServiceMethodScreen())},
		{serviceVNC, L.ServiceVNCWeakSignIn(L.ServiceMethodPasswordOnly())},
	} {
		s := &serviceTab{service: c.service}
		s.settle(nil, errNoSecureSignIn, nil)
		if s.phase != svcForm || s.note != c.note {
			t.Errorf("%s: phase %q, note %q", c.service, s.phase, s.note)
		}
	}
}

// TestScreenRotatesPictures checks the picture rotation that keeps the
// main thread off the lock: an update converts BGRA into the picture
// neither shown nor waiting, and never writes the one the bitmap shows.
func TestScreenRotatesPictures(t *testing.T) {
	sc := newScreen(&serviceTab{})
	fb := sc.resize(2, 1)
	copy(fb.Pix, []byte{1, 2, 3, 0, 4, 5, 6, 0})
	sc.updated(image.Rect(0, 0, 2, 1))
	if sc.ready < 0 {
		t.Fatal("no picture waiting after an update")
	}
	if got := sc.pics[sc.ready].Pix; string(got) != string([]byte{3, 2, 1, 0xff, 6, 5, 4, 0xff}) {
		t.Errorf("converted %v", got)
	}
	// As present would: the waiting picture becomes the bitmap's.
	sc.shown, sc.ready = sc.ready, -1
	shown := sc.pics[sc.shown]
	before := string(shown.Pix)
	for i := range 4 {
		fb.Pix[0] = byte(10 + i)
		sc.updated(image.Rect(0, 0, 1, 1))
		if sc.ready == sc.shown {
			t.Fatalf("update %d wrote the shown picture", i)
		}
		if got := sc.pics[sc.ready].Pix[2]; got != byte(10+i) {
			t.Errorf("update %d: blue %d", i, got)
		}
	}
	if string(shown.Pix) != before {
		t.Error("the shown picture changed")
	}
}

// TestEndGrabWithoutGrab is the regression test for the first endGrab
// loading the core library on the main thread: with nothing grabbed it
// returns before asking for the library.
func TestEndGrabWithoutGrab(t *testing.T) {
	loaded := false
	saved := coreLib
	coreLib = func() (*core.Lib, error) { loaded = true; return nil, errors.New("not loaded in tests") }
	defer func() { coreLib = saved }()
	(&serviceTab{}).endGrab()
	if loaded {
		t.Error("endGrab loaded the core library with nothing grabbed")
	}
}
