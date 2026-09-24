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

// Package zenoh configures Kubernetes API clients to reach a local ZBT listener.
package zenoh

import (
	"context"
	"fmt"
	"net"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	"github.com/liqotech/liqo/pkg/consts"
)

// ConfigureDialer installs a Dial function when the identity secret declares a
// local ZBT listener. The REST host is deliberately left untouched so TLS uses
// the original remote API server name and certificate authority.
func ConfigureDialer(config *rest.Config, secret *corev1.Secret) error {
	if config == nil {
		return fmt.Errorf("REST config is nil")
	}

	address, found := secret.Annotations[consts.ZenohControlPlaneListenerAnnotation]
	if !found || address == "" {
		if secret.Annotations[consts.ZenohControlPlaneRequiredAnnotation] == "true" {
			return fmt.Errorf("Zenoh control-plane listener annotation is required on Secret %q", secret.Name)
		}
		return nil
	}
	return ConfigureDialerForAddress(config, address)
}

// ConfigureDialerForAddress installs a local ZBT listener as the TCP
// destination while preserving config.Host for HTTP Host and TLS validation.
func ConfigureDialerForAddress(config *rest.Config, address string) error {
	if config == nil {
		return fmt.Errorf("REST config is nil")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("invalid Zenoh control-plane listener address %q: %w", address, err)
	}

	dialer := &net.Dialer{}
	config.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
	return nil
}
