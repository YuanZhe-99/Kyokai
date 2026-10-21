// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/device.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"crypto/rand"
	"encoding/binary"
	"unicode/utf16"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	deviceCore             = 0x4472
	deviceServerAnnounce   = 0x496e
	deviceClientIDConfirm  = 0x4343
	deviceClientName       = 0x434e
	deviceListAnnounce     = 0x4441
	deviceServerCapability = 0x5350
	deviceClientCapability = 0x4350
	deviceUserLoggedOn     = 0x554c
	maxDeviceMessage       = 64 << 10

	deviceVersionMajor          = 1
	deviceVersionMinimum        = 2
	deviceVersionCapabilities   = 5
	deviceVersionServerClientID = 12
	deviceVersionMaximum        = 13
	deviceCapabilityGeneral     = 1
	deviceGeneralVersion1       = 1
	deviceGeneralVersion2       = 2
	deviceGeneralLength1        = 40
	deviceGeneralLength2        = 44
	deviceCapabilityHeader      = 8
	deviceExtendedDisplayName   = 0x00000002
	deviceNameUnicode           = 1
	deviceNameMaximumUnits      = 255
)

// Windows 11 26100 sends no audio when the client does not join an RDPDR channel.
type deviceChannel struct {
	send         func([]byte) error
	clientName   []byte
	announced    bool
	confirmed    bool
	capabilities bool
	devicesSent  bool
	minor        uint16
}

func newDeviceChannel(clientName string) *deviceChannel {
	var units []uint16
	for _, character := range clientName {
		if character == 0 || len(units)+utf16.RuneLen(character) > deviceNameMaximumUnits {
			break
		}
		units = utf16.AppendRune(units, character)
	}
	var name []byte
	for _, unit := range append(units, 0) {
		name = binary.LittleEndian.AppendUint16(name, unit)
	}
	return &deviceChannel{clientName: name}
}

func devicePDU(kind uint16, body []byte) []byte {
	return append(binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, deviceCore), kind), body...)
}

func (d *deviceChannel) OnOpen(send func([]byte) error) error {
	d.send = send
	return nil
}

func (d *deviceChannel) OnClose() {
	d.send = nil
}

func (d *deviceChannel) OnData(data []byte) error {
	if len(data) > maxDeviceMessage {
		return E.New("rdp: device channel message of ", len(data), " bytes exceeds ", maxDeviceMessage, " bytes")
	}
	r := &reader{data: data}
	component, kind := r.u16(), r.u16()
	if r.err != nil {
		return r.err
	}
	if component != deviceCore {
		return nil
	}
	switch kind {
	case deviceServerAnnounce:
		major, minor, id := r.u16(), r.u16(), r.u32()
		if r.err != nil {
			return r.err
		}
		if r.remaining() != 0 || major != deviceVersionMajor || minor < deviceVersionMinimum {
			return E.New("rdp: invalid device Server Announce: version ", major, ".", minor, ", ", r.remaining(), " trailing bytes")
		}
		if minor < deviceVersionServerClientID {
			var random [4]byte
			_, _ = rand.Read(random[:])
			id = binary.LittleEndian.Uint32(random[:])
		}
		d.announced, d.confirmed, d.capabilities, d.devicesSent = false, false, false, false
		d.minor = min(minor, deviceVersionMaximum)
		body := binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, deviceVersionMajor), d.minor)
		err := d.send(devicePDU(deviceClientIDConfirm, binary.LittleEndian.AppendUint32(body, id)))
		if err != nil {
			return err
		}
		body = binary.LittleEndian.AppendUint32(nil, deviceNameUnicode)
		body = binary.LittleEndian.AppendUint32(body, 0)
		body = binary.LittleEndian.AppendUint32(body, uint32(len(d.clientName)))
		err = d.send(devicePDU(deviceClientName, append(body, d.clientName...)))
		if err != nil {
			return err
		}
		d.announced = true
		return nil
	case deviceServerCapability:
		if !d.announced {
			return E.New("rdp: device capabilities before announce")
		}
		version, err := deviceCapabilityVersion(r)
		if err != nil {
			return err
		}
		length := uint16(deviceGeneralLength1)
		if version == deviceGeneralVersion2 {
			length = deviceGeneralLength2
		}
		body := binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, 1), 0)
		body = binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(body, deviceCapabilityGeneral), length)
		body = binary.LittleEndian.AppendUint32(body, version)
		body = zeros(body, 8)
		body = binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(body, deviceVersionMajor), d.minor)
		body = zeros(body, 8)
		body = binary.LittleEndian.AppendUint32(body, deviceExtendedDisplayName)
		body = zeros(body, 8)
		if version == deviceGeneralVersion2 {
			body = binary.LittleEndian.AppendUint32(body, 0)
		}
		err = d.send(devicePDU(deviceClientCapability, body))
		if err != nil {
			return err
		}
		d.capabilities = true
		return d.announceEmptyDevices()
	case deviceClientIDConfirm:
		major, minor := r.u16(), r.u16()
		r.take(4)
		if r.err != nil {
			return r.err
		}
		if !d.announced {
			return E.New("rdp: device Client ID Confirm before announce")
		}
		if r.remaining() != 0 || major != deviceVersionMajor || minor < deviceVersionMinimum {
			return E.New("rdp: invalid device Client ID Confirm: version ", major, ".", minor, ", ", r.remaining(), " trailing bytes")
		}
		d.minor = min(minor, deviceVersionMaximum)
		d.confirmed = true
		return d.announceEmptyDevices()
	case deviceUserLoggedOn:
		if r.remaining() != 0 {
			return E.New("rdp: device User Logged On notification has ", r.remaining(), " trailing bytes")
		}
		return nil
	default:
		return nil
	}
}

func deviceCapabilityVersion(r *reader) (uint32, error) {
	count := int(r.u16())
	r.take(2)
	if r.err != nil {
		return 0, r.err
	}
	if count > r.remaining()/deviceCapabilityHeader {
		return 0, E.New("rdp: ", count, " device capabilities do not fit in ", r.remaining(), " bytes")
	}
	version := uint32(deviceGeneralVersion1)
	general := false
	for range count {
		kind, size, offeredVersion := r.u16(), int(r.u16()), r.u32()
		if r.err != nil {
			return 0, r.err
		}
		if size < deviceCapabilityHeader {
			return 0, E.New("rdp: device capability ", kind, " declares ", size, " bytes, less than its ", deviceCapabilityHeader, "-byte header")
		}
		r.take(size - deviceCapabilityHeader)
		if r.err != nil {
			return 0, r.err
		}
		if kind == deviceCapabilityGeneral {
			if general {
				return 0, E.New("rdp: duplicate device general capability")
			}
			if (offeredVersion != deviceGeneralVersion1 && offeredVersion != deviceGeneralVersion2) ||
				(offeredVersion == deviceGeneralVersion1 && size != deviceGeneralLength1) ||
				(offeredVersion == deviceGeneralVersion2 && size != deviceGeneralLength2) {
				return 0, E.New("rdp: invalid device general capability: version ", offeredVersion, ", ", size, " bytes")
			}
			general, version = true, offeredVersion
		}
	}
	if r.remaining() != 0 {
		return 0, E.New("rdp: device capabilities have ", r.remaining(), " trailing bytes")
	}
	return version, nil
}

func (d *deviceChannel) announceEmptyDevices() error {
	if !d.confirmed || (!d.capabilities && d.minor >= deviceVersionCapabilities) || d.devicesSent {
		return nil
	}
	err := d.send(devicePDU(deviceListAnnounce, binary.LittleEndian.AppendUint32(nil, 0)))
	if err != nil {
		return err
	}
	d.devicesSent = true
	return nil
}
