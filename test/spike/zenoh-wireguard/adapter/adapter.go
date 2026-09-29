// Copyright 2019-2026 The Liqo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package adapter carries framed UDP datagrams over one bidirectional TCP stream.
// It is intentionally a Spike-only component: zenoh-bridge-tcp transports the
// TCP stream between its dial and listen instances.
package adapter

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	// MaxDatagramSize is the largest UDP payload accepted by the framing protocol.
	MaxDatagramSize = 65535
)

// Mode specifies whether the adapter establishes or accepts the TCP transport.
type Mode string

const (
	// ModeDial establishes a TCP connection to TCPAddress and reconnects after failures.
	ModeDial Mode = "dial"
	// ModeListen accepts a TCP connection from the remote bridge.
	ModeListen Mode = "listen"
)

// Config configures one UDP/TCP adapter endpoint.
type Config struct {
	Mode           Mode
	UDPListen      string
	UDPTarget      string
	TCPAddress     string
	ReconnectDelay time.Duration
}

// Run serves the adapter until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Mode != ModeDial && cfg.Mode != ModeListen {
		return fmt.Errorf("invalid mode %q", cfg.Mode)
	}
	if cfg.UDPListen == "" || cfg.TCPAddress == "" {
		return errors.New("udp listen address and tcp address are required")
	}
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = time.Second
	}

	listenAddr, err := net.ResolveUDPAddr("udp", cfg.UDPListen)
	if err != nil {
		return fmt.Errorf("resolving UDP listen address %q: %w", cfg.UDPListen, err)
	}
	udpConn, err := net.ListenUDP("udp", listenAddr)
	if err != nil {
		return fmt.Errorf("listening on UDP %q: %w", cfg.UDPListen, err)
	}
	defer udpConn.Close()

	var target *net.UDPAddr
	if cfg.UDPTarget != "" {
		target, err = net.ResolveUDPAddr("udp", cfg.UDPTarget)
		if err != nil {
			return fmt.Errorf("resolving UDP target %q: %w", cfg.UDPTarget, err)
		}
	}

	local := newUDPLocal(udpConn, target)
	go local.read(ctx)

	switch cfg.Mode {
	case ModeDial:
		return runDial(ctx, cfg, local)
	case ModeListen:
		return runListen(ctx, cfg, local)
	default:
		panic("validated mode")
	}
}

func runDial(ctx context.Context, cfg Config, local *udpLocal) error {
	dialer := net.Dialer{}
	for {
		conn, err := dialer.DialContext(ctx, "tcp", cfg.TCPAddress)
		if err == nil {
			err = relay(ctx, conn, local)
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.ReconnectDelay):
		}
	}
}

func runListen(ctx context.Context, cfg Config, local *udpLocal) error {
	listener, err := net.Listen("tcp", cfg.TCPAddress)
	if err != nil {
		return fmt.Errorf("listening on TCP %q: %w", cfg.TCPAddress, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accepting TCP connection: %w", err)
		}
		_ = relay(ctx, conn, local)
		if ctx.Err() != nil {
			return nil
		}
	}
}

func relay(ctx context.Context, conn net.Conn, local *udpLocal) error {
	defer conn.Close()

	writer := &frameWriter{writer: bufio.NewWriter(conn)}
	local.setWriter(writer)
	defer local.setWriter(nil)

	errCh := make(chan error, 1)
	go func() {
		errCh <- readFrames(conn, local.write)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

type udpLocal struct {
	conn *net.UDPConn

	mu       sync.RWMutex
	target   *net.UDPAddr
	lastPeer *net.UDPAddr
	writer   *frameWriter
}

func newUDPLocal(conn *net.UDPConn, target *net.UDPAddr) *udpLocal {
	return &udpLocal{conn: conn, target: target}
}

func (l *udpLocal) read(ctx context.Context) {
	buf := make([]byte, MaxDatagramSize)
	for {
		n, peer, err := l.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		l.mu.Lock()
		l.lastPeer = peer
		writer := l.writer
		l.mu.Unlock()
		if writer != nil {
			_ = writer.write(buf[:n])
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (l *udpLocal) setWriter(writer *frameWriter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writer = writer
}

func (l *udpLocal) write(payload []byte) error {
	l.mu.RLock()
	target := l.target
	if target == nil {
		target = l.lastPeer
	}
	l.mu.RUnlock()
	if target == nil {
		return nil
	}
	_, err := l.conn.WriteToUDP(payload, target)
	return err
}

type frameWriter struct {
	mu     sync.Mutex
	writer *bufio.Writer
}

func (w *frameWriter) write(payload []byte) error {
	if len(payload) > MaxDatagramSize {
		return fmt.Errorf("datagram length %d exceeds maximum %d", len(payload), MaxDatagramSize)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := writeFrame(w.writer, payload); err != nil {
		return err
	}
	return w.writer.Flush()
}

func writeFrame(writer io.Writer, payload []byte) error {
	if len(payload) > MaxDatagramSize {
		return fmt.Errorf("datagram length %d exceeds maximum %d", len(payload), MaxDatagramSize)
	}
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return fmt.Errorf("writing frame length: %w", err)
	}
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("writing frame payload: %w", err)
	}
	return nil
}

func readFrames(reader io.Reader, handle func([]byte) error) error {
	for {
		var header [2]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return err
		}
		payload := make([]byte, binary.BigEndian.Uint16(header[:]))
		if _, err := io.ReadFull(reader, payload); err != nil {
			return fmt.Errorf("reading frame payload: %w", err)
		}
		if err := handle(payload); err != nil {
			return fmt.Errorf("handling frame payload: %w", err)
		}
	}
}
