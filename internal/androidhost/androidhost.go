// Package androidhost bridges the Android OS services that Kyokai's controller
// uses: the Keystore-wrapped master key, the session foreground service and
// wake lock, MediaCodec decoder capabilities and Opus audio playback,
// clipboard image timestamps, and foldable hinge posture.
package androidhost

import "io"

// RawTouch describes a multi-touch or pointer action captured on Android's
// SurfaceView in DIPs (matching MyGo's window coordinates).
type RawTouch struct {
	// Phase is "down", "move", "up" or "cancel".
	Phase string
	// Kind is "touch", "mouse" or "pen".
	Kind string
	// Zone is 1 for the main remote-screen stage or 2 for the split-keyboard
	// center touchpad.
	Zone int
	// ID is the pointer's stable ID from ACTION_DOWN to ACTION_UP.
	ID int
	// X and Y are window coordinates in DIPs.
	X, Y float64
	// Buttons is MotionEvent.buttonState for mice/styluses.
	Buttons int
}

// Host provides Android platform services to the controller GUI and engine.
//
//mygo:native
type Host interface {
	// Info returns the Android build, paths and model description.
	Info() (DeviceInfo, error)
	// MasterKey returns or creates the 32-byte master key (base64) wrapped
	// under Android Keystore alias "kyokai-master", or "" when unavailable.
	MasterKey() string
	// Decoders enumerates the H.264 and HEVC MediaCodec decoders available
	// on this device, hardware first.
	Decoders() []DecoderCap
	// SetSessions starts, updates or stops (when text is "") the ongoing
	// foreground service and partial wake lock that keep open sessions
	// alive in the background.
	SetSessions(text string)
	// ClipboardImage reads an image from the Android clipboard when its
	// URI+timestamp mark differs from unless, returning the mark and PNG
	// bytes (empty PNG when unchanged or not an image).
	ClipboardImage(unless string) ClipImage
	// SetClipboardImage writes PNG bytes to the Android clipboard and
	// returns its URI+timestamp mark so it is not echoed back.
	SetClipboardImage(png []byte) string
	// WatchPosture observes foldable hinge angle and Jetpack Window
	// half-opened folding features until the returned closer is closed.
	WatchPosture(onChange func(Posture)) io.Closer
	// SetTouchRegion installs an intercepting touch listener when capture is
	// true on up to two trackpad/screen areas (r1 for the stage, r2 for an
	// optional split-keyboard center touchpad, in window DIPs, excluding up to
	// two overlay rectangles on r1) so 1-, 2- and 3-finger gestures and mouse
	// drags reach Go directly. Pass capture = false when leaving the viewer.
	SetTouchRegion(capture bool, r1L, r1T, r1R, r1B, r2L, r2T, r2R, r2B, ex1L, ex1T, ex1R, ex1B, ex2L, ex2T, ex2R, ex2B float64, onTouch func(RawTouch))
	// OpenSound opens a low-latency Opus 48 kHz stereo MediaCodec +
	// AudioTrack player for a host's sound stream.
	OpenSound() (SoundPlayer, error)
}

// SoundPlayer decodes and plays 10 ms 48 kHz stereo Opus packets.
type SoundPlayer interface {
	io.Closer
	// Feed queues one Opus packet; packets older than 200 ms are dropped.
	Feed(packet []byte)
}

// DeviceInfo describes the Android device and app directories.
type DeviceInfo struct {
	AndroidVersion string
	SDK            int
	Manufacturer   string
	Model          string
	ABIs           string
	FilesDir       string
	NoBackupDir    string
	CacheDir       string
	NativeLibDir   string
	HasHingeSensor bool
}

// DecoderCap describes the preferred decoder for a video codec ("hevc" or "h264").
type DecoderCap struct {
	Codec     string
	Name      string
	Hardware  bool
	MaxWidth  int
	MaxHeight int
}

// ClipImage holds the Android clipboard's current image mark and PNG bytes.
type ClipImage struct {
	Mark string
	PNG  []byte
}

// Posture reports a foldable device's hinge state for laptop mode.
type Posture struct {
	// HalfOpened is true when Jetpack Window reports a HALF_OPENED
	// horizontal fold or the hinge sensor is between 30° and 150°.
	HalfOpened bool
	// HingeTop, HingeBottom are the fold's vertical bounds in window pixels
	// (0 when reported by sensor angle alone, in which case split at 50%).
	HingeTop    int
	HingeBottom int
	// Angle is the raw hinge angle in degrees (-1 when unavailable).
	Angle float64
}
