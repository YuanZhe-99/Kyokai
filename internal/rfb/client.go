// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/client.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: media stream, file copy, virtual displays and the Prepare hook removed, import paths.

package rfb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"math"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YuanZhe-99/Kyokai/internal/bgra"
	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
	"github.com/YuanZhe-99/Kyokai/internal/outbox"
)

type State int

const (
	StateConnecting State = iota
	StateAuthenticating
	StateWaitingForApproval
	StateChoosingSession
	StateConnected
	StateClosed
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateAuthenticating:
		return "authenticating"
	case StateWaitingForApproval:
		return "waiting-for-approval"
	case StateChoosingSession:
		return "choosing-session"
	case StateConnected:
		return "connected"
	case StateClosed:
		return "closed"
	default:
		return fmt.Sprint("state(", int(s), ")")
	}
}

type Handler interface {
	StateChanged(state State)
	ResizeFramebuffer(width, height int) (*bgra.Image, error)
	FramebufferUpdated(update Update)
	CursorChanged(cursor *Cursor)
	CursorMoved(x, y int)
	DisplaysChanged(layout *DisplayLayout)
	ClipboardReceived(clipboard *Clipboard)
	ClipboardFailed(problem ClipboardProblem, err error)
	Bell()
	KeyboardSourceChanged(source KeyboardSource)
	StatusReceived(status Status)
	DeviceInfoReceived(info DeviceInfo)
	RemoteDragChanged(drag *RemoteDrag)
	DropDestinationReceived(id uint32, path string)
	SystemCommandsChanged(commands []SystemCommand)
	ChooseSession(request SessionRequest) (SessionChoice, error)
}

type Update struct {
	Dirty      Rectangle
	Rectangles []Rectangle
}

type Client struct {
	options Options
	handler Handler
	logger  *slog.Logger
	conn    net.Conn
	stream  *stream
	outbox  *outbox.Outbox
	writer  *outbox.Writer
	info    ServerInfo
	wrapKey [16]byte
	ctx     context.Context
	cancel  context.CancelCauseFunc

	decoders     decoders
	cursors      map[uint32]*Cursor
	updates      atomic.Uint64
	rectangles   atomic.Uint64
	pendingRekey bool
	rekeyKeys    recordKeys
	running      bool

	framebufferAccess sync.Mutex
	framebuffer       *bgra.Image
	width             int
	height            int
	size              atomic.Uint64

	access                sync.Mutex
	mode                  Mode
	quality               Quality
	sharedClipboard       bool
	pushing               bool
	updateInterval        time.Duration
	scale                 float64
	buttons               uint8
	heldKeys              map[uint32]bool
	scrollRemainderX      float64
	scrollRemainderY      float64
	layout                *DisplayLayout
	clipboardEcho         uint32
	accessibilitySettings uint32
	gestures              gestureState
	keyboard              keyboardState

	lastUpdate     atomic.Int64
	pixelsReceived atomic.Bool
	inputReady     bool
	heldInput      [][]byte
	heldMotion     bool

	closeOnce   sync.Once
	closed      atomic.Bool
	writerDone  chan struct{}
	writerError error
}

func Dial(ctx context.Context, options Options, handler Handler) (*Client, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if options.ClientName == "" {
		options.ClientName = options.Username
	}
	lifetime, cancelLifetime := context.WithCancelCause(context.Background())
	client := &Client{
		ctx:             lifetime,
		cancel:          cancelLifetime,
		options:         options,
		handler:         handler,
		logger:          logger,
		cursors:         make(map[uint32]*Cursor),
		mode:            options.Mode,
		quality:         options.Quality,
		scale:           1,
		updateInterval:  options.UpdateInterval,
		sharedClipboard: options.Clipboard,
		writerDone:      make(chan struct{}),
	}
	handler.StateChanged(StateConnecting)
	conn, err := dial(ctx, options)
	if err != nil {
		cancelLifetime(err)
		return nil, err
	}
	client.conn = conn
	stop := context.AfterFunc(ctx, func() {
		conn.Close()
	})
	err = client.setup(ctx)
	if !stop() {
		err = E.Errors(ctx.Err(), err)
	}
	if err != nil {
		cancelLifetime(err)
		if client.outbox != nil {
			client.outbox.Abort(err)
		}
		conn.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) setup(ctx context.Context) error {
	c.stream = newStream(c.conn)
	h := &handshake{ctx: ctx, conn: c.conn, stream: c.stream, options: &c.options, logger: c.logger}
	err := h.negotiateVersion()
	if err != nil {
		return err
	}
	c.handler.StateChanged(StateAuthenticating)
	h.selected = func(securityType SecurityType) {
		if securityType == SecurityAskControl || securityType == SecurityAskObserve {
			c.handler.StateChanged(StateWaitingForApproval)
		}
	}
	err = h.authenticate()
	if err != nil {
		return err
	}
	err = h.initialize()
	if err != nil {
		return err
	}
	c.info = h.info
	copy(c.wrapKey[:], h.wrapKey)
	_, err = c.resizeLocked(c.info.Width, c.info.Height)
	if err != nil {
		return err
	}
	if c.info.Dialect == DialectApple && c.info.Flags&ServerFlagSessionSelect != 0 {
		err = c.selectSession()
		if err != nil {
			return err
		}
	}
	c.outbox = outbox.New()
	c.writer = outbox.NewWriter(c.conn)
	if c.info.Dialect == DialectApple {
		err = c.startApple()
	} else {
		err = c.startStandard()
	}
	if err != nil {
		return err
	}
	go func() {
		c.writerError = c.writer.Run(c.ctx, c.outbox)
		if c.writerError != nil {
			c.conn.Close()
		}
		close(c.writerDone)
	}()
	c.handler.StateChanged(StateConnected)
	return nil
}

func (c *Client) Info() ServerInfo {
	return c.info
}

func (c *Client) Logger() *slog.Logger {
	return c.logger
}

func (c *Client) startApple() error {
	if c.wrapKey == [16]byte{} {
		return E.New("rfb: security type ", c.info.SecurityType, " left no key for the record layer")
	}
	var prelude []byte
	prelude = append(prelude, viewerInfoMessage()...)
	prelude = append(prelude, setModeMessage(c.mode)...)
	if c.options.Clipboard {
		prelude = append(prelude, autoPasteboardMessage(true)...)
	}
	prelude = append(prelude, 0x12, 0, 0, 1, 0, 1, 0, 1, 0, 0, 0, 1)
	prelude = append(prelude, 0x12, 0, 0, 2, 0, 1, 0, 0)
	_, err := c.conn.Write(prelude)
	if err != nil {
		return err
	}
	c.pendingRekey = true
	for c.pendingRekey {
		err = c.receiveMessage()
		if err != nil {
			return E.Cause(err, "wait for the record layer")
		}
	}
	encoder := &recordEncoder{}
	err = encoder.rekey(c.rekeyKeys)
	if err != nil {
		return err
	}
	c.writer.SetEncoder(encoder.encode)
	c.logger.Debug("record layer active")
	return c.writer.Write(c.firstMessages())
}

func (c *Client) firstMessages() [][]byte {
	var messages [][]byte
	messages = append(messages, setPixelFormatMessage(pixelFormatBGRA))
	messages = append(messages, setEncodingsMessage(c.encodings()))
	if c.options.DisplayID != 0 {
		messages = append(messages, setDisplayMessage(c.options.DisplayID))
	}
	c.pushing = c.options.AutomaticUpdates && c.info.Accepts(0x09)
	return append(messages, c.updateRequests(c.info.Width, c.info.Height)...)
}

func (c *Client) startStandard() error {
	c.inputReady = true
	messages := [][]byte{
		setPixelFormatMessage(pixelFormatBGRA),
		setEncodingsMessage(c.encodings()),
		framebufferUpdateRequestMessage(false, c.info.Width, c.info.Height),
	}
	return c.writer.Write(messages)
}

func (c *Client) encodings() []Encoding {
	c.access.Lock()
	quality := c.quality
	c.access.Unlock()
	picture := c.options.Encodings
	if c.info.Dialect != DialectApple {
		if len(picture) == 0 {
			picture = standardPictureEncodings
		}
		return append(slices.Clone(picture), standardMetadataEncodings...)
	}
	if len(picture) == 0 {
		picture = applePictureEncodings(quality)
	}
	return append(slices.Clone(picture), appleMetadataEncodings...)
}

func (c *Client) armMessage(width, height int) []byte {
	if !c.info.Accepts(0x09) {
		return nil
	}
	c.access.Lock()
	pushing, interval := c.pushing, c.updateInterval
	c.access.Unlock()
	microseconds := uint32(math.MaxUint32)
	if pushing {
		microseconds = uint32(min(max(interval, 0)/time.Microsecond, math.MaxUint32-1))
	}
	return autoFramebufferUpdateMessage(microseconds, width, height)
}

func (c *Client) SetUpdateInterval(interval time.Duration) error {
	c.access.Lock()
	rearm := interval != c.updateInterval && c.pushing
	c.updateInterval = interval
	c.access.Unlock()
	if !rearm {
		return nil
	}
	width, height := c.FramebufferSize()
	arm := c.armMessage(width, height)
	if arm == nil {
		return nil
	}
	return c.send(arm)
}

func (c *Client) watch(done <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		pixels := c.pixelsReceived.Load()
		c.access.Lock()
		pushing := c.pushing
		c.access.Unlock()
		width, height := c.FramebufferSize()
		idle := time.Since(time.Unix(0, c.lastUpdate.Load()))
		switch {
		case !pixels:
			c.send(framebufferUpdateRequestMessage(false, width, height))
		case !pushing && idle > 3*time.Second:
			c.send(framebufferUpdateRequestMessage(true, width, height))
		}
	}
}

func (c *Client) Run() error {
	defer c.handler.StateChanged(StateClosed)
	c.running = true
	c.lastUpdate.Store(time.Now().UnixNano())
	done := make(chan struct{})
	defer close(done)
	go c.watch(done)
	var err error
	for err == nil {
		err = c.receiveMessage()
	}
	closing := c.closed.Load()
	if !closing {
		c.abort(err)
	}
	c.Close()
	if closing && E.IsClosed(err) {
		err = nil
	}
	writerFailed := c.writerError != nil && !errors.Is(c.writerError, net.ErrClosed)
	if writerFailed && (err == nil || errors.Is(err, net.ErrClosed)) {
		err = c.writerError
	}
	return err
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.finishPaste()
		c.closed.Store(true)
		c.outbox.Close(net.ErrClosed)
		<-c.writerDone
		c.conn.Close()
		c.cancel(nil)
	})
	return c.writerError
}

func (c *Client) Abort() error {
	c.abort(context.Canceled)
	return nil
}

func (c *Client) abort(err error) {
	c.closed.Store(true)
	c.cancel(err)
	c.outbox.Abort(err)
	c.conn.Close()
}

func (c *Client) send(message []byte) error {
	return c.outbox.Push(message, false)
}

func (c *Client) SendMessage(message []byte) error {
	return c.outbox.PushWait(message)
}

func (c *Client) QueueMessage(message []byte) error {
	return c.outbox.Push(message, false)
}

func (c *Client) resizeLocked(width, height int) (bool, error) {
	if width <= 0 || height <= 0 {
		return false, E.New("rfb: framebuffer size ", width, "x", height)
	}
	if c.framebuffer != nil && c.width == width && c.height == height {
		return false, nil
	}
	c.logger.Debug("framebuffer resize", "width", width, "height", height)
	c.width, c.height = width, height
	c.size.Store(uint64(width)<<32 | uint64(height))
	framebuffer, err := c.handler.ResizeFramebuffer(width, height)
	if err != nil {
		return false, err
	}
	if framebuffer == nil {
		framebuffer = bgra.New(width, height)
	}
	err = c.checkFramebuffer(framebuffer)
	if err != nil {
		return false, err
	}
	c.framebuffer = framebuffer
	c.decoders.resetMVS(width, height)
	return true, nil
}

func (c *Client) checkFramebuffer(framebuffer *bgra.Image) error {
	err := framebuffer.Validate()
	if err != nil {
		return err
	}
	if framebuffer.Width != c.width || framebuffer.Height != c.height {
		return E.New("rfb: framebuffer is ", framebuffer.Width, "x", framebuffer.Height, ", the session is ", c.width, "x", c.height)
	}
	return nil
}

func (c *Client) SetFramebuffer(framebuffer *bgra.Image) error {
	c.framebufferAccess.Lock()
	err := c.checkFramebuffer(framebuffer)
	if err == nil {
		c.framebuffer = framebuffer
	}
	c.framebufferAccess.Unlock()
	if err != nil {
		return err
	}
	return c.RequestRefresh()
}

func (c *Client) Framebuffer(fn func(*bgra.Image)) {
	c.framebufferAccess.Lock()
	defer c.framebufferAccess.Unlock()
	fn(c.framebuffer)
}

func (c *Client) FramebufferSize() (int, int) {
	size := c.size.Load()
	return int(size >> 32), int(uint32(size))
}

func (c *Client) Statistics() Statistics {
	statistics := Statistics{Updates: c.updates.Load(), Rectangles: c.rectangles.Load()}
	if c.stream.records != nil {
		statistics.Records = c.stream.records.records.Load()
		statistics.WireBytes = c.stream.records.wireBytes.Load()
	}
	return statistics
}

type Statistics struct {
	Updates    uint64
	Rectangles uint64
	Records    uint64
	WireBytes  uint64
}

func (c *Client) receiveMessage() error {
	messageType, err := c.stream.u8()
	if err != nil {
		return err
	}
	switch messageType {
	case 0:
		return c.receiveFramebufferUpdate()
	case 1:
		header, err := c.stream.bytes(5)
		if err != nil {
			return err
		}
		count := binary.BigEndian.Uint16(header[3:])
		return c.stream.discard(int(count) * 6)
	case 2:
		c.handler.Bell()
		return nil
	case 3:
		return c.receiveCutText()
	case 0x14:
		return c.receiveStatus()
	case 0x15:
		return c.receiveAccessibility()
	case 0x1e:
		return c.receiveDropDestination()
	case 0x1f:
		return c.receivePasteboard()
	case 0x20:
		return c.receiveDrag()
	}
	return E.New("rfb: unknown server message type 0x", strconv.FormatUint(uint64(messageType), 16))
}

func (c *Client) receiveFramebufferUpdate() error {
	_, err := c.stream.u8()
	if err != nil {
		return err
	}
	count, err := c.stream.u16()
	if err != nil {
		return err
	}
	c.framebufferAccess.Lock()
	update, err := c.receiveRectangles(int(count))
	c.framebufferAccess.Unlock()
	if err != nil {
		return err
	}
	c.updates.Add(1)
	c.lastUpdate.Store(time.Now().UnixNano())
	if !update.Dirty.Empty() {
		if !c.pixelsReceived.Swap(true) {
			err = c.releaseInput()
			if err != nil {
				return err
			}
		}
		c.handler.FramebufferUpdated(update)
	}
	return c.afterUpdate()
}

func (c *Client) afterUpdate() error {
	if !c.running || c.closed.Load() {
		return nil
	}
	c.access.Lock()
	pushing := c.pushing
	c.access.Unlock()
	if pushing {
		return nil
	}
	return c.send(framebufferUpdateRequestMessage(true, c.width, c.height))
}

func (c *Client) receiveRectangles(count int) (Update, error) {
	var update Update
	for i := 0; i < count; i++ {
		var header [12]byte
		err := c.stream.readFull(header[:])
		if err != nil {
			return update, err
		}
		rectangle := Rectangle{
			X:        int(binary.BigEndian.Uint16(header[0:])),
			Y:        int(binary.BigEndian.Uint16(header[2:])),
			Width:    int(binary.BigEndian.Uint16(header[4:])),
			Height:   int(binary.BigEndian.Uint16(header[6:])),
			Encoding: Encoding(int32(binary.BigEndian.Uint32(header[8:]))),
		}
		if rectangle.Encoding == EncodingLastRect {
			break
		}
		sequence := c.rectangles.Add(1)
		observe := c.options.RectangleObserver != nil
		if observe {
			c.stream.startCapture()
		}
		err = c.receiveRectangle(rectangle)
		if observe {
			payload := c.stream.stopCapture()
			c.options.RectangleObserver(RawRectangle{Sequence: sequence, Rectangle: rectangle, Payload: payload})
		}
		if err != nil {
			return update, E.Cause(err, "read ", rectangle.Encoding, " rectangle ", rectangle)
		}
		if rectangle.Encoding.carriesPixels() && !rectangle.Empty() {
			bounds := image.Rect(rectangle.X, rectangle.Y, rectangle.X+rectangle.Width, rectangle.Y+rectangle.Height).Intersect(c.framebuffer.Bounds())
			clipped := Rectangle{X: bounds.Min.X, Y: bounds.Min.Y, Width: bounds.Dx(), Height: bounds.Dy(), Encoding: rectangle.Encoding}
			update.Dirty = update.Dirty.Union(clipped)
			update.Rectangles = append(update.Rectangles, clipped)
		}
	}
	return update, nil
}

func (c *Client) receiveRectangle(rectangle Rectangle) error {
	switch rectangle.Encoding {
	case EncodingRaw, EncodingCopyRect, EncodingHextile, EncodingZlib, EncodingZRLE,
		EncodingAppleHalftone, EncodingAppleGray, EncodingAppleThousands, EncodingMVS:
		return c.decoders.decode(c.stream, c.framebuffer, rectangle)
	case EncodingDesktopSize:
		return c.resizeAndRefresh(rectangle.Width, rectangle.Height)
	case EncodingExtendedDesktopSize:
		return c.receiveExtendedDesktopSize(rectangle)
	case EncodingDesktopName:
		length, err := c.stream.u32()
		if err != nil {
			return err
		}
		kept := min(length, 1<<16)
		name, err := c.stream.bytes(int(kept))
		if err != nil {
			return err
		}
		err = c.stream.discard(int(length - kept))
		if err != nil {
			return err
		}
		c.info.Name = decodeName(name)
		return nil
	case EncodingRichCursor:
		return c.receiveRichCursor(rectangle)
	case EncodingPointerPosition, EncodingCursorPosition:
		c.handler.CursorMoved(rectangle.X, rectangle.Y)
		return nil
	case EncodingCursorImage:
		return c.receiveCursorImage(rectangle)
	case EncodingRekey:
		return c.receiveRekey()
	case EncodingDisplayLayout:
		return c.receiveDisplayLayout()
	case EncodingDisplayInfo:
		return c.receiveDisplayInfo(rectangle)
	case EncodingKeyboardSource:
		return c.receiveKeyboardSource()
	case EncodingDeviceInfo:
		return c.receiveDeviceInfo()
	case EncodingVendorKeysyms:
		return c.receiveVendorKeysyms()
	case EncodingUserInfo:
		length, err := c.stream.u16()
		if err != nil {
			return err
		}
		return c.stream.discard(int(length))
	default:
		return E.New("rfb: unsupported encoding")
	}
}

func (c *Client) receiveRekey() error {
	body, err := c.stream.bytes(rekeySize)
	if err != nil {
		return err
	}
	generation, keys, err := unwrapRekey(c.wrapKey, body)
	if err != nil {
		return err
	}
	c.logger.Debug("rekey", "generation", generation, "pending", c.pendingRekey)
	c.wrapKey = keys.key
	if !c.pendingRekey {
		return E.New("rfb: unexpected rekey to generation ", generation, " during the session")
	}
	c.rekeyKeys = keys
	err = c.stream.startRecords(keys)
	if err != nil {
		return err
	}
	c.pendingRekey = false
	return nil
}

func (c *Client) resizeAndRefresh(width, height int) error {
	changed, err := c.resizeLocked(width, height)
	if err != nil || !changed || !c.running {
		return err
	}
	return c.send(framebufferUpdateRequestMessage(false, c.width, c.height))
}

func (c *Client) receiveExtendedDesktopSize(rectangle Rectangle) error {
	count, err := c.stream.u8()
	if err != nil {
		return err
	}
	err = c.stream.discard(3 + int(count)*16)
	if err != nil {
		return err
	}
	if rectangle.Y != 0 {
		c.logger.Debug("desktop size change refused", "reason", rectangle.X, "status", rectangle.Y)
		return nil
	}
	return c.resizeAndRefresh(rectangle.Width, rectangle.Height)
}

func (c *Client) RequestRefresh() error {
	width, height := c.FramebufferSize()
	return c.send(framebufferUpdateRequestMessage(false, width, height))
}

func (c *Client) SetQuality(quality Quality) error {
	c.access.Lock()
	c.quality = quality
	c.access.Unlock()
	err := c.send(setEncodingsMessage(c.encodings()))
	if err != nil {
		return err
	}
	return c.RequestRefresh()
}

func (c *Client) SetMode(mode Mode) error {
	c.access.Lock()
	c.mode = mode
	c.access.Unlock()
	if c.info.Dialect != DialectApple {
		return nil
	}
	return c.send(setModeMessage(mode))
}

func (c *Client) Mode() Mode {
	c.access.Lock()
	defer c.access.Unlock()
	return c.mode
}

func (c *Client) SelectDisplay(id uint32) error {
	c.access.Lock()
	defer c.access.Unlock()
	return c.send(setDisplayMessage(id))
}

func (c *Client) SetScale(factor float64) error {
	if !(factor > 0 && factor <= 1) {
		return E.New("rfb: scale ", factor, " is outside (0, 1]")
	}
	message := make([]byte, 10)
	message[0] = 0x08
	binary.BigEndian.PutUint64(message[2:], math.Float64bits(factor))
	return c.send(message)
}

func (c *Client) rearm(width, height int) error {
	for _, message := range c.updateRequests(width, height) {
		err := c.send(message)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) updateRequests(width, height int) [][]byte {
	messages := [][]byte{framebufferUpdateRequestMessage(false, width, height)}
	if arm := c.armMessage(width, height); arm != nil {
		messages = append(messages, arm)
	}
	return messages
}
