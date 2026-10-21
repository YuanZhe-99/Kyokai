// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/mcs.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: UDP multitransport removed, standard-library helpers in place of sing.

package rdp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/YuanZhe-99/Kyokai/internal/outbox"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	mcsErectDomainRequest          = 1
	mcsDisconnectProviderUltimatum = 8
	mcsAttachUserRequest           = 10
	mcsAttachUserConfirm           = 11
	mcsChannelJoinRequest          = 14
	mcsChannelJoinConfirm          = 15
	mcsSendDataRequest             = 25
	mcsSendDataIndication          = 26
	mcsInitiatorPresent            = 0x02
	mcsChannelIDPresent            = 0x02
	mcsUserIDBase                  = 1001
	mcsReasonUserRequested         = 3

	mcsPriorityHigh  = 0x40
	mcsSegmentBegin  = 0x20
	mcsSegmentEnd    = 0x10
	mcsLengthLong    = 0x80
	mcsMaximumLength = 0x7fff
	mcsMaximumData   = 16 << 20

	berTagInteger     = 0x02
	berTagOctetString = 0x04
	berTagEnumerated  = 0x0a
	berTagSequence    = 0x30
	berLengthLong     = 0x80

	gccConferenceCreateResponse = 0x14

	userDataClientCore     = 0xc001
	userDataClientSecurity = 0xc002
	userDataClientNetwork  = 0xc003
	userDataClientMessage  = 0xc006
	userDataServerCore     = 0x0c01
	userDataServerSecurity = 0x0c02
	userDataServerNetwork  = 0x0c03
	userDataServerMessage  = 0x0c04

	rdpVersion5Plus              = 0x00080004
	colorDepth8                  = 0xca01
	secureAccessSequenceDelete   = 0xaa03
	clientBuild                  = 26000
	keyboardTypeEnhanced         = 4
	keyboardFunctionKeys         = 12
	clientProductID              = 1
	highColorDepth24             = 24
	supportedColorDepthsAll      = 0x000f
	earlySupportErrorInfo        = 0x0001
	earlyWant32BPP               = 0x0002
	earlyValidConnectionType     = 0x0020
	earlySupportNetcharDetect    = 0x0080
	earlySupportDynamicGFX       = 0x0100
	connectionTypeAutodetect     = 7
	deviceScaleFactor            = 100
	clientNameLength             = 32
	imeFileNameLength            = 64
	clientDigitalProductIDLength = 64
	clientChannelNameLength      = 8
	encryptionMethodNone         = 0
)

var (
	mcsConnectInitial  = []byte{0x7f, 0x65}
	mcsConnectResponse = []byte{0x7f, 0x66}
	mcsDomainSelector  = []byte{berTagOctetString, 1, 1}
	mcsUpwardFlag      = []byte{0x01, 1, 0xff}
	mcsTargetDomain    = []uint32{34, 2, 0, 1, 0, 1, 65535, 2}
	mcsMinimumDomain   = []uint32{1, 1, 1, 1, 0, 1, 1056, 2}
	mcsMaximumDomain   = []uint32{65535, 64535, 65535, 1, 0, 1, 65535, 2}

	t124ObjectIdentifier        = []byte{0, 5, 0, 0x14, 0x7c, 0, 1}
	gccConferenceCreateRequest  = []byte{0, 8, 0, 0x10, 0, 1, 0xc0, 0, 'D', 'u', 'c', 'a'}
	gccConferenceResponseHeader = []byte{1, 0xc0, 0, 'M', 'c', 'D', 'n'}
)

type mcsLayer struct {
	transport *transport
	outbox    *outbox.Outbox
	userID    uint16
	global    uint16
	message   uint16
	joined    []uint16
	fragments map[uint16][]byte
}

func berValue(tag []byte, value []byte) []byte {
	b := append([]byte(nil), tag...)
	switch {
	case len(value) < berLengthLong:
		b = append(b, byte(len(value)))
	case len(value) < 256:
		b = append(b, berLengthLong|1, byte(len(value)))
	default:
		b = append(b, berLengthLong|2, byte(len(value)>>8), byte(len(value)))
	}
	return append(b, value...)
}

func berInteger(n uint32) []byte {
	b := []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	for len(b) > 1 && b[0] == 0 && b[1]&0x80 == 0 {
		b = b[1:]
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return berValue([]byte{berTagInteger}, b)
}

func domainParameters(values []uint32) []byte {
	var b []byte
	for _, value := range values {
		b = append(b, berInteger(value)...)
	}
	return berValue([]byte{berTagSequence}, b)
}

func perLength(b []byte, n int) []byte {
	if n < mcsLengthLong {
		return append(b, byte(n))
	}
	return append(b, byte(n>>8)|mcsLengthLong, byte(n))
}

func readPERLength(r *reader) int {
	n := int(r.u8())
	if n&mcsLengthLong != 0 {
		n = (n&^mcsLengthLong)<<8 | int(r.u8())
	}
	return n
}

func readBER(r *reader, expected ...byte) *reader {
	for _, tag := range expected {
		if actual := r.u8(); actual != tag && r.err == nil {
			r.err = E.New("rdp: unexpected BER tag byte 0x", strconv.FormatUint(uint64(actual), 16), ", expected 0x", strconv.FormatUint(uint64(tag), 16))
		}
	}
	n := int(r.u8())
	if n&berLengthLong != 0 {
		count := n &^ berLengthLong
		if count == 0 || count > 4 {
			r.err = E.New("rdp: unsupported BER length of ", count, " bytes")
			return r.sub(0)
		}
		n = 0
		for range count {
			n = n<<8 | int(r.u8())
		}
	}
	return r.sub(n)
}

func clientUserData(opts Options, protocol uint32, channels []*staticChannel) []byte {
	core := binary.LittleEndian.AppendUint32(nil, rdpVersion5Plus)
	core = binary.LittleEndian.AppendUint16(core, uint16(opts.Width))
	core = binary.LittleEndian.AppendUint16(core, uint16(opts.Height))
	core = binary.LittleEndian.AppendUint16(core, colorDepth8)
	core = binary.LittleEndian.AppendUint16(core, secureAccessSequenceDelete)
	core = binary.LittleEndian.AppendUint32(core, opts.KeyboardLayout)
	core = binary.LittleEndian.AppendUint32(core, clientBuild)
	core = fixedUTF16(core, opts.ClientName, clientNameLength)
	core = binary.LittleEndian.AppendUint32(core, keyboardTypeEnhanced)
	core = binary.LittleEndian.AppendUint32(core, 0)
	core = binary.LittleEndian.AppendUint32(core, keyboardFunctionKeys)
	core = zeros(core, imeFileNameLength)
	core = binary.LittleEndian.AppendUint16(core, colorDepth8)
	core = binary.LittleEndian.AppendUint16(core, clientProductID)
	core = binary.LittleEndian.AppendUint32(core, 0)
	core = binary.LittleEndian.AppendUint16(core, highColorDepth24)
	core = binary.LittleEndian.AppendUint16(core, supportedColorDepthsAll)
	core = binary.LittleEndian.AppendUint16(core, earlySupportErrorInfo|earlyWant32BPP|earlyValidConnectionType|earlySupportNetcharDetect|earlySupportDynamicGFX)
	core = zeros(core, clientDigitalProductIDLength)
	core = append(core, connectionTypeAutodetect, 0)
	core = binary.LittleEndian.AppendUint32(core, protocol)
	core = binary.LittleEndian.AppendUint32(core, 0)
	core = binary.LittleEndian.AppendUint32(core, 0)
	core = binary.LittleEndian.AppendUint16(core, 0)
	core = binary.LittleEndian.AppendUint32(core, uint32(opts.DesktopScaleFactor))
	core = binary.LittleEndian.AppendUint32(core, deviceScaleFactor)
	network := binary.LittleEndian.AppendUint32(nil, uint32(len(channels)))
	for _, channel := range channels {
		name := make([]byte, clientChannelNameLength)
		copy(name, channel.name)
		network = append(network, name...)
		network = binary.LittleEndian.AppendUint32(network, channel.options)
	}
	data := lengthBlock(userDataClientCore, core)
	data = append(data, lengthBlock(userDataClientSecurity, binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, encryptionMethodNone), encryptionMethodNone))...)
	data = append(data, lengthBlock(userDataClientNetwork, network)...)
	data = append(data, lengthBlock(userDataClientMessage, binary.LittleEndian.AppendUint32(nil, 0))...)
	b := perLength(slices.Clone(t124ObjectIdentifier), len(data)+len(gccConferenceCreateRequest)+2)
	b = append(b, gccConferenceCreateRequest...)
	b = perLength(b, len(data))
	return append(b, data...)
}

func (m *mcsLayer) connect(opts Options, channels []*staticChannel) error {
	b := slices.Concat(mcsDomainSelector, mcsDomainSelector, mcsUpwardFlag)
	b = append(b, domainParameters(mcsTargetDomain)...)
	b = append(b, domainParameters(mcsMinimumDomain)...)
	b = append(b, domainParameters(mcsMaximumDomain)...)
	b = append(b, berValue([]byte{berTagOctetString}, clientUserData(opts, m.transport.protocol, channels))...)
	err := m.outbox.Push(tpkt(berValue(mcsConnectInitial, b)), false)
	if err != nil {
		return err
	}
	packet, err := m.readHandshake()
	if err != nil {
		return err
	}
	err = m.parseConnectResponse(packet, channels)
	if err != nil {
		return err
	}
	erect := tpkt([]byte{mcsErectDomainRequest << 2, 1, 0, 1, 0})
	err = m.outbox.Push(append(erect, tpkt([]byte{mcsAttachUserRequest << 2})...), false)
	if err != nil {
		return err
	}
	packet, err = m.readHandshake()
	if err != nil {
		return err
	}
	r := &reader{data: packet}
	tag, result := r.u8(), r.u8()
	if tag != mcsAttachUserConfirm<<2|mcsInitiatorPresent || result != 0 {
		return E.New("rdp: MCS attach user rejected: PDU 0x", strconv.FormatUint(uint64(tag), 16), ", result ", result)
	}
	m.userID = r.u16be() + mcsUserIDBase
	if r.err != nil {
		return r.err
	}
	for _, id := range slices.Concat([]uint16{m.userID, m.global}, m.joined) {
		request := binary.BigEndian.AppendUint16([]byte{mcsChannelJoinRequest << 2}, m.userID-mcsUserIDBase)
		request = binary.BigEndian.AppendUint16(request, id)
		err = m.outbox.Push(tpkt(request), false)
		if err != nil {
			return err
		}
		packet, err = m.readHandshake()
		if err != nil {
			return err
		}
		r = &reader{data: packet}
		tag := r.u8()
		result := r.u8()
		initiator := r.u16be() + mcsUserIDBase
		requested := r.u16be()
		if r.err != nil {
			return r.err
		}
		if tag&^mcsChannelIDPresent != mcsChannelJoinConfirm<<2 || result != 0 || initiator != m.userID || requested != id {
			return E.New("rdp: MCS join channel ", id, " rejected: PDU 0x", strconv.FormatUint(uint64(tag), 16), ", result ", result)
		}
		if tag&mcsChannelIDPresent != 0 {
			if joined := r.u16be(); r.err == nil && joined != id {
				return E.New("rdp: MCS joined channel ", joined, " instead of ", id)
			}
		}
		if r.err != nil {
			return r.err
		}
	}
	return nil
}

func (m *mcsLayer) readHandshake() ([]byte, error) {
	fast, _, p, err := m.transport.readPacket()
	if err != nil {
		return nil, err
	}
	if fast {
		return nil, E.New("rdp: unexpected fast-path packet during MCS handshake")
	}
	return p, nil
}

func (m *mcsLayer) parseConnectResponse(packet []byte, channels []*staticChannel) error {
	r := &reader{data: packet}
	body := readBER(r, mcsConnectResponse...)
	result := readBER(body, berTagEnumerated).rest()
	if len(result) != 1 || result[0] != 0 {
		return E.New("rdp: MCS connection rejected with result 0x", hex.EncodeToString(result))
	}
	readBER(body, berTagInteger).rest()
	readBER(body, berTagSequence).rest()
	gcc := readBER(body, berTagOctetString)
	err := E.Errors(r.err, body.err, gcc.err)
	if err != nil {
		return err
	}
	if identifier := gcc.take(len(t124ObjectIdentifier)); !bytes.Equal(identifier, t124ObjectIdentifier) {
		return E.New("rdp: GCC object identifier 0x", hex.EncodeToString(identifier), " is not T.124")
	}
	// Some Windows versions report this length as 0x2a regardless of the actual size.
	readPERLength(gcc)
	if choice := gcc.u8(); choice != gccConferenceCreateResponse {
		return E.New("rdp: GCC response choice 0x", strconv.FormatUint(uint64(choice), 16), " is not ConferenceCreateResponse")
	}
	gcc.u16be()
	gcc.take(readPERLength(gcc))
	if code := gcc.u8(); code != 0 {
		return E.New("rdp: GCC conference rejected with result ", code)
	}
	if header := gcc.take(len(gccConferenceResponseHeader)); !bytes.Equal(header, gccConferenceResponseHeader) {
		return E.New("rdp: unexpected GCC conference response header 0x", hex.EncodeToString(header))
	}
	blocks := gcc.sub(readPERLength(gcc))
	if gcc.err != nil {
		return gcc.err
	}
	foundNetwork := false
	for blocks.remaining() > 0 && blocks.err == nil {
		kind := blocks.u16()
		size := int(blocks.u16())
		if size < 4 {
			return E.New("rdp: GCC block 0x", strconv.FormatUint(uint64(kind), 16), " declares length ", size)
		}
		data := blocks.sub(size - 4)
		switch kind {
		case userDataServerCore:
			data.u32()
			if requested := data.u32(); data.err == nil && requested != requestedProtocols {
				return E.New("rdp: the security negotiation was altered: the server received protocols 0x", strconv.FormatUint(uint64(requested), 16), " instead of 0x", strconv.FormatUint(requestedProtocols, 16))
			}
		case userDataServerSecurity:
			method, level := data.u32(), data.u32()
			if method != encryptionMethodNone || level != 0 {
				return E.New("rdp: standard RDP encryption is unsupported: method 0x", strconv.FormatUint(uint64(method), 16), ", level ", level)
			}
		case userDataServerNetwork:
			m.global = data.u16()
			count := int(data.u16())
			if count != len(channels) {
				return E.New("rdp: server assigned ", count, " channels, requested ", len(channels))
			}
			for _, channel := range channels {
				id := data.u16()
				if id == 0 || id == m.global || slices.Contains(m.joined, id) {
					return E.New("rdp: invalid static channel ID ", id)
				}
				channel.id = id
				m.joined = append(m.joined, id)
			}
			foundNetwork = true
		case userDataServerMessage:
			m.message = data.u16()
		}
		if data.err != nil {
			return data.err
		}
	}
	if blocks.err != nil {
		return blocks.err
	}
	if !foundNetwork || m.global == 0 {
		return E.New("rdp: missing server channel allocation")
	}
	if m.message != 0 {
		if m.message == m.global || slices.Contains(m.joined, m.message) {
			return E.New("rdp: invalid message channel ID ", m.message)
		}
		m.joined = append(m.joined, m.message)
	}
	return nil
}

func (m *mcsLayer) packet(channel uint16, payload []byte) ([]byte, error) {
	if len(payload) > mcsMaximumLength {
		return nil, E.New("rdp: MCS payload of ", len(payload), " bytes exceeds ", mcsMaximumLength)
	}
	b := binary.BigEndian.AppendUint16([]byte{mcsSendDataRequest << 2}, m.userID-mcsUserIDBase)
	b = binary.BigEndian.AppendUint16(b, channel)
	b = append(b, mcsPriorityHigh|mcsSegmentBegin|mcsSegmentEnd)
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload))|mcsLengthLong<<8)
	b = append(b, payload...)
	return tpkt(b), nil
}

func (m *mcsLayer) send(channel uint16, payload []byte) error {
	packet, err := m.packet(channel, payload)
	if err != nil {
		return err
	}
	return m.outbox.Push(packet, false)
}

func (m *mcsLayer) receive(packet []byte) (uint16, []byte, error) {
	r := &reader{data: packet}
	tag := r.u8()
	if tag>>2 == mcsDisconnectProviderUltimatum {
		return 0, nil, errDisconnected
	}
	if tag != mcsSendDataIndication<<2 {
		return 0, nil, E.New("rdp: unexpected MCS PDU 0x", strconv.FormatUint(uint64(tag), 16))
	}
	r.u16be()
	channel := r.u16be()
	segmentation := r.u8() & (mcsSegmentBegin | mcsSegmentEnd)
	payload := r.take(readPERLength(r))
	if r.err != nil {
		return 0, nil, r.err
	}
	if channel != m.global && !slices.Contains(m.joined, channel) {
		return 0, nil, E.New("rdp: data for unassigned channel ", channel)
	}
	if segmentation == mcsSegmentBegin|mcsSegmentEnd {
		return channel, payload, nil
	}
	if segmentation&mcsSegmentBegin != 0 {
		m.fragments[channel] = nil
	}
	fragment, ok := m.fragments[channel]
	if !ok {
		return 0, nil, E.New("rdp: MCS continuation on channel ", channel, " without first fragment")
	}
	if len(fragment)+len(payload) > mcsMaximumData {
		return 0, nil, E.New("rdp: MCS message on channel ", channel, " exceeds the 16 MiB limit")
	}
	fragment = append(fragment, payload...)
	if segmentation&mcsSegmentEnd == 0 {
		m.fragments[channel] = fragment
		return channel, nil, nil
	}
	delete(m.fragments, channel)
	return channel, fragment, nil
}
