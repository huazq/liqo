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

package controlplane

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	liqov1beta1 "github.com/liqotech/liqo/apis/core/v1beta1"
	"github.com/liqotech/liqo/pkg/consts"
)

func TestReconcileCreatesAndAnnotatesZenohAccessPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := liqov1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	access := &liqov1beta1.RemoteAPIAccess{ObjectMeta: metav1.ObjectMeta{Name: "remote-a", Namespace: "liqo"},
		Spec: liqov1beta1.RemoteAPIAccessSpec{RemoteClusterID: "remote-a"}}
	identitySecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "identity", Namespace: "tenant-a", Labels: map[string]string{
		consts.RemoteClusterID: "remote-a",
	}}}
	kubernetesService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: kubernetesServiceName, Namespace: kubernetesServiceNS},
		Spec: corev1.ServiceSpec{ClusterIP: "10.43.0.1"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&liqov1beta1.RemoteAPIAccess{}).
		WithObjects(access, identitySecret, kubernetesService).Build()
	reconciler := &ZenohRemoteAPIAccessReconciler{Client: cl, Scheme: scheme, LiqoNamespace: "liqo", LocalClusterID: "local-a", BridgeImage: "example/zbt:test", ConfigSecretName: "zenoh-config", ConfigSecretKey: "config.json5"}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: access.Name, Namespace: access.Namespace}}); err != nil {
		t.Fatal(err)
	}

	var listener corev1.Service
	if err := cl.Get(ctx, types.NamespacedName{Name: listenerNamePrefix + "remote-a", Namespace: "liqo"}, &listener); err != nil {
		t.Fatal(err)
	}
	if listener.Spec.Type != corev1.ServiceTypeNodePort {
		t.Fatalf("expected NodePort listener, got %s", listener.Spec.Type)
	}
	if len(listener.Spec.Ports) != 1 || listener.Spec.Ports[0].Port != bridgePort {
		t.Fatalf("unexpected listener ports: %#v", listener.Spec.Ports)
	}

	var backend, listenerDeployment appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Name: backendName, Namespace: "liqo"}, &backend); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Name: listenerNamePrefix + "remote-a", Namespace: "liqo"}, &listenerDeployment); err != nil {
		t.Fatal(err)
	}
	if got := backend.Spec.Template.Spec.Containers[0].Args[1]; got != "local-a/10.43.0.1:443" {
		t.Fatalf("unexpected backend target %q", got)
	}
	if got := listenerDeployment.Spec.Template.Spec.Containers[0].Args[1]; got != "remote-a/0.0.0.0:8443,proto=raw" {
		t.Fatalf("unexpected listener endpoint %q", got)
	}
	if got := listenerDeployment.Spec.Template.Spec.Containers[0].Args[7]; got != "0.0.0.0:9100" {
		t.Fatalf("unexpected metrics address %q", got)
	}

	var secret corev1.Secret
	if err := cl.Get(ctx, types.NamespacedName{Name: "identity", Namespace: "tenant-a"}, &secret); err != nil {
		t.Fatal(err)
	}
	wantAddress := listenerNamePrefix + "remote-a.liqo.svc:8443"
	if got := secret.Annotations[consts.ZenohControlPlaneListenerAnnotation]; got != wantAddress {
		t.Fatalf("expected identity listener %q, got %q", wantAddress, got)
	}
}
