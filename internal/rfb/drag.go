// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/drag.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: standard-library helpers in place of sing, import paths.

package rfb

import (
	"encoding/binary"
	"math/rand/v2"
	"net/url"
	"strconv"
	"strings"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	TypeFileURL   = "public.file-url"
	TypeDragImage = "com.apple.remotedesktop.dragimage-png"

	// ScreenSharing refuses to send a drag above 7 MiB compressed, and closes the connection
	// on a server drag whose sizes pass 0x63fffff (ScreenSharing 776.27, macOS 27.0.1 26A434,
	// UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50, _RFBPostDropEventCore 0x1cc142f6c..0x1cc142f70,
	// _HandleServerMessage 0x1cc061be0..0x1cc061bf4).
	maximumDragSent     = 7 << 20
	maximumDragReceived = 0x63fffff
	maximumDropPath     = 1023
)

type RemoteDragItem struct {
	Path   string
	Folder bool
}

type RemoteDrag struct {
	Items     []RemoteDragItem
	Image     []byte
	Clipboard *Clipboard
}

// SSDragHelper turns only flavors whose type has the preferred OSType 'furl' (public.file-url)
// into com.apple.pasteboard.promised-file-url promises (SSDragHelper 756.36.5.2,
// macOS 27.0.1 26A434, UUID E020362A-B6FF-3C2D-AE9F-8A83196BD555,
// sub_1000063d4 0x100006538..0x1000066c4).
func NewFileClipboard(paths []string) *Clipboard {
	items := make([]ClipboardItem, len(paths))
	for i, path := range paths {
		location := (&url.URL{Scheme: "file", Path: path}).String()
		items[i] = ClipboardItem{Flavors: []ClipboardFlavor{{
			Type: TypeFileURL,
			Tags: []ClipboardTag{{Key: "com.apple.ostype", Value: "furl"}},
			Data: []byte(location),
		}}}
	}
	return &Clipboard{Items: items}
}

func (c *Client) checkDrag(messageType uint8) error {
	if c.info.Dialect != DialectApple || !c.info.Accepts(messageType) {
		return E.New("rfb: the server does not take drag message 0x", strconv.FormatUint(uint64(messageType), 16))
	}
	if c.Mode() == ModeObserve {
		return E.New("rfb: drags are not sent while observing")
	}
	return nil
}

// The Mac's drag follows pointer events with the left button held and drops where the
// button is released.
func (c *Client) SendDrag(clipboard *Clipboard) (uint32, error) {
	err := c.checkDrag(0x20)
	if err != nil {
		return 0, err
	}
	archive := buildPasteboardArchive(clipboard)
	compressed, err := compressPasteboard(archive)
	if err != nil {
		return 0, err
	}
	if len(compressed) > maximumDragSent {
		return 0, E.New("rfb: drag of ", len(compressed), " compressed bytes is larger than the ", maximumDragSent, " bytes the Mac takes")
	}
	id := newDragID()
	return id, c.send(append(dragMessage(id, uint32(len(archive)), uint32(len(compressed))), compressed...))
}

func (c *Client) CancelDrag(id uint32) error {
	err := c.checkDrag(0x20)
	if err != nil {
		return err
	}
	return c.send(dragMessage(id, 0, 0))
}

// ScreensharingAgent's CancelDrag handler ignores the ID and ends a drag of the Mac's own by
// activating Finder and posting Escape (ScreensharingAgent 756.36.5.2, macOS 27.0.1 26A434,
// UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3, sub_10001b3bc 0x10001b63c..0x10001b65c,
// sub_100022890 0x1000229e8..0x100022b8c).
func (c *Client) CancelRemoteDrag() error {
	return c.CancelDrag(newDragID())
}

// ScreensharingAgent ignores a drag request when the Mac has no drag in progress, so nothing
// answers it (ScreensharingAgent 756.36.5.2, macOS 27.0.1 26A434,
// UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3, sub_10001b1f0 0x10001b28c..0x10001b298,
// 0x10001b334..0x10001b3a8).
func (c *Client) RequestDrag() error {
	err := c.checkDrag(0x0e)
	if err != nil {
		return err
	}
	message := []byte{0x0e, 0, 0, 0}
	return c.send(binary.BigEndian.AppendUint32(message, newDragID()))
}

func newDragID() uint32 {
	return rand.Uint32N(0x7fffffff) + 1
}

func dragMessage(id, size, compressedSize uint32) []byte {
	message := []byte{0x20, 0, 0, 0}
	message = binary.BigEndian.AppendUint32(message, id)
	message = binary.BigEndian.AppendUint32(message, size)
	return binary.BigEndian.AppendUint32(message, compressedSize)
}

// ScreensharingAgent writes the drag as 20 00 00 00 | A | uncompressed size | compressed size,
// with A always 1, and sends no data once the drag ends on the Mac. Its writer gets A = 1
// directly or from SSDragHelper, which sends only the A it was started with, and the agent
// starts it with A = 1 when it gives no drag data (ScreensharingAgent 756.36.5.2,
// macOS 27.0.1 26A434, UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3,
// sub_1000080b8 0x10000819c..0x1000081d8, sub_100013070 0x1000132c4..0x1000132d8,
// sub_1000136d8 0x100014518, sub_100007c80 0x100007d0c..0x100007db4,
// sub_10001b3bc 0x10001b4f8..0x10001b50c, sub_100022378 0x100022434..0x100022448;
// SSDragHelper 756.36.5.2, macOS 27.0.1 26A434, UUID E020362A-B6FF-3C2D-AE9F-8A83196BD555,
// sub_100003318 0x10000374c..0x100003758, 0x100003d74..0x100003d7c, 0x100004658,
// sub_100006b40 0x100007bb8..0x100007bc4).
// ScreenSharing ignores A = 0 (ScreenSharing 776.27, macOS 27.0.1 26A434,
// UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50, -[SSSession handleViewerDropEvent:]
// 0x1cc0e2a04..0x1cc0e2a08).
func (c *Client) receiveDrag() error {
	header, err := c.stream.bytes(15)
	if err != nil {
		return err
	}
	id := binary.BigEndian.Uint32(header[3:])
	size := binary.BigEndian.Uint32(header[7:])
	compressedSize := binary.BigEndian.Uint32(header[11:])
	if size > maximumDragReceived || compressedSize > maximumDragReceived {
		return E.New("rfb: drag of ", size, " bytes, ", compressedSize, " compressed, is over the ", maximumDragReceived, "-byte limit")
	}
	compressed, err := c.stream.bytes(int(compressedSize))
	if err != nil {
		return err
	}
	if id == 0 {
		return nil
	}
	if compressedSize == 0 {
		c.handler.RemoteDragChanged(nil)
		return nil
	}
	archive, err := inflatePasteboard(compressed, size)
	if err != nil {
		return E.Cause(err, "inflate drag")
	}
	clipboard, err := parsePasteboardArchive(archive)
	if err != nil {
		c.logger.Warn("unreadable drag", "error", err)
		return nil
	}
	drag := &RemoteDrag{Clipboard: clipboard}
	for _, item := range clipboard.Items {
		for _, flavor := range item.Flavors {
			switch flavor.Type {
			case TypeDragImage:
				drag.Image = flavor.Data
			case TypeFileURL:
				location, parseErr := url.Parse(string(flavor.Data))
				if parseErr != nil || location.Scheme != "file" || location.Path == "" {
					continue
				}
				path := location.Path
				folder := len(path) > 1 && strings.HasSuffix(path, "/")
				if folder {
					path = path[:len(path)-1]
				}
				drag.Items = append(drag.Items, RemoteDragItem{Path: path, Folder: folder})
			}
		}
	}
	c.handler.RemoteDragChanged(drag)
	return nil
}

// ScreensharingAgent writes the folder a drag from SendDrag landed on as
// 1e 00 00 00 | A | path length | path, without a terminator, A being the ID SendDrag sent:
// screensharingd passes the ID to the agent, the agent to SSDragHelper, and SSDragHelper
// returns it with the drop path (ScreensharingAgent 756.36.5.2, macOS 27.0.1 26A434,
// UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3, sub_100007dd4 0x100007ec0..0x100007eec,
// sub_100007a60 0x100007bfc..0x100007c04, sub_10001ad84 0x10001af80..0x10001af94;
// screensharingd 756.36.5.2, macOS 27.0.1 26A434, UUID 44908B06-F788-3822-ABC4-2285001F6A8D,
// sub_100036680 0x10003a31c..0x10003a320, 0x100039c34..0x100039c40;
// SSDragHelper 756.36.5.2, macOS 27.0.1 26A434, UUID E020362A-B6FF-3C2D-AE9F-8A83196BD555,
// sub_100003318 0x100003d74..0x100003d84, 0x100005c6c..0x100005c78).
// ScreenSharing treats a path longer than 1023 bytes as an error (ScreenSharing 776.27,
// macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50, _HandleServerMessage
// 0x1cc061f10..0x1cc061f14).
func (c *Client) receiveDropDestination() error {
	header, err := c.stream.bytes(11)
	if err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[7:])
	if length > maximumDropPath {
		return E.New("rfb: drop path of ", length, " bytes is over the ", maximumDropPath, "-byte limit")
	}
	path, err := c.stream.bytes(int(length))
	if err != nil {
		return err
	}
	c.handler.DropDestinationReceived(binary.BigEndian.Uint32(header[3:]), string(path))
	return nil
}
