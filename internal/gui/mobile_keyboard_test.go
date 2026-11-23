package gui

import (
	"testing"
	"time"
)

func TestMobileKeyboardLayersAndKeysyms(t *testing.T) {
	if got := KeysymFor(0x02, false); got != '1' {
		t.Fatalf("KeysymFor(1, unshifted) = %#x, want '1'", got)
	}
	if got := KeysymFor(0x02, true); got != '!' {
		t.Fatalf("KeysymFor(1, shifted) = %#x, want '!'", got)
	}
	if got := KeysymFor(scanF1, false); got != 0xFFBE {
		t.Fatalf("KeysymFor(F1) = %#x, want 0xFFBE", got)
	}
	if got := KeysymFor(scanF12, false); got != 0xFFC9 {
		t.Fatalf("KeysymFor(F12) = %#x, want 0xFFC9", got)
	}

	kb := NewMobileKeyboard(nil, 1.6)
	if got := kb.shownChar("1", "windows"); got != "1" {
		t.Fatalf("unshifted 1 = %q", got)
	}
	kb.latch[kbShift] = kbOnce
	if got := kb.shownChar("1", "windows"); got != "!" {
		t.Fatalf("shifted 1 = %q, want !", got)
	}
	if got := kb.shownChar("q", "macos"); got != "Q" {
		t.Fatalf("shifted q = %q, want Q", got)
	}

	// Mac Option layer: Opt+q -> œ, Opt+Shift+q -> Œ, Cmd+Opt+q -> q (shortcut).
	kb.latch[kbShift] = kbOff
	kb.latch[kbAlt] = kbOnce
	if got := kb.shownChar("q", "macos"); got != "œ" {
		t.Fatalf("Mac Opt+q = %q, want œ", got)
	}
	kb.latch[kbShift] = kbOnce
	if got := kb.shownChar("q", "macos"); got != "Œ" {
		t.Fatalf("Mac Opt+Shift+q = %q, want Œ", got)
	}
	kb.latch[kbWin] = kbOnce
	if got := kb.shownChar("q", "macos"); got != "Q" {
		t.Fatalf("Mac Cmd+Opt+Shift+q = %q, want Q", got)
	}

	// Modifier latch cleared after typing one character.
	kb.ReleaseAll(func(uint32, bool, bool) {})
	kb.latch[kbCtrl] = kbOnce
	var events [][2]uint32
	kb.tapCode(usKeyMap["c"].code, false, func(code uint32, down, _ bool) {
		var d uint32
		if down {
			d = 1
		}
		events = append(events, [2]uint32{code, d})
	})
	if kb.latch[kbCtrl] != kbOff {
		t.Fatalf("expected once-latched Ctrl to clear after tapCode")
	}
	want := [][2]uint32{{scanLCtrl, 1}, {usKeyMap["c"].code, 1}, {usKeyMap["c"].code, 0}, {scanLCtrl, 0}}
	if len(events) != len(want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event[%d]=%v, want %v", i, events[i], want[i])
		}
	}
}

func TestNormalizeHostOS(t *testing.T) {
	if got := NormalizeHostOS("", "", true); got != "windows" {
		t.Fatalf("nanokvm auto OS = %q, want windows", got)
	}
	if got := NormalizeHostOS("", "", false); got != "linux" {
		t.Fatalf("empty peer OS = %q, want linux", got)
	}
	if got := NormalizeHostOS("", "macOS 15", false); got != "macos" {
		t.Fatalf("macOS peer = %q, want macos", got)
	}
	if got := NormalizeHostOS("macos", "windows", true); got != "macos" {
		t.Fatalf("override macos = %q, want macos", got)
	}
}

func TestMobileKeyboardWinTapAndStandalone(t *testing.T) {
	kb := NewMobileKeyboard(nil, 1.6)
	var sent [][2]uint32
	opts := MobileKeyboardOptions{
		HostOS: "windows",
		Send: func(code uint32, down, _ bool) {
			var d uint32
			if down {
				d = 1
			}
			sent = append(sent, [2]uint32{code, d})
		},
	}
	winSpec := kMod("Win", kbWin, 10)

	// First tap latches Win once so Win+D works single-handed.
	kb.activate(winSpec, opts)
	if kb.latch[kbWin] != kbOnce {
		t.Fatalf("1st Win tap latch = %v, want kbOnce", kb.latch[kbWin])
	}
	if len(sent) != 0 {
		t.Fatalf("1st Win tap sent %v, want 0", sent)
	}

	// Deliberate second tap (>350ms later) sends standalone Win press+release and clears latch.
	kb.lastTap[kbWin] = time.Now().Add(-400 * time.Millisecond)
	kb.activate(winSpec, opts)
	if kb.latch[kbWin] != kbOff {
		t.Fatalf("2nd Win tap latch = %v, want kbOff", kb.latch[kbWin])
	}
	if len(sent) != 2 || sent[0] != [2]uint32{scanLWin, 1} || sent[1] != [2]uint32{scanLWin, 0} {
		t.Fatalf("standalone Win events = %v, want LWin down+up", sent)
	}

	// Rapid double tap (<350ms) locks Win.
	sent = nil
	kb.lastTap[kbWin] = time.Time{}
	kb.activate(winSpec, opts)
	kb.activate(winSpec, opts)
	if kb.latch[kbWin] != kbLocked {
		t.Fatalf("rapid double Win tap latch = %v, want kbLocked", kb.latch[kbWin])
	}
}
