// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/capabilities.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"encoding/binary"
)

const (
	capabilityGeneral             = 1
	capabilityBitmap              = 2
	capabilityOrder               = 3
	capabilityControl             = 5
	capabilityActivation          = 7
	capabilityPointer             = 8
	capabilityShare               = 9
	capabilityColorCache          = 10
	capabilitySound               = 12
	capabilityInput               = 13
	capabilityFont                = 14
	capabilityBrush               = 15
	capabilityGlyphCache          = 16
	capabilityOffscreenCache      = 17
	capabilityBitmapCacheRev2     = 19
	capabilityVirtualChannel      = 20
	capabilityMultifragmentUpdate = 26
	capabilityLargePointer        = 27
	capabilitySurfaceCommands     = 28
	capabilityBitmapCodecs        = 29
	capabilityFrameAcknowledge    = 30

	osMajorTypeUnix           = 4
	osMinorTypeNativeXServer  = 7
	capabilityProtocolVersion = 0x0200
	fastPathOutputSupported   = 0x0001
	longCredentialsSupported  = 0x0004

	preferredBitsPerPixel = 32
	drawAllowSkipAlpha    = 0x08

	orderCapabilityLength        = 84
	orderDesktopSaveXGranularity = 1
	orderDesktopSaveYGranularity = 20
	orderMaximumLevel            = 1
	orderNegotiateSupport        = 0x0002
	orderZeroBoundsDeltas        = 0x0008

	bitmapCacheAllowWaitingList = 2
	bitmapCacheCellCount        = 3
	bitmapCacheUnusedCells      = 20

	inputFlagScancodes = 0x0001

	glyphCapabilityLength       = 48
	virtualChannelCompressSC    = 1
	soundBeeps                  = 1
	fontSupportList             = 1
	controlPriorityNever        = 2
	colorTableCacheSize         = 6
	largePointer96              = 1
	surfaceSetBits              = 0x02
	surfaceFrameMarker          = 0x10
	surfaceStreamBits           = 0x40
	maximumUnacknowledgedFrames = 2

	nsCodecDynamicFidelity   = 1
	nsCodecSubsampling       = 1
	nsCodecColorLossLevel    = 3
	rfxCaptureNonCAC         = 1
	rfxCapsBlock             = 0xcbc0
	rfxCapsetBlock           = 0xcbc1
	rfxCapsetType            = 0xcfc0
	rfxCodecID               = 1
	rfxVersion               = 0x0100
	rfxTileSize              = 64
	rfxColorConversionICT    = 1
	rfxTransformDWT53A       = 1
	rfxEntropyRLGR1          = 1
	rfxEntropyRLGR3          = 4
	rfxImageCapabilityLength = 8
)

var (
	nsCodecGUID       = []byte{0xb9, 0x1b, 0x8d, 0xca, 0x0f, 0x00, 0x4f, 0x15, 0x58, 0x9f, 0xae, 0x2d, 0x1a, 0x87, 0xe2, 0xd6}
	remoteFXCodecGUID = []byte{0x12, 0x2f, 0x77, 0x76, 0x72, 0xbd, 0x63, 0x44, 0xaf, 0xb3, 0xb7, 0x3c, 0x9c, 0x6f, 0x78, 0x86}
	bitmapCacheCells  = []uint32{120, 120, 336}
)

func (s *session) capabilities() (uint16, []byte) {
	var caps []byte
	var count uint16
	add := func(kind uint16, data []byte) {
		caps = append(caps, lengthBlock(kind, data)...)
		count++
	}
	general := uint16s(osMajorTypeUnix, osMinorTypeNativeXServer, capabilityProtocolVersion, 0, 0,
		fastPathOutputSupported|longCredentialsSupported|noBitmapCompressionHeader, 0, 0, 0)
	add(capabilityGeneral, append(general, 1, 0))
	bitmap := uint16s(preferredBitsPerPixel, 1, 1, 1, uint16(s.graphics.width.Load()), uint16(s.graphics.height.Load()), 0, 1, 1)
	bitmap = append(bitmap, 0, drawAllowSkipAlpha)
	add(capabilityBitmap, append(bitmap, uint16s(1, 0)...))
	order := make([]byte, orderCapabilityLength)
	binary.LittleEndian.PutUint16(order[20:], orderDesktopSaveXGranularity)
	binary.LittleEndian.PutUint16(order[22:], orderDesktopSaveYGranularity)
	binary.LittleEndian.PutUint16(order[26:], orderMaximumLevel)
	binary.LittleEndian.PutUint16(order[30:], orderNegotiateSupport|orderZeroBoundsDeltas)
	add(capabilityOrder, order)
	cache := binary.LittleEndian.AppendUint16(nil, bitmapCacheAllowWaitingList)
	cache = append(cache, 0, bitmapCacheCellCount)
	for _, cell := range bitmapCacheCells {
		cache = binary.LittleEndian.AppendUint32(cache, cell)
	}
	add(capabilityBitmapCacheRev2, zeros(cache, bitmapCacheUnusedCells))
	add(capabilityPointer, uint16s(1, pointerCacheSize, pointerCacheSize))
	input := uint16s(inputFlagScancodes|inputFlagFastPath2|inputFlagUnicode|inputFlagMouseHWheel, 0)
	input = binary.LittleEndian.AppendUint32(input, s.keyboardLayout)
	input = binary.LittleEndian.AppendUint32(input, keyboardTypeEnhanced)
	input = binary.LittleEndian.AppendUint32(input, 0)
	input = binary.LittleEndian.AppendUint32(input, keyboardFunctionKeys)
	add(capabilityInput, zeros(input, imeFileNameLength))
	add(capabilityBrush, binary.LittleEndian.AppendUint32(nil, 0))
	add(capabilityGlyphCache, make([]byte, glyphCapabilityLength))
	add(capabilityVirtualChannel, binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, virtualChannelCompressSC), channelChunkLength))
	add(capabilitySound, uint16s(soundBeeps, 0))
	add(capabilityShare, uint16s(0, 0))
	add(capabilityFont, uint16s(fontSupportList, 0))
	add(capabilityControl, uint16s(0, 0, controlPriorityNever, controlPriorityNever))
	add(capabilityColorCache, uint16s(colorTableCacheSize, 0))
	add(capabilityActivation, make([]byte, 8))
	add(capabilityOffscreenCache, make([]byte, 8))
	if s.serverCaps[capabilityMultifragmentUpdate] {
		add(capabilityMultifragmentUpdate, binary.LittleEndian.AppendUint32(nil, maxFastPathUpdate))
	}
	if s.serverCaps[capabilityLargePointer] {
		add(capabilityLargePointer, uint16s(largePointer96))
	}
	if s.serverCaps[capabilitySurfaceCommands] {
		add(capabilitySurfaceCommands, binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, surfaceSetBits|surfaceFrameMarker|surfaceStreamBits), 0))
	}
	if s.serverCaps[capabilityBitmapCodecs] {
		add(capabilityBitmapCodecs, bitmapCodecs())
	}
	if s.serverCaps[capabilityFrameAcknowledge] {
		add(capabilityFrameAcknowledge, binary.LittleEndian.AppendUint32(nil, maximumUnacknowledgedFrames))
	}
	return count, caps
}

func bitmapCodecs() []byte {
	b := append([]byte{2}, nsCodecGUID...)
	b = append(b, codecIDNSCodec)
	b = binary.LittleEndian.AppendUint16(b, 3)
	b = append(b, nsCodecDynamicFidelity, nsCodecSubsampling, nsCodecColorLossLevel)
	var icaps []byte
	for _, entropy := range []byte{rfxEntropyRLGR1, rfxEntropyRLGR3} {
		icaps = binary.LittleEndian.AppendUint16(icaps, rfxVersion)
		icaps = binary.LittleEndian.AppendUint16(icaps, rfxTileSize)
		icaps = append(icaps, 0, rfxColorConversionICT, rfxTransformDWT53A, entropy)
	}
	capset := binary.LittleEndian.AppendUint16(nil, rfxCapsetBlock)
	capset = binary.LittleEndian.AppendUint32(capset, uint32(13+len(icaps)))
	capset = append(capset, rfxCodecID)
	capset = binary.LittleEndian.AppendUint16(capset, rfxCapsetType)
	capset = binary.LittleEndian.AppendUint16(capset, uint16(len(icaps)/rfxImageCapabilityLength))
	capset = binary.LittleEndian.AppendUint16(capset, rfxImageCapabilityLength)
	capset = append(capset, icaps...)
	caps := binary.LittleEndian.AppendUint16(nil, rfxCapsBlock)
	caps = binary.LittleEndian.AppendUint32(caps, 8)
	caps = binary.LittleEndian.AppendUint16(caps, 1)
	caps = append(caps, capset...)
	container := binary.LittleEndian.AppendUint32(nil, uint32(12+len(caps)))
	container = binary.LittleEndian.AppendUint32(container, rfxCaptureNonCAC)
	container = binary.LittleEndian.AppendUint32(container, uint32(len(caps)))
	container = append(container, caps...)
	b = append(b, remoteFXCodecGUID...)
	b = append(b, codecIDRemoteFX)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(container)))
	return append(b, container...)
}
