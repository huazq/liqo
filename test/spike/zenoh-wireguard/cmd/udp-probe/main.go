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

// udp-probe sends one datagram and succeeds only when the exact payload returns.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	address := flag.String("address", "", "UDP destination")
	payload := flag.String("payload", "liqo-zenoh-wireguard-spike", "payload to send")
	timeout := flag.Duration("timeout", 5*time.Second, "reply timeout")
	flag.Parse()
	if *address == "" {
		fmt.Fprintln(os.Stderr, "-address is required")
		os.Exit(2)
	}

	conn, err := net.Dial("udp", *address)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()
	started := time.Now()
	if _, err := conn.Write([]byte(*payload)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := conn.SetReadDeadline(time.Now().Add(*timeout)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	received := make([]byte, len(*payload))
	n, err := conn.Read(received)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !bytes.Equal(received[:n], []byte(*payload)) {
		fmt.Fprintf(os.Stderr, "unexpected reply %q\n", received[:n])
		os.Exit(1)
	}
	fmt.Printf("UDP round trip succeeded in %s\n", time.Since(started))
}
