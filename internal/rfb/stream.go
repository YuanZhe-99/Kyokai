// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/stream.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

package rfb

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
)

type stream struct {
	raw     *bufio.Reader
	source  io.Reader
	records *recordReader

	capturing bool
	captured  []byte
	scratch   [8]byte
}

func newStream(conn net.Conn) *stream {
	raw := bufio.NewReaderSize(conn, 256*1024)
	return &stream{raw: raw, source: raw}
}

func (s *stream) startRecords(keys recordKeys) error {
	records := &recordReader{source: s.raw}
	err := records.decoder.rekey(keys)
	if err != nil {
		return err
	}
	s.records = records
	s.source = records
	return nil
}

func (s *stream) Read(data []byte) (int, error) {
	err := s.readFull(data)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *stream) readFull(data []byte) error {
	_, err := io.ReadFull(s.source, data)
	if err != nil {
		return err
	}
	if s.capturing {
		s.captured = append(s.captured, data...)
	}
	return nil
}

func (s *stream) bytes(length int) ([]byte, error) {
	data := make([]byte, length)
	err := s.readFull(data)
	return data, err
}

func (s *stream) u8() (uint8, error) {
	err := s.readFull(s.scratch[:1])
	return s.scratch[0], err
}

func (s *stream) u16() (uint16, error) {
	err := s.readFull(s.scratch[:2])
	return binary.BigEndian.Uint16(s.scratch[:2]), err
}

func (s *stream) u32() (uint32, error) {
	err := s.readFull(s.scratch[:4])
	return binary.BigEndian.Uint32(s.scratch[:4]), err
}

func (s *stream) discard(length int) error {
	var buffer [4096]byte
	for length > 0 {
		chunk := min(length, len(buffer))
		err := s.readFull(buffer[:chunk])
		if err != nil {
			return err
		}
		length -= chunk
	}
	return nil
}

func (s *stream) startCapture() {
	s.capturing = true
	s.captured = s.captured[:0]
}

func (s *stream) stopCapture() []byte {
	s.capturing = false
	return s.captured
}
