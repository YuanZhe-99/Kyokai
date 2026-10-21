// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rdp/codec/h264/videotoolbox_stub.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: import paths.

//go:build !darwin || !cgo

package h264

import E "github.com/YuanZhe-99/Kyokai/internal/exceptions"

type videoSession struct{}

func Available() bool {
	return false
}

func (s *videoSession) decode(units [][]byte, width, height int) (picture, bool, error) {
	return picture{}, false, E.New("h264: hardware decoding is not supported on this platform")
}

func (s *videoSession) release() {}

func (s *videoSession) close() {}
