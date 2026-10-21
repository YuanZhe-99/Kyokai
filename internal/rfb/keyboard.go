// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/keyboard.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: standard-library helpers in place of sing, import paths.

package rfb

import (
	"encoding/binary"
	"slices"
	"strconv"
	"sync"
	"time"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

type KeyboardType uint16

const (
	KeyboardANSI KeyboardType = 40
	KeyboardISO  KeyboardType = 41
	KeyboardJIS  KeyboardType = 42
)

const (
	keyFlagKeyCodeValid  uint8 = 1
	keyFlagPreferKeyCode uint8 = 2
)

const keysymVoid uint32 = 0xffffff

const macKeyCodeCommand uint16 = 55

const (
	pasteRestoreDelay   = time.Second
	pasteRestoreMinimum = 150 * time.Millisecond
	pasteFetchTimeout   = 2 * time.Second
	pasteEchoWindow     = 3 * time.Second
	typeTransient       = "org.nspasteboard.TransientType"
)

type keyboardState struct {
	queueAccess sync.Mutex
	queue       []func()
	draining    bool

	paste               *textPaste
	pasteboard          *Clipboard
	pasteboardWaiters   []chan *Clipboard
	ownPasteboardEchoes []time.Time
}

type textPaste struct {
	saved      *Clipboard
	restore    *time.Timer
	due        time.Time
	lastPasted time.Time
}

// screensharingd rejects decrypted events unless bytes 0 and 10 are each 0 or 0xff
// (screensharingd 756.36.5.2, macOS 27.0.1 26A434, UUID 44908B06-F788-3822-ABC4-2285001F6A8D,
// sub_100036680 0x10003c218..0x10003c234).
func encryptedKeyMessage(key [16]byte, flags uint8, keysym uint32, down bool, keyboardType KeyboardType, keyCode uint16) ([]byte, error) {
	message := make([]byte, 18)
	message[0] = 0x10
	message[1] = flags
	block := message[2:]
	block[0] = 0xff
	block[1] = boolByte(down)
	binary.BigEndian.PutUint32(block[2:], keysym)
	binary.BigEndian.PutUint16(block[12:], uint16(keyboardType))
	binary.BigEndian.PutUint16(block[14:], keyCode)
	err := encryptECB(key[:], block)
	if err != nil {
		return nil, err
	}
	return message, nil
}

func (c *Client) SupportsKeyCodes() bool {
	return c.info.Accepts(0x10)
}

func (c *Client) observing() bool {
	c.access.Lock()
	defer c.access.Unlock()
	return c.mode == ModeObserve
}

func (c *Client) SendKeyCode(keyCode uint16, keyboardType KeyboardType, keysym uint32, down bool) error {
	if !c.SupportsKeyCodes() {
		if keysym == 0 {
			return nil
		}
		return c.SendKey(keysym, down)
	}
	if c.observing() {
		return E.New("rfb: key events are not sent while observing")
	}
	c.trackKey(keysym, down)
	message, err := encryptedKeyMessage(c.wrapKey, keyFlagKeyCodeValid|keyFlagPreferKeyCode, appleKeysym(keysym), down, keyboardType, keyCode)
	if err != nil {
		return err
	}
	return c.sendKeyboard(message)
}

func (c *Client) sendKeyboard(message []byte) error {
	c.keyboard.queueAccess.Lock()
	if c.keyboard.draining {
		c.keyboard.queue = append(c.keyboard.queue, func() {
			c.sendInput(message, false)
		})
		c.keyboard.queueAccess.Unlock()
		return nil
	}
	c.keyboard.queueAccess.Unlock()
	return c.sendInput(message, false)
}

func (c *Client) enqueueKeyboard(operation func()) {
	c.keyboard.queueAccess.Lock()
	c.keyboard.queue = append(c.keyboard.queue, operation)
	draining := c.keyboard.draining
	c.keyboard.draining = true
	c.keyboard.queueAccess.Unlock()
	if !draining {
		go c.drainKeyboard()
	}
}

func (c *Client) drainKeyboard() {
	for {
		c.keyboard.queueAccess.Lock()
		if len(c.keyboard.queue) == 0 {
			c.keyboard.draining = false
			c.keyboard.queueAccess.Unlock()
			return
		}
		operation := c.keyboard.queue[0]
		c.keyboard.queue = c.keyboard.queue[1:]
		c.keyboard.queueAccess.Unlock()
		operation()
	}
}

func keyboardSourceMessage(id string) []byte {
	message := []byte{0x1a, 0}
	message = binary.BigEndian.AppendUint16(message, uint16(4+len(id)))
	message = binary.BigEndian.AppendUint16(message, 1)
	message = binary.BigEndian.AppendUint16(message, uint16(len(id)))
	return append(message, id...)
}

func (c *Client) SetKeyboardSource(id string) error {
	return c.sendKeyboard(keyboardSourceMessage(id))
}

func (c *Client) SelectKeyboardSource(id string) error {
	if id == "" {
		return E.New("rfb: no input source")
	}
	if c.observing() {
		return E.New("rfb: input source ", strconv.Quote(id), " is not selected while observing")
	}
	message := keyboardSourceMessage(id)
	message = append(message, keyEventMessage(false, keysymVoid)...)
	message = append(message, keyboardSourceMessage("")...)
	return c.sendKeyboard(message)
}

func (c *Client) Paste(clipboard *Clipboard, super bool) error {
	if c.observing() {
		return E.New("rfb: clipboard is not pasted while observing")
	}
	err := c.SetClipboard(clipboard)
	if err != nil {
		return err
	}
	apple := c.info.Dialect == DialectApple
	modifier, alternate := KeysymControlLeft, KeysymControlRight
	if apple || super {
		modifier, alternate = KeysymSuperLeft, KeysymSuperRight
	}
	c.access.Lock()
	held := c.heldKeys[modifier] || c.heldKeys[alternate]
	c.access.Unlock()
	press := func(down bool) error {
		if apple {
			return c.SendKeyCode(macKeyCodeCommand, KeyboardANSI, modifier, down)
		}
		return c.SendKey(modifier, down)
	}
	if !held {
		err = press(true)
		if err != nil {
			return err
		}
	}
	err = c.SendKey('v', true)
	if err == nil {
		err = c.SendKey('v', false)
	}
	if !held {
		err = E.Errors(err, press(false))
	}
	return err
}

func (c *Client) PasteText(text string) error {
	if c.info.Dialect != DialectApple {
		return E.New("rfb: only a Mac takes pasted text")
	}
	if c.observing() {
		return E.New("rfb: text is not pasted while observing")
	}
	clipboard := NewTextClipboard(text)
	if len(clipboard.Items) > 0 {
		item := &clipboard.Items[0]
		item.Flavors = append(item.Flavors, ClipboardFlavor{Type: typeTransient, Data: []byte{1}})
	}
	message, err := pasteboardMessage(clipboard, 0)
	if err != nil {
		return err
	}
	command := appleKeysym(KeysymSuperLeft)
	message = append(message, keyEventMessage(true, command)...)
	message = append(message, keyEventMessage(true, 'v')...)
	message = append(message, keyEventMessage(false, 'v')...)
	message = append(message, keyEventMessage(false, command)...)
	c.enqueueKeyboard(func() {
		err := c.pasteText(message)
		if err != nil {
			c.logger.Warn("paste text", "error", err)
		}
	})
	return nil
}

func (c *Client) pasteText(message []byte) error {
	c.access.Lock()
	run := c.keyboard.paste
	if run != nil {
		run.restore.Stop()
	}
	c.access.Unlock()
	var unsaved error
	if run == nil {
		var saved *Clipboard
		saved, unsaved = c.pasteboardToRestore()
		run = &textPaste{saved: saved}
	}
	c.expectOwnPasteboardEcho()
	err := c.sendInput(message, false)
	now := time.Now()
	c.access.Lock()
	c.keyboard.paste = run
	run.lastPasted = now
	run.due = now.Add(pasteRestoreDelay)
	run.restore = time.AfterFunc(pasteRestoreDelay, func() {
		c.enqueueKeyboard(func() {
			c.restorePasteboard(run)
		})
	})
	c.access.Unlock()
	if err != nil {
		return err
	}
	if unsaved != nil {
		c.logger.Warn("pasteboard is not put back after pasting", "error", unsaved)
		c.handler.ClipboardFailed(ClipboardNotRestored, E.Cause(unsaved, "fetch the pasteboard to restore after pasting"))
	}
	return nil
}

func (c *Client) restorePasteboard(run *textPaste) {
	c.access.Lock()
	if c.keyboard.paste != run || time.Now().Before(run.due) {
		c.access.Unlock()
		return
	}
	c.keyboard.paste = nil
	saved := run.saved
	c.access.Unlock()
	if saved == nil {
		return
	}
	message, err := pasteboardMessage(saved, 0)
	if err == nil {
		c.expectOwnPasteboardEcho()
		err = c.sendInput(message, false)
	}
	if err != nil {
		c.logger.Warn("restore pasteboard", "error", err)
	}
}

func (c *Client) finishPaste() {
	c.access.Lock()
	run := c.keyboard.paste
	c.keyboard.paste = nil
	var saved *Clipboard
	var wait time.Duration
	if run != nil {
		run.restore.Stop()
		saved = run.saved
		wait = time.Until(run.lastPasted.Add(pasteRestoreMinimum))
	}
	c.access.Unlock()
	if saved == nil {
		return
	}
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-c.writerDone:
			return
		}
	}
	message, err := pasteboardMessage(saved, 0)
	if err == nil {
		c.send(message)
	}
}

func (c *Client) pasteboardToRestore() (*Clipboard, error) {
	c.access.Lock()
	cached, shared := c.keyboard.pasteboard, c.sharedClipboard
	c.access.Unlock()
	if shared && cached != nil && !cached.Promise {
		return restorableClipboard(cached), nil
	}
	waiter := make(chan *Clipboard, 1)
	c.access.Lock()
	c.keyboard.pasteboardWaiters = append(c.keyboard.pasteboardWaiters, waiter)
	c.access.Unlock()
	err := c.RequestClipboard()
	if err == nil {
		timer := time.NewTimer(pasteFetchTimeout)
		defer timer.Stop()
		select {
		case clipboard := <-waiter:
			if clipboard != nil {
				return restorableClipboard(clipboard), nil
			}
			err = E.New("rfb: the Mac's pasteboard is unreadable")
		case <-timer.C:
			err = E.New("rfb: no pasteboard in ", pasteFetchTimeout)
		case <-c.writerDone:
			err = E.New("rfb: session ended")
		}
	}
	c.access.Lock()
	if index := slices.Index(c.keyboard.pasteboardWaiters, waiter); index >= 0 {
		c.keyboard.pasteboardWaiters = slices.Delete(c.keyboard.pasteboardWaiters, index, index+1)
	}
	c.access.Unlock()
	return nil, err
}

func restorableClipboard(clipboard *Clipboard) *Clipboard {
	restorable := &Clipboard{}
	for _, item := range clipboard.Items {
		kept := ClipboardItem{Flavors: slices.DeleteFunc(slices.Clone(item.Flavors), func(flavor ClipboardFlavor) bool {
			return flavor.Promise
		})}
		if len(kept.Flavors) > 0 {
			restorable.Items = append(restorable.Items, kept)
		}
	}
	return restorable
}

func (c *Client) expectOwnPasteboardEcho() {
	c.access.Lock()
	if c.sharedClipboard {
		c.keyboard.ownPasteboardEchoes = append(c.keyboard.ownPasteboardEchoes, time.Now().Add(pasteEchoWindow))
	}
	c.access.Unlock()
}

func (c *Client) takeOwnPasteboardEchoLocked() bool {
	now := time.Now()
	echoes := c.keyboard.ownPasteboardEchoes
	for len(echoes) > 0 && now.After(echoes[0]) {
		echoes = echoes[1:]
	}
	if len(echoes) == 0 {
		c.keyboard.ownPasteboardEchoes = nil
		return false
	}
	c.keyboard.ownPasteboardEchoes = echoes[1:]
	return true
}

func (c *Client) pasteboardReceived(clipboard *Clipboard) {
	c.access.Lock()
	waiters := c.keyboard.pasteboardWaiters
	c.keyboard.pasteboardWaiters = nil
	c.keyboard.pasteboard = clipboard
	c.access.Unlock()
	for _, waiter := range waiters {
		waiter <- clipboard
	}
}
