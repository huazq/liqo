// Copyright 2019-2026 The Liqo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package zenoh

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	apispdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clientspdy "k8s.io/client-go/transport/spdy"
)

// NewSPDYExecutor creates an executor which honors config.Dial. client-go's
// remotecommand.NewSPDYExecutor does not propagate rest.Config.Dial to its
// upgrade transport, which would bypass a ZBT listener for exec and attach.
func NewSPDYExecutor(config *rest.Config, method string, url *url.URL) (remotecommand.Executor, error) {
	transport, upgrader, err := SPDYRoundTripperFor(config)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewSPDYExecutorForTransports(transport, upgrader, method, url)
}

// NewSPDYDialer creates a port-forward dialer which honors config.Dial.
func NewSPDYDialer(config *rest.Config, method string, url *url.URL) (httpstream.Dialer, error) {
	transport, upgrader, err := SPDYRoundTripperFor(config)
	if err != nil {
		return nil, err
	}
	return clientspdy.NewDialer(upgrader, &http.Client{Transport: transport}, method, url), nil
}

// SPDYRoundTripperFor returns an upgrade transport which honors config.Dial.
func SPDYRoundTripperFor(config *rest.Config) (http.RoundTripper, clientspdy.Upgrader, error) {
	if config == nil {
		return nil, nil, fmt.Errorf("REST config is nil")
	}
	if config.Dial == nil {
		return clientspdy.RoundTripperFor(config)
	}

	tlsConfig, err := rest.TLSConfigFor(config)
	if err != nil {
		return nil, nil, fmt.Errorf("build SPDY TLS configuration: %w", err)
	}

	upgradeTransport := &http.Transport{
		DialContext:     config.Dial,
		TLSClientConfig: tlsConfig,
	}
	upgradeRoundTripper, err := apispdy.NewRoundTripperWithConfig(apispdy.RoundTripperConfig{
		UpgradeTransport: upgradeTransport,
		PingPeriod:       5 * time.Second,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create SPDY upgrade transport: %w", err)
	}
	wrapper, err := rest.HTTPWrappersForConfig(config, upgradeRoundTripper)
	if err != nil {
		return nil, nil, fmt.Errorf("wrap SPDY transport: %w", err)
	}

	return wrapper, upgradeRoundTripper, nil
}
