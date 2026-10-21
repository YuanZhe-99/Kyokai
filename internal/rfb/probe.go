// Vendored from open-screen-sharing (https://github.com/nekohasekai/open-screen-sharing),
// internal/rfb/probe.go, commit ce6408d4fcb27a68974b7f374b1f55283aa75ac9.
// Copyright (C) 2026 nekohasekai. License: GPL-3.0-or-later, with the additional term that no
// derivative work may use the name of that application or imply association with it without
// prior consent. Modified by Kyokai: Options.Conn used instead of dialing, import paths.

package rfb

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	E "github.com/YuanZhe-99/Kyokai/internal/exceptions"
)

// dial returns Options.Conn when set, otherwise a TCP connection to Options.Address.
func dial(ctx context.Context, options Options) (net.Conn, error) {
	if options.Conn != nil {
		return options.Conn, nil
	}
	dialer := net.Dialer{
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     5 * time.Second,
			Interval: 5 * time.Second,
			Count:    3,
		},
	}
	return dialer.DialContext(ctx, "tcp", options.Address)
}

func Probe(ctx context.Context, options Options) (ServerInfo, error) {
	return exchange(ctx, options, func(h *handshake) error {
		err := h.negotiateVersion()
		if err != nil {
			return err
		}
		err = h.readSecurityTypes()
		if err != nil {
			return err
		}
		h.logger.Debug("probe", "version", h.info.Version.String(), "offered", fmt.Sprint(h.info.SecurityTypes))
		return nil
	})
}

func CheckLogin(ctx context.Context, options Options) (ServerInfo, error) {
	return exchange(ctx, options, func(h *handshake) error {
		err := h.negotiateVersion()
		if err != nil {
			return err
		}
		err = h.authenticate()
		if err != nil {
			return err
		}
		return h.initialize()
	})
}

func exchange(ctx context.Context, options Options, steps func(h *handshake) error) (ServerInfo, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	conn, err := dial(ctx, options)
	if err != nil {
		return ServerInfo{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() {
		conn.Close()
	})
	h := &handshake{ctx: ctx, conn: conn, stream: newStream(conn), options: &options, logger: logger}
	err = steps(h)
	if !stop() {
		err = E.Errors(ctx.Err(), err)
	}
	if err != nil {
		return ServerInfo{}, err
	}
	return h.info, nil
}
