// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/clipboard.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: standard-library helpers in place of sing, import paths.

package rfb

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"strconv"
	"unicode/utf8"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	TypeUTF8PlainText  = "public.utf8-plain-text"
	typeUTF16PlainText = "public.utf16-plain-text"
	// ScreensharingAgent's SetPasteboard handler rejects a pasteboard whose compressed or
	// uncompressed size is above 100 MiB (ScreensharingAgent 756.36.5.2, macOS 27.0.1 26A434,
	// UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3, sub_100019b98 0x100019c74..0x100019c8c).
	maximumPasteboard = 100 << 20
)

type Clipboard struct {
	Items   []ClipboardItem
	Promise bool
}

type ClipboardItem struct {
	Flavors []ClipboardFlavor
}

type ClipboardFlavor struct {
	Type    string
	Tags    []ClipboardTag
	Data    []byte
	Promise bool
}

type ClipboardTag struct {
	Key   string
	Value string
}

type ClipboardProblem int

const (
	ClipboardUnreadable ClipboardProblem = iota + 1
	ClipboardNotRestored
)

func NewTextClipboard(text string) *Clipboard {
	if text == "" {
		return &Clipboard{}
	}
	return &Clipboard{Items: []ClipboardItem{{Flavors: []ClipboardFlavor{{Type: TypeUTF8PlainText, Data: []byte(text)}}}}}
}

func (c *Clipboard) Text() (string, bool) {
	for _, item := range c.Items {
		for _, flavor := range item.Flavors {
			if flavor.Type == TypeUTF8PlainText && !flavor.Promise {
				return string(flavor.Data), true
			}
		}
	}
	return "", false
}

func parsePasteboardArchive(archive []byte) (*Clipboard, error) {
	reader := &fieldReader{data: archive}
	counted := func(limit int) []byte {
		length := reader.take(4)
		if length == nil {
			return nil
		}
		size := int(binary.BigEndian.Uint32(length))
		if size > limit {
			reader.err = E.New("rfb: pasteboard field of ", size, " bytes is over the ", limit, "-byte limit")
			return nil
		}
		return reader.take(size)
	}
	number := func() int {
		value := reader.take(4)
		if value == nil {
			return 0
		}
		return int(binary.BigEndian.Uint32(value))
	}
	clipboard := &Clipboard{}
	for len(reader.data) > 0 && reader.err == nil {
		flavorCount := number()
		if flavorCount > 1024 {
			return nil, E.New("rfb: pasteboard item with ", flavorCount, " flavors, over the limit of 1024")
		}
		var item ClipboardItem
		for i := 0; i < flavorCount && reader.err == nil; i++ {
			var flavor ClipboardFlavor
			flavor.Type = string(counted(4096))
			number()
			tagCount := number()
			if tagCount > 256 {
				return nil, E.New("rfb: pasteboard flavor with ", tagCount, " tags, over the limit of 256")
			}
			for j := 0; j < tagCount && reader.err == nil; j++ {
				key := string(counted(4096))
				value := string(counted(4096))
				flavor.Tags = append(flavor.Tags, ClipboardTag{Key: key, Value: value})
			}
			flavor.Data = bytes.Clone(counted(maximumPasteboard))
			flavor.Promise = len(flavor.Data) == 0
			if flavor.Type == typeUTF16PlainText && len(flavor.Data)%2 == 0 {
				flavor.Data = swapUTF16Order(flavor.Data)
			}
			item.Flavors = append(item.Flavors, flavor)
		}
		clipboard.Items = append(clipboard.Items, item)
	}
	if reader.err != nil {
		return nil, E.Cause(reader.err, "parse pasteboard")
	}
	return clipboard, nil
}

func buildPasteboardArchive(clipboard *Clipboard) []byte {
	var archive []byte
	if len(clipboard.Items) == 0 {
		return binary.BigEndian.AppendUint32(archive, 0)
	}
	appendCounted := func(value []byte) {
		archive = binary.BigEndian.AppendUint32(archive, uint32(len(value)))
		archive = append(archive, value...)
	}
	for _, item := range clipboard.Items {
		archive = binary.BigEndian.AppendUint32(archive, uint32(len(item.Flavors)))
		for _, flavor := range item.Flavors {
			appendCounted([]byte(flavor.Type))
			archive = binary.BigEndian.AppendUint32(archive, 0)
			archive = binary.BigEndian.AppendUint32(archive, uint32(len(flavor.Tags)))
			for _, tag := range flavor.Tags {
				appendCounted([]byte(tag.Key))
				appendCounted([]byte(tag.Value))
			}
			data := flavor.Data
			if flavor.Type == typeUTF16PlainText && len(data)%2 == 0 {
				data = swapUTF16Order(data)
			}
			appendCounted(data)
		}
	}
	return archive
}

// ScreenSharing passes each flavor through CoreEndianFlipData('aevt', its OSType), which turns
// 'utxt' into big-endian UTF-16 on the wire (ScreenSharing 776.27, macOS 27.0.1 26A434,
// _CopyPasteboardEntries 0x1cc179428..0x1cc179454, _UnpackScrapDataWithFilter 0x1cc165b94..0x1cc165bb8).
func swapUTF16Order(data []byte) []byte {
	swapped := make([]byte, len(data))
	for i := 0; i < len(data); i += 2 {
		binary.NativeEndian.PutUint16(swapped[i:], binary.BigEndian.Uint16(data[i:]))
	}
	return swapped
}

func inflatePasteboard(compressed []byte, size uint32) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	archive, err := io.ReadAll(io.LimitReader(reader, int64(size)))
	if err == nil && len(archive) != int(size) {
		err = io.ErrUnexpectedEOF
	}
	return archive, err
}

func compressPasteboard(archive []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer, err := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	if err != nil {
		return nil, err
	}
	writer.Write(archive)
	err = writer.Flush()
	if err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

func pasteboardMessage(clipboard *Clipboard, echo uint32) ([]byte, error) {
	archive := buildPasteboardArchive(clipboard)
	if len(archive) > maximumPasteboard {
		return nil, E.New("rfb: pasteboard of ", len(archive), " bytes is over the 100 MiB limit")
	}
	compressed, err := compressPasteboard(archive)
	if err != nil {
		return nil, err
	}
	if len(compressed) > maximumPasteboard {
		return nil, E.New("rfb: pasteboard of ", len(compressed), " compressed bytes is over the 100 MiB limit")
	}
	message := []byte{0x1f, 0, boolByte(clipboard.Promise), 0}
	message = binary.BigEndian.AppendUint32(message, echo)
	message = binary.BigEndian.AppendUint32(message, uint32(len(archive)))
	message = binary.BigEndian.AppendUint32(message, uint32(len(compressed)))
	return append(message, compressed...), nil
}

func (c *Client) receivePasteboard() error {
	header, err := c.stream.bytes(15)
	if err != nil {
		return err
	}
	promise := header[1]&1 != 0
	uncompressed := binary.BigEndian.Uint32(header[7:])
	compressedSize := binary.BigEndian.Uint32(header[11:])
	if uncompressed > maximumPasteboard || compressedSize > maximumPasteboard {
		return E.New("rfb: pasteboard of ", uncompressed, " bytes, ", compressedSize, " compressed, is over the 100 MiB limit")
	}
	compressed, err := c.stream.bytes(int(compressedSize))
	if err != nil {
		return err
	}
	archive, err := inflatePasteboard(compressed, uncompressed)
	if err != nil {
		return E.Cause(err, "inflate pasteboard")
	}
	clipboard, err := parsePasteboardArchive(archive)
	if err != nil {
		c.logger.Warn("unreadable pasteboard", "error", err)
		c.pasteboardReceived(nil)
		c.handler.ClipboardFailed(ClipboardUnreadable, err)
		return c.afterUpdate()
	}
	clipboard.Promise = promise
	c.pasteboardReceived(clipboard)
	c.handler.ClipboardReceived(clipboard)
	return c.afterUpdate()
}

func (c *Client) RequestClipboard() error {
	if c.info.Dialect != DialectApple {
		return E.New("rfb: only a Mac can be asked for its pasteboard")
	}
	c.access.Lock()
	c.clipboardEcho++
	echo := c.clipboardEcho
	c.access.Unlock()
	message := []byte{0x0b, 0, 0, 0}
	return c.send(binary.BigEndian.AppendUint32(message, echo))
}

func (c *Client) SetSharedClipboard(enabled bool) error {
	if c.info.Dialect != DialectApple {
		return E.New("rfb: only a Mac shares its pasteboard")
	}
	c.enqueueKeyboard(func() {
		c.access.Lock()
		c.sharedClipboard = enabled
		c.access.Unlock()
		err := c.send(autoPasteboardMessage(enabled))
		if err == nil && enabled {
			err = c.RequestClipboard()
		}
		if err != nil {
			c.logger.Warn("share pasteboard", "error", err)
		}
	})
	return nil
}

func (c *Client) SetClipboard(clipboard *Clipboard) error {
	if c.info.Dialect != DialectApple {
		text, found := clipboard.Text()
		if !found {
			return E.New("rfb: the clipboard has no text, and the server takes only text")
		}
		encoded, err := encodeLatin1(text)
		if err != nil {
			return err
		}
		return c.send(clientCutTextMessage(encoded))
	}
	message, err := pasteboardMessage(clipboard, 0)
	if err != nil {
		return err
	}
	c.enqueueKeyboard(func() {
		c.access.Lock()
		restorable := restorableClipboard(clipboard)
		if c.keyboard.paste != nil {
			c.keyboard.paste.saved = restorable
		}
		c.keyboard.pasteboard = restorable
		c.access.Unlock()
		c.expectOwnPasteboardEcho()
		sendErr := c.send(message)
		if sendErr != nil {
			c.logger.Warn("set pasteboard", "error", sendErr)
		}
	})
	return nil
}

func (c *Client) receiveCutText() error {
	header, err := c.stream.bytes(7)
	if err != nil {
		return err
	}
	length := int32(binary.BigEndian.Uint32(header[3:]))
	if length < 0 {
		return c.stream.discard(int(-length))
	}
	if length > maximumPasteboard {
		return E.New("rfb: cut text of ", length, " bytes is over the 100 MiB limit")
	}
	text, err := c.stream.bytes(int(length))
	if err != nil {
		return err
	}
	c.handler.ClipboardReceived(NewTextClipboard(decodeLatin1(text)))
	return nil
}

func decodeLatin1(text []byte) string {
	runes := make([]rune, len(text))
	for i, value := range text {
		runes[i] = rune(value)
	}
	return string(runes)
}

func encodeLatin1(text string) ([]byte, error) {
	encoded := make([]byte, 0, len(text))
	for len(text) > 0 {
		value, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		if value > 0xff {
			return nil, E.New("rfb: the server's clipboard takes only Latin-1 text, not ", strconv.QuoteRune(value))
		}
		encoded = append(encoded, byte(value))
	}
	return encoded, nil
}
