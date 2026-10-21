// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/keyboard.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: SendScancode added, import paths.

package rdp

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	keysymVoid  = 0xffffff
	keysymPause = 0xff13

	macKeyCapsLock   = 57
	macKeyISOSection = 10
	macKeyGrave      = 50
)

var shortcutScancodes = []uint16{
	scancodeControl, keyExtended | scancodeControl,
	scancodeAlt, keyExtended | scancodeAlt,
	keyExtended | scancodeLeftWindows, keyExtended | scancodeRightWindows,
}

var modifierScancodes = append([]uint16{scancodeLeftShift, scancodeRightShift}, shortcutScancodes...)

var pauseEvents = []inputEvent{
	scancodeEvent(keyExtended1|scancodeControl, true), scancodeEvent(scancodeNumLock, true),
	scancodeEvent(keyExtended1|scancodeControl, false), scancodeEvent(scancodeNumLock, false),
}

func (c *Client) SendKey(keysym uint32, down bool) error {
	if !c.session.active.Load() {
		return nil
	}
	c.input.access.Lock()
	defer c.input.access.Unlock()
	return c.input.keysym(keysym, down)
}

// SendScancode sends a key press or release by its PC/AT set 1 scan code; extended marks the
// E0-prefixed keys (right Ctrl and Alt, arrows, Insert, Delete, Home, End, Page Up/Down, the
// Windows keys). Added by Kyokai for clients that already hold scan codes.
func (c *Client) SendScancode(code uint8, extended bool, down bool) error {
	if !c.session.active.Load() {
		return nil
	}
	scancode := uint16(code)
	if extended {
		scancode |= keyExtended
	}
	c.input.access.Lock()
	defer c.input.access.Unlock()
	return c.input.scancode(scancode, down)
}

func (c *Client) SendKeyCode(keyCode uint16, iso bool, keysym uint32, down bool) error {
	active := c.session.active.Load()
	if keyCode == macKeyCapsLock {
		c.input.access.Lock()
		defer c.input.access.Unlock()
		return c.input.setLock(capsLock, down, active)
	}
	if !active {
		return nil
	}
	if iso {
		switch keyCode {
		case macKeyISOSection:
			keyCode = macKeyGrave
		case macKeyGrave:
			keyCode = macKeyISOSection
		}
	}
	c.input.access.Lock()
	defer c.input.access.Unlock()
	if int(keyCode) < len(macScancodes) && macScancodes[keyCode] != 0 {
		return c.input.scancode(macScancodes[keyCode], down)
	}
	return c.input.keysym(keysym, down)
}

func (q *inputQueue) keysym(keysym uint32, down bool) error {
	if keysym == 0 || keysym == keysymVoid {
		return nil
	}
	if keysym == keysymPause {
		if !down {
			return nil
		}
		return q.enqueue(inputGroup{events: pauseEvents})
	}
	code := keysymScancode(keysym)
	character, printable := keysymCharacter(keysym)
	if !printable {
		if code == 0 {
			return E.New("rdp: no PC key for keysym 0x", strconv.FormatUint(uint64(keysym), 16))
		}
		return q.scancode(code, down)
	}
	unicode, remembered := q.typed[keysym]
	if !remembered {
		unicode = code == 0 || q.unicode && !slices.ContainsFunc(shortcutScancodes, func(modifier uint16) bool { return q.pressed[modifier] })
	}
	if down {
		q.typed[keysym] = unicode
	} else {
		delete(q.typed, keysym)
	}
	if unicode {
		if !q.unicode {
			return E.New("rdp: the server does not accept Unicode input")
		}
		return q.enqueue(inputGroup{events: appendUnicodeEvents(nil, character, down), paced: !down})
	}
	return q.scancode(code, down)
}

func (q *inputQueue) keystroke(character rune) []inputEvent {
	if character < 0x20 || character > 0x7e {
		return nil
	}
	code := keysymScancode(uint32(character))
	if code == 0 {
		return nil
	}
	shift := unicode.IsUpper(character) || strings.ContainsRune(shiftedSymbols, character)
	if unicode.IsLetter(character) && q.locks&capsLock != 0 {
		shift = !shift
	}
	events := []inputEvent{scancodeEvent(code, true), scancodeEvent(code, false)}
	held := slices.DeleteFunc([]uint16{scancodeLeftShift, scancodeRightShift}, func(code uint16) bool { return !q.pressed[code] })
	if shift == (len(held) > 0) {
		return events
	}
	if shift {
		held = []uint16{scancodeLeftShift}
	}
	for _, code := range held {
		events = slices.Insert(events, 0, scancodeEvent(code, shift))
		events = append(events, scancodeEvent(code, !shift))
	}
	return events
}

func keysymCharacter(keysym uint32) (rune, bool) {
	switch {
	case keysym >= 0x20 && keysym <= 0x7e, keysym >= 0xa0 && keysym <= 0xff:
		return rune(keysym), true
	case keysym&0xff000000 == 0x01000000:
		character := rune(keysym & 0x00ffffff)
		return character, utf8.ValidRune(character)
	default:
		return 0, false
	}
}

var macScancodes = [128]uint16{
	0: 0x1e, 1: 0x1f, 2: 0x20, 3: 0x21, 4: 0x23, 5: 0x22, 6: 0x2c, 7: 0x2d,
	8: 0x2e, 9: 0x2f, 10: 0x56, 11: 0x30, 12: 0x10, 13: 0x11, 14: 0x12, 15: 0x13,
	16: 0x15, 17: 0x14, 18: 0x02, 19: 0x03, 20: 0x04, 21: 0x05, 22: 0x07, 23: 0x06,
	24: 0x0d, 25: 0x0a, 26: 0x08, 27: 0x0c, 28: 0x09, 29: 0x0b, 30: 0x1b, 31: 0x18,
	32: 0x16, 33: 0x1a, 34: 0x17, 35: 0x19, 36: 0x1c, 37: 0x26, 38: 0x24, 39: 0x28,
	40: 0x25, 41: 0x27, 42: 0x2b, 43: 0x33, 44: 0x35, 45: 0x31, 46: 0x32, 47: 0x34,
	48: 0x0f, 49: 0x39, 50: 0x29, 51: 0x0e, 53: 0x01,
	54: 0x15c, 55: 0x15b, 56: 0x2a, 58: 0x38, 59: 0x1d, 60: 0x36, 61: 0x138, 62: 0x11d,
	64: 0x68, 65: 0x53, 67: 0x37, 69: 0x4e, 71: 0x45, 72: 0x130, 73: 0x12e, 74: 0x120,
	75: 0x135, 76: 0x11c, 78: 0x4a, 79: 0x69, 80: 0x6a, 81: 0x59,
	82: 0x52, 83: 0x4f, 84: 0x50, 85: 0x51, 86: 0x4b, 87: 0x4c, 88: 0x4d, 89: 0x47,
	90: 0x6b, 91: 0x48, 92: 0x49, 93: 0x7d, 94: 0x73, 95: 0x7e,
	96: 0x3f, 97: 0x40, 98: 0x41, 99: 0x3d, 100: 0x42, 101: 0x43,
	102: 0x7b, 103: 0x57, 104: 0x70, 105: 0x64, 106: 0x67, 107: 0x65,
	109: 0x44, 110: 0x15d, 111: 0x58, 113: 0x66, 114: 0x152, 115: 0x147,
	116: 0x149, 117: 0x153, 118: 0x3e, 119: 0x14f, 120: 0x3c, 121: 0x151,
	122: 0x3b, 123: 0x14b, 124: 0x14d, 125: 0x150, 126: 0x148,
}

func keysymScancode(keysym uint32) uint16 {
	if keysym >= 'A' && keysym <= 'Z' {
		keysym += 'a' - 'A'
	}
	switch {
	case keysym >= 'a' && keysym <= 'z':
		return [...]uint16{0x1e, 0x30, 0x2e, 0x20, 0x12, 0x21, 0x22, 0x23, 0x17, 0x24, 0x25, 0x26, 0x32, 0x31, 0x18, 0x19, 0x10, 0x13, 0x1f, 0x14, 0x16, 0x2f, 0x11, 0x2d, 0x15, 0x2c}[keysym-'a']
	case keysym >= '1' && keysym <= '9':
		return uint16(keysym-'1') + 2
	case keysym >= 0xffbe && keysym <= 0xffc7:
		return uint16(keysym-0xffbe) + 0x3b
	case keysym >= 0xffc8 && keysym <= 0xffc9:
		return uint16(keysym-0xffc8) + 0x57
	case keysym >= 0xffca && keysym <= 0xffd5:
		return uint16(keysym-0xffca) + 0x64
	case keysym >= 0xffb0 && keysym <= 0xffb9:
		return [...]uint16{0x52, 0x4f, 0x50, 0x51, 0x4b, 0x4c, 0x4d, 0x47, 0x48, 0x49}[keysym-0xffb0]
	}
	return keysymScancodes[keysym]
}

const shiftedSymbols = `~!@#$%^&*()_+{}|:"<>?`

var keysymScancodes = map[uint32]uint16{
	'0': 0x0b, '-': 0x0c, '=': 0x0d, '[': 0x1a, ']': 0x1b, ';': 0x27, '\'': 0x28,
	'`': 0x29, '\\': 0x2b, ',': 0x33, '.': 0x34, '/': 0x35, ' ': 0x39,
	'!': 0x02, '@': 0x03, '#': 0x04, '$': 0x05, '%': 0x06, '^': 0x07, '&': 0x08, '*': 0x09,
	'(': 0x0a, ')': 0x0b, '_': 0x0c, '+': 0x0d, '{': 0x1a, '}': 0x1b, ':': 0x27, '"': 0x28,
	'~': 0x29, '|': 0x2b, '<': 0x33, '>': 0x34, '?': 0x35,
	0xff08: 0x0e, 0xff09: 0x0f, 0xfe20: 0x0f, 0xff0d: 0x1c, 0xff1b: 0x01,
	0xff14: 0x46, 0xff15: 0x137, 0xff61: 0x137, 0xff6b: 0x146,
	0xff50: 0x147, 0xff51: 0x14b, 0xff52: 0x148, 0xff53: 0x14d, 0xff54: 0x150,
	0xff55: 0x149, 0xff56: 0x151, 0xff57: 0x14f, 0xff58: 0x4c,
	0xff63: 0x152, 0xff67: 0x15d, 0xff6a: 0x152, 0xffff: 0x153,
	0xff7f: 0x45, 0xff80: 0x39, 0xff89: 0x0f, 0xff8d: 0x11c,
	0xff95: 0x47, 0xff96: 0x4b, 0xff97: 0x48, 0xff98: 0x4d, 0xff99: 0x50,
	0xff9a: 0x49, 0xff9b: 0x51, 0xff9c: 0x4f, 0xff9d: 0x4c, 0xff9e: 0x52, 0xff9f: 0x53,
	0xffaa: 0x37, 0xffab: 0x4e, 0xffac: 0x7e, 0xffad: 0x4a, 0xffae: 0x53, 0xffaf: 0x135, 0xffbd: 0x59,
	0xffe1: 0x2a, 0xffe2: 0x36, 0xffe3: 0x1d, 0xffe4: 0x11d, 0xffe5: 0x3a,
	0xffe7: 0x15b, 0xffe8: 0x15c, 0xffe9: 0x38, 0xffea: 0x138, 0xffeb: 0x15b, 0xffec: 0x15c,
	0xfe03: 0x138, 0x1008ff11: 0x12e, 0x1008ff12: 0x120, 0x1008ff13: 0x130, 0x1008ff2a: 0x15e,
}
