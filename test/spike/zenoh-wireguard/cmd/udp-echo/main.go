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

// udp-echo is a disposable UDP endpoint used only by the transport Spike.
package main

import (
	"flag"
	"log"
	"net"
)

func main() {
	listenAddress := flag.String("listen", ":51843", "UDP address to echo")
	flag.Parse()

	conn, err := net.ListenPacket("udp", *listenAddress)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	buf := make([]byte, 65535)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("received %d bytes from %s", n, peer)
		if _, err := conn.WriteTo(buf[:n], peer); err != nil {
			log.Printf("writing UDP reply: %v", err)
		}
	}
}
