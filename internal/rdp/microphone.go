// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/microphone.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rdp

import (
	"encoding/binary"
	"log/slog"
	"sync"
	"time"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

const (
	microphoneVersion      = 0x01
	microphoneFormats      = 0x02
	microphoneOpen         = 0x03
	microphoneOpenReply    = 0x04
	microphoneDataIncoming = 0x05
	microphoneData         = 0x06
	microphoneFormatChange = 0x07

	microphoneProtocolVersion = 2
	microphoneFormatsHeader   = 8
)

type microphoneChannel struct {
	events  Events
	logger  *slog.Logger
	send    func([]byte) error
	formats []AudioFormat
	sending sync.Mutex

	access  sync.Mutex
	active  *AudioFormat
	frames  int
	pending []byte
	burst   int
	paced   chan struct{}
}

func (m *microphoneChannel) OnOpen(send func([]byte) error) error {
	m.send = send
	return nil
}

func (m *microphoneChannel) OnClose() {
	m.access.Lock()
	open := m.active != nil
	m.active = nil
	m.pending = nil
	if m.paced != nil {
		close(m.paced)
		m.paced = nil
	}
	m.access.Unlock()
	if open {
		m.events.MicrophoneClosed()
	}
}

func (m *microphoneChannel) OnData(data []byte) error {
	r := &reader{data: data}
	kind := r.u8()
	if r.err != nil {
		return r.err
	}
	switch kind {
	case microphoneVersion:
		version := r.u32()
		if r.err != nil {
			return r.err
		}
		return m.reply(binary.LittleEndian.AppendUint32([]byte{microphoneVersion}, min(version, microphoneProtocolVersion)))
	case microphoneFormats:
		return m.negotiate(r)
	case microphoneOpen:
		frames, index := r.u32(), r.u32()
		if r.err != nil {
			return r.err
		}
		format, err := m.start(index, int(frames))
		if err != nil {
			return err
		}
		m.logger.Debug("open microphone", "channels", format.Channels, "rate", format.SampleRate, "frames", frames)
		err = m.reply(binary.LittleEndian.AppendUint32([]byte{microphoneFormatChange}, index))
		if err != nil {
			return err
		}
		err = m.reply(binary.LittleEndian.AppendUint32([]byte{microphoneOpenReply}, 0))
		if err != nil {
			return err
		}
		m.events.MicrophoneOpened(format)
		return nil
	case microphoneFormatChange:
		index := r.u32()
		if r.err != nil {
			return r.err
		}
		m.access.Lock()
		open, frames := m.active != nil, m.frames
		m.access.Unlock()
		if open {
			format, err := m.start(index, frames)
			if err != nil {
				return err
			}
			m.events.MicrophoneOpened(format)
		}
		return m.reply(binary.LittleEndian.AppendUint32([]byte{microphoneFormatChange}, index))
	default:
		return nil
	}
}

func (m *microphoneChannel) negotiate(r *reader) error {
	count := r.u32()
	r.take(4)
	if r.err != nil {
		return r.err
	}
	formats, encoded, err := readPCMFormats(r, int(count))
	if err != nil {
		return err
	}
	m.formats = formats
	err = m.reply([]byte{microphoneDataIncoming})
	if err != nil {
		return err
	}
	body := binary.LittleEndian.AppendUint32([]byte{microphoneFormats}, uint32(len(formats)))
	body = binary.LittleEndian.AppendUint32(body, uint32(1+microphoneFormatsHeader+len(encoded)))
	return m.reply(append(body, encoded...))
}

func (m *microphoneChannel) start(index uint32, frames int) (AudioFormat, error) {
	m.access.Lock()
	defer m.access.Unlock()
	if index >= uint32(len(m.formats)) {
		return AudioFormat{}, E.New("rdp: microphone format index ", index, " is outside the ", len(m.formats), " negotiated formats")
	}
	format := m.formats[index]
	if frames == 0 || frames > format.SampleRate {
		return AudioFormat{}, E.New("rdp: microphone packets of ", frames, " frames, want 1 to ", format.SampleRate)
	}
	m.active = &format
	m.frames = frames
	m.pending = nil
	m.burst = 0
	if m.paced != nil {
		close(m.paced)
	}
	m.paced = make(chan struct{})
	go m.pace(m.paced, m.send, time.Duration(frames)*time.Second/time.Duration(format.SampleRate))
	return format, nil
}

func (m *microphoneChannel) reply(data []byte) error {
	m.sending.Lock()
	defer m.sending.Unlock()
	return m.send(data)
}

func (m *microphoneChannel) write(pcm []byte) {
	m.access.Lock()
	defer m.access.Unlock()
	if m.active == nil {
		return
	}
	m.pending = append(m.pending, pcm...)
	m.burst = max(m.burst, len(pcm))
	if limit := m.active.SampleRate * m.active.Channels * 2; len(m.pending) > limit {
		m.pending = m.pending[len(m.pending)-limit:]
	}
}

func (m *microphoneChannel) pace(stop <-chan struct{}, send func([]byte) error, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		m.access.Lock()
		var packets [][]byte
		if m.active != nil {
			size := m.frames * m.active.Channels * 2
			// rdpendp.dll of Windows 11 26200 drops a whole Data PDU that does not fit in its capture
			// buffer, which the audio engine empties one period at a time, so a backlog goes out at
			// most two packets per packet interval instead of all at once.
			count := min(len(m.pending)/size, 1)
			if len(m.pending) > max(m.burst, size)+2*size {
				count = 2
			}
			for range count {
				packets = append(packets, append([]byte{microphoneData}, m.pending[:size]...))
				m.pending = m.pending[size:]
			}
		}
		m.access.Unlock()
		m.sending.Lock()
		for _, packet := range packets {
			err := send([]byte{microphoneDataIncoming})
			if err == nil {
				err = send(packet)
			}
			if err != nil {
				m.logger.Debug("send microphone audio", "error", err)
				break
			}
		}
		m.sending.Unlock()
	}
}

func (c *Client) SendMicrophoneAudio(pcm []byte) {
	c.microphone.write(pcm)
}
