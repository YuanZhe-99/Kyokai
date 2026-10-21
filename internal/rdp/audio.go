// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/audio.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"encoding/binary"
	"sync/atomic"
	"time"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	audioClose       = 0x01
	audioWave        = 0x02
	audioSetVolume   = 0x03
	audioSetPitch    = 0x04
	audioWaveConfirm = 0x05
	audioTraining    = 0x06
	audioFormats     = 0x07
	audioQualityMode = 0x0C
	audioWave2       = 0x0D

	audioVersion            = 8
	audioQualityModeVersion = 6
	audioWaveInfoLength     = 12
	audioWaveInfoPrefix     = 4
	audioWave2HeaderLength  = 12
	audioFormatMinimum      = 18
	audioFormatsHeader      = 14
	audioControlLength      = 4
	audioTrainingLength     = 4

	audioCapabilityAlive = 0x00000001
	audioVolumeMaximum   = 0xffffffff
	audioQualityHigh     = 2
	waveFormatPCM        = 1
	audioBitsPerSample   = 16
	audioMinimumRate     = 8000
	audioMaximumRate     = 192000
)

type audioPendingWave struct {
	format    uint16
	timestamp uint16
	audioTime uint32
	timeBits  uint
	block     byte
	dataSize  int
	prefix    [audioWaveInfoPrefix]byte
	arrival   time.Time
}

type audioChannel struct {
	events  Events
	changed func()

	available atomic.Bool
	send      func([]byte) error
	formats   []AudioFormat
	active    AudioFormat
	announced bool
	pending   *audioPendingWave
	clock     audioTimestampClock
}

type audioTimestampClock struct {
	initialized bool
	raw         uint32
	bits        uint
	arrival     time.Time
	elapsed     uint64
}

func (c *audioChannel) OnOpen(send func([]byte) error) error {
	c.send = send
	return nil
}

func (c *audioChannel) OnClose() {
	c.send = nil
	c.pending = nil
	if c.available.Swap(false) {
		c.changed()
	}
}

func (c *audioChannel) OnData(data []byte) error {
	if c.pending != nil {
		wave := c.pending
		c.pending = nil
		if len(data) != wave.dataSize {
			return E.New("rdp: audio wave data of ", len(data), " bytes, want ", wave.dataSize)
		}
		pcm := append([]byte(nil), data...)
		copy(pcm, wave.prefix[:])
		return c.deliver(wave, pcm)
	}
	r := &reader{data: data}
	kind := r.u8()
	r.take(1)
	bodySize := int(r.u16())
	if r.err != nil {
		return r.err
	}
	if kind == audioWave {
		if bodySize < audioWaveInfoLength {
			return E.New("rdp: audio wave of ", bodySize, " bytes is shorter than its ", audioWaveInfoLength, "-byte header")
		}
		if r.remaining() != audioWaveInfoLength {
			return E.New("rdp: audio wave info of ", r.remaining(), " bytes, want ", audioWaveInfoLength)
		}
		wave := &audioPendingWave{
			timestamp: r.u16(),
			format:    r.u16(),
			block:     r.u8(),
			dataSize:  bodySize - audioWaveInfoLength + audioWaveInfoPrefix,
			arrival:   time.Now(),
		}
		r.take(3)
		wave.audioTime = uint32(wave.timestamp)
		wave.timeBits = 16
		copy(wave.prefix[:], r.take(audioWaveInfoPrefix))
		err := c.validateWave(wave)
		if err != nil {
			return err
		}
		c.pending = wave
		return nil
	}
	if r.remaining() != bodySize {
		return E.New("rdp: audio message ", kind, " of ", r.remaining(), " bytes, header declares ", bodySize)
	}
	switch kind {
	case audioFormats:
		return c.negotiate(r)
	case audioTraining:
		body := r.take(audioTrainingLength)
		if r.err != nil {
			return r.err
		}
		return c.send(audioPDU(audioTraining, body))
	case audioWave2:
		if bodySize < audioWave2HeaderLength {
			return E.New("rdp: audio wave2 of ", bodySize, " bytes is shorter than its ", audioWave2HeaderLength, "-byte header")
		}
		wave := &audioPendingWave{
			timestamp: r.u16(),
			format:    r.u16(),
			block:     r.u8(),
			dataSize:  bodySize - audioWave2HeaderLength,
			arrival:   time.Now(),
		}
		r.take(3)
		wave.audioTime = r.u32()
		wave.timeBits = 32
		err := c.validateWave(wave)
		if err != nil {
			return err
		}
		return c.deliver(wave, append([]byte(nil), r.rest()...))
	case audioClose:
		if bodySize != 0 {
			return E.New("rdp: audio close of ", bodySize, " bytes, want 0")
		}
		c.announced = false
	case audioSetVolume, audioSetPitch:
		if bodySize != audioControlLength {
			return E.New("rdp: audio control message ", kind, " of ", bodySize, " bytes, want ", audioControlLength)
		}
	}
	return nil
}

func (c *audioChannel) negotiate(r *reader) error {
	if c.available.Swap(false) {
		c.changed()
	}
	c.formats = nil
	c.announced = false
	c.clock = audioTimestampClock{}
	r.take(audioFormatsHeader)
	count := int(r.u16())
	r.take(1)
	version := r.u16()
	r.take(1)
	if r.err != nil {
		return r.err
	}
	formats, encoded, err := readPCMFormats(r, count)
	if err != nil {
		return err
	}
	if r.remaining() != 0 {
		return E.New("rdp: ", r.remaining(), " bytes after the audio formats")
	}
	body := binary.LittleEndian.AppendUint32(nil, audioCapabilityAlive)
	body = binary.LittleEndian.AppendUint32(body, audioVolumeMaximum)
	body = binary.LittleEndian.AppendUint32(body, 0)
	body = binary.LittleEndian.AppendUint16(body, 0)
	body = binary.LittleEndian.AppendUint16(body, uint16(len(formats)))
	body = append(body, 0)
	body = binary.LittleEndian.AppendUint16(body, min(version, audioVersion))
	body = append(body, 0)
	body = append(body, encoded...)
	err = c.send(audioPDU(audioFormats, body))
	if err != nil {
		return err
	}
	if version >= audioQualityModeVersion {
		err = c.send(audioPDU(audioQualityMode, binary.LittleEndian.AppendUint16(binary.LittleEndian.AppendUint16(nil, audioQualityHigh), 0)))
		if err != nil {
			return err
		}
	}
	c.formats = formats
	if len(formats) != 0 {
		c.available.Store(true)
		c.changed()
	}
	return nil
}

func (c *audioChannel) validateWave(wave *audioPendingWave) error {
	if int(wave.format) >= len(c.formats) {
		return E.New("rdp: audio format index ", wave.format, " is outside the ", len(c.formats), " negotiated formats")
	}
	if frame := c.formats[wave.format].Channels * 2; wave.dataSize%frame != 0 {
		return E.New("rdp: audio wave of ", wave.dataSize, " bytes is not a whole number of ", frame, "-byte frames")
	}
	return nil
}

func (c *audioChannel) deliver(wave *audioPendingWave, pcm []byte) error {
	if len(pcm) != 0 {
		format := c.formats[wave.format]
		if !c.announced || c.active != format {
			c.active = format
			c.announced = true
			c.events.AudioFormatChanged(format)
		}
		milliseconds := c.clock.extend(wave.audioTime, wave.timeBits, wave.arrival)
		c.events.AudioPacketReceived(pcm, milliseconds*uint64(format.SampleRate)/1000)
	}
	body := binary.LittleEndian.AppendUint16(nil, wave.timestamp+uint16(time.Since(wave.arrival).Milliseconds()))
	body = append(body, wave.block, 0)
	return c.send(audioPDU(audioWaveConfirm, body))
}

func (c *audioTimestampClock) extend(raw uint32, bits uint, arrival time.Time) uint64 {
	if c.initialized {
		period := int64(1) << min(bits, c.bits)
		delta := int64((uint64(raw) - uint64(c.raw)) & uint64(period-1))
		expected := max(int64(0), arrival.Sub(c.arrival).Milliseconds())
		offset := expected - delta
		if offset >= 0 {
			delta += ((offset + period/2) / period) * period
		} else {
			delta -= ((-offset + period/2) / period) * period
		}
		c.elapsed += uint64(max(int64(0), delta))
	}
	c.initialized = true
	c.raw = raw
	c.bits = bits
	c.arrival = arrival
	return c.elapsed
}

func readPCMFormats(r *reader, count int) ([]AudioFormat, []byte, error) {
	if count > r.remaining()/audioFormatMinimum {
		return nil, nil, E.New("rdp: ", count, " audio formats do not fit in ", r.remaining(), " bytes")
	}
	var formats []AudioFormat
	var encoded []byte
	for range count {
		tag := r.u16()
		channels := r.u16()
		sampleRate := r.u32()
		bytesPerSecond := r.u32()
		blockAlign := r.u16()
		bits := r.u16()
		r.take(int(r.u16()))
		if r.err != nil {
			return nil, nil, r.err
		}
		if tag != waveFormatPCM || bits != audioBitsPerSample || (channels != 1 && channels != 2) ||
			sampleRate < audioMinimumRate || sampleRate > audioMaximumRate ||
			blockAlign != channels*2 || bytesPerSecond != sampleRate*uint32(blockAlign) {
			continue
		}
		formats = append(formats, AudioFormat{
			Channels:      int(channels),
			SampleRate:    int(sampleRate),
			BitsPerSample: int(bits),
		})
		encoded = binary.LittleEndian.AppendUint16(encoded, waveFormatPCM)
		encoded = binary.LittleEndian.AppendUint16(encoded, channels)
		encoded = binary.LittleEndian.AppendUint32(encoded, sampleRate)
		encoded = binary.LittleEndian.AppendUint32(encoded, bytesPerSecond)
		encoded = binary.LittleEndian.AppendUint16(encoded, blockAlign)
		encoded = binary.LittleEndian.AppendUint16(encoded, bits)
		encoded = binary.LittleEndian.AppendUint16(encoded, 0)
	}
	return formats, encoded, nil
}

func audioPDU(kind byte, body []byte) []byte {
	pdu := binary.LittleEndian.AppendUint16([]byte{kind, 0}, uint16(len(body)))
	return append(pdu, body...)
}
