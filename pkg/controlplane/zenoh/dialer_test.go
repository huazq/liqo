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

package zenoh

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/liqotech/liqo/pkg/consts"
)

func TestConfigureDialerPreservesHostAndDialsListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			close(accepted)
			_ = conn.Close()
		}
	}()

	config := &rest.Config{Host: "https://192.0.2.10:6443"}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		consts.ZenohControlPlaneListenerAnnotation: listener.Addr().String(),
	}}}
	if err := ConfigureDialer(config, secret); err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://192.0.2.10:6443" {
		t.Fatalf("REST host was changed to %q", config.Host)
	}

	conn, err := config.Dial(context.Background(), "tcp", "192.0.2.10:6443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	<-accepted
}

func TestConfigureDialerRejectsInvalidListenerAddress(t *testing.T) {
	config := &rest.Config{}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		consts.ZenohControlPlaneListenerAnnotation: "not-an-address",
	}}}
	if err := ConfigureDialer(config, secret); err == nil {
		t.Fatal("expected invalid listener address error")
	}
}

func TestConfigureDialerRejectsDirectFallbackWhenZenohIsRequired(t *testing.T) {
	config := &rest.Config{Host: "https://192.0.2.10:6443"}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		consts.ZenohControlPlaneRequiredAnnotation: "true",
	}}}

	if err := ConfigureDialer(config, secret); err == nil {
		t.Fatal("expected required Zenoh transport without listener to be rejected")
	}
}

func TestSPDYExecutorHonorsDial(t *testing.T) {
	requestReceived := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestReceived <- struct{}{}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		clientConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer clientConn.Close()

		serverConn, dialErr := net.Dial("tcp", server.Listener.Addr().String())
		if dialErr != nil {
			return
		}
		defer serverConn.Close()

		go func() { _, _ = io.Copy(serverConn, clientConn) }()
		_, _ = io.Copy(clientConn, serverConn)
	}()

	config := &rest.Config{
		Host: "https://127.0.0.1:1",
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
		},
	}
	execURL, err := url.Parse(config.Host + "/api/v1/namespaces/default/pods/test/exec")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewSPDYExecutor(config, http.MethodPost, execURL)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{}); err == nil {
		t.Fatal("expected SPDY upgrade to be rejected by the test server")
	}
	select {
	case <-requestReceived:
	case <-ctx.Done():
		t.Fatal("SPDY executor did not use the configured dialer")
	}
}
