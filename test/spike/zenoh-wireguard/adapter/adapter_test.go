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

package adapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestFramesRoundTrip(t *testing.T) {
	payloads := [][]byte{nil, []byte("wireguard"), bytes.Repeat([]byte{0xab}, MaxDatagramSize)}
	var stream bytes.Buffer
	for _, payload := range payloads {
		if err := writeFrame(&stream, payload); err != nil {
			t.Fatalf("writeFrame() error = %v", err)
		}
	}

	var received [][]byte
	err := readFrames(&stream, func(payload []byte) error {
		received = append(received, payload)
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("readFrames() error = %v, want EOF", err)
	}
	if len(received) != len(payloads) {
		t.Fatalf("received %d payloads, want %d", len(received), len(payloads))
	}
	for i := range payloads {
		if !bytes.Equal(received[i], payloads[i]) {
			t.Errorf("payload %d differs", i)
		}
	}
}

func TestWriteFrameRejectsOversizeDatagram(t *testing.T) {
	err := writeFrame(io.Discard, make([]byte, MaxDatagramSize+1))
	if err == nil {
		t.Fatal("writeFrame() succeeded for an oversized datagram")
	}
}

func TestReadFramesRejectsTruncatedPayload(t *testing.T) {
	err := readFrames(bytes.NewReader([]byte{0, 3, 1, 2}), func([]byte) error { return nil })
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("readFrames() error = %v, want unexpected EOF", err)
	}
}

func TestUDPDatagramRoundTrip(t *testing.T) {
	echoConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echoConn.Close()
	go echoUDP(t, echoConn)

	tcpAddress := freeTCPAddress(t)
	serverUDPAddress := freeUDPAddress(t)
	clientUDPAddress := freeUDPAddress(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- Run(ctx, Config{
			Mode:       ModeListen,
			UDPListen:  serverUDPAddress,
			UDPTarget:  echoConn.LocalAddr().String(),
			TCPAddress: tcpAddress,
		})
	}()

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- Run(ctx, Config{
			Mode:           ModeDial,
			UDPListen:      clientUDPAddress,
			TCPAddress:     tcpAddress,
			ReconnectDelay: 10 * time.Millisecond,
		})
	}()

	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	message := []byte("wireguard datagram")
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err = clientConn.WriteToUDP(message, mustUDPAddr(t, clientUDPAddress))
		if err != nil {
			t.Fatal(err)
		}
		_ = clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buf := make([]byte, MaxDatagramSize)
		n, _, readErr := clientConn.ReadFromUDP(buf)
		if readErr == nil && bytes.Equal(buf[:n], message) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not receive echoed datagram: %v", readErr)
		}
	}

	cancel()
	if err := <-serverErr; err != nil {
		t.Fatalf("server Run() error = %v", err)
	}
	if err := <-clientErr; err != nil {
		t.Fatalf("client Run() error = %v", err)
	}
}

func echoUDP(t *testing.T, conn *net.UDPConn) {
	t.Helper()
	buf := make([]byte, MaxDatagramSize)
	for {
		n, peer, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if _, err := conn.WriteToUDP(buf[:n], peer); err != nil {
			t.Errorf("UDP echo error: %v", err)
			return
		}
	}
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func freeUDPAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().String()
}

func mustUDPAddr(t *testing.T, address string) *net.UDPAddr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}
