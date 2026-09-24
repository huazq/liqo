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

package root

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNewAPIServerClientCAController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: extensionAPIServerAuthenticationNamespace,
			Name:      extensionAPIServerAuthenticationConfigMap,
		},
		Data: map[string]string{extensionAPIServerClientCAKey: testCAPEM(t)},
	})

	controller, err := newAPIServerClientCAController(ctx, client)
	if err != nil {
		t.Fatalf("creating API server client CA controller: %v", err)
	}
	if len(controller.CurrentCABundleContent()) == 0 {
		t.Fatal("the API server client CA controller did not load the initial CA bundle")
	}
}

func TestNewAPIServerClientCAControllerRequiresPublishedCA(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	client := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: extensionAPIServerAuthenticationNamespace,
			Name:      extensionAPIServerAuthenticationConfigMap,
		},
	})

	if _, err := newAPIServerClientCAController(ctx, client); err == nil {
		t.Fatal("expected an error when the published API server client CA is absent")
	}
}

func testCAPEM(t *testing.T) string {
	t.Helper()

	_, key, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("generating CA key: %v", err)
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-client-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, &x509.Certificate{}, key.Public(), key)
	if err != nil {
		t.Fatalf("creating CA certificate: %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
