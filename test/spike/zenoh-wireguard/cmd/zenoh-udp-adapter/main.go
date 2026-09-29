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

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/liqotech/liqo/test/spike/zenoh-wireguard/adapter"
)

func main() {
	var cfg adapter.Config
	var reconnectDelay time.Duration

	flag.StringVar((*string)(&cfg.Mode), "mode", "", "TCP transport mode: dial or listen")
	flag.StringVar(&cfg.UDPListen, "udp-listen", "", "local UDP address to receive and send datagrams")
	flag.StringVar(&cfg.UDPTarget, "udp-target", "", "fixed local UDP destination; omit to reply to the most recent UDP peer")
	flag.StringVar(&cfg.TCPAddress, "tcp-address", "", "TCP address to dial or listen on")
	flag.DurationVar(&reconnectDelay, "reconnect-delay", time.Second, "delay before reconnecting in dial mode")
	flag.Parse()
	cfg.ReconnectDelay = reconnectDelay

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := adapter.Run(ctx, cfg); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
