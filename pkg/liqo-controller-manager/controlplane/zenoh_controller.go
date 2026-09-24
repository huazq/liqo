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

// Package controlplane reconciles Zenoh control-plane access workloads.
package controlplane

import (
	"context"
	"fmt"
	"net/netip"
	"path"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	liqov1beta1 "github.com/liqotech/liqo/apis/core/v1beta1"
	"github.com/liqotech/liqo/pkg/consts"
)

const (
	bridgePort            int32 = 8443
	backendName                 = "liqo-zenoh-api-backend"
	listenerNamePrefix          = "liqo-zenoh-api-listener-"
	kubernetesServiceName       = "kubernetes"
	kubernetesServiceNS         = "default"
)

// ZenohRemoteAPIAccessReconciler manages the ZBT listener and backend workloads.
type ZenohRemoteAPIAccessReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	LiqoNamespace    string
	LocalClusterID   liqov1beta1.ClusterID
	BridgeImage      string
	ConfigSecretName string
	ConfigSecretKey  string
}

// +kubebuilder:rbac:groups=core.liqo.io,resources=remoteapiaccesses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.liqo.io,resources=remoteapiaccesses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;secrets,verbs=get;list;watch;create;update;patch;delete

func (r *ZenohRemoteAPIAccessReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var access liqov1beta1.RemoteAPIAccess
	if err := r.Get(ctx, req.NamespacedName, &access); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if access.Spec.RemoteClusterID == "" {
		return ctrl.Result{}, fmt.Errorf("RemoteAPIAccess %q has no remoteClusterID", access.Name)
	}
	if access.Namespace != r.LiqoNamespace {
		return ctrl.Result{}, fmt.Errorf("RemoteAPIAccess %q must be in the Liqo namespace %q", access.Name, r.LiqoNamespace)
	}
	if r.BridgeImage == "" || r.ConfigSecretName == "" {
		return ctrl.Result{}, fmt.Errorf("Zenoh bridge image and configuration Secret are required")
	}
	if r.ConfigSecretKey == "" || path.Base(r.ConfigSecretKey) != r.ConfigSecretKey {
		return ctrl.Result{}, fmt.Errorf("Zenoh configuration Secret key must be a file name, got %q", r.ConfigSecretKey)
	}

	backendReady, err := r.ensureBackend(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	listenerName := listenerNamePrefix + string(access.Spec.RemoteClusterID)
	listenerNodePort, listenerReady, err := r.ensureListener(ctx, &access, listenerName)
	if err != nil {
		return ctrl.Result{}, err
	}

	address := fmt.Sprintf("%s.%s.svc:%d", listenerName, access.Namespace, bridgePort)
	if err := r.annotateIdentitySecrets(ctx, access.Spec.RemoteClusterID, address); err != nil {
		return ctrl.Result{}, err
	}
	ready := backendReady && listenerReady && listenerNodePort != 0
	if access.Status.ListenerAddress != address || access.Status.ListenerNodePort != listenerNodePort ||
		setReadyCondition(&access, ready) {
		access.Status.ListenerAddress = address
		access.Status.ListenerNodePort = listenerNodePort
		if err := r.Status().Update(ctx, &access); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func (r *ZenohRemoteAPIAccessReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&liqov1beta1.RemoteAPIAccess{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.remoteAccessForSecret)).
		Complete(r)
}

func (r *ZenohRemoteAPIAccessReconciler) remoteAccessForSecret(ctx context.Context, object client.Object) []reconcile.Request {
	remoteClusterID := object.GetLabels()[consts.RemoteClusterID]
	if remoteClusterID == "" {
		return nil
	}
	var accesses liqov1beta1.RemoteAPIAccessList
	if err := r.List(ctx, &accesses, client.InNamespace(r.LiqoNamespace)); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, 1)
	for i := range accesses.Items {
		if accesses.Items[i].Spec.RemoteClusterID == liqov1beta1.ClusterID(remoteClusterID) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&accesses.Items[i])})
		}
	}
	return requests
}

func (r *ZenohRemoteAPIAccessReconciler) ensureBackend(ctx context.Context) (bool, error) {
	var kubernetesService corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: kubernetesServiceNS, Name: kubernetesServiceName}, &kubernetesService); err != nil {
		return false, fmt.Errorf("getting local Kubernetes API Service: %w", err)
	}
	clusterIP := kubernetesService.Spec.ClusterIP
	if _, err := netip.ParseAddr(clusterIP); err != nil {
		return false, fmt.Errorf("local Kubernetes API Service has invalid ClusterIP %q: %w", clusterIP, err)
	}
	desired := r.bridgeDeployment(backendName, []string{"--backend", fmt.Sprintf("%s/%s:443", r.LocalClusterID, clusterIP)})
	deployment := &appsv1.Deployment{ObjectMeta: desired.ObjectMeta}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Labels = desired.Labels
		deployment.Spec = desired.Spec
		return nil
	})
	if err != nil {
		return false, err
	}
	return deployment.Status.AvailableReplicas > 0, nil
}

func (r *ZenohRemoteAPIAccessReconciler) ensureListener(ctx context.Context, access *liqov1beta1.RemoteAPIAccess, name string) (int32, bool, error) {
	desired := r.bridgeDeployment(name, []string{"--listen", fmt.Sprintf("%s/0.0.0.0:%d,proto=raw", access.Spec.RemoteClusterID, bridgePort)})
	deployment := &appsv1.Deployment{ObjectMeta: desired.ObjectMeta}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Labels = desired.Labels
		deployment.Spec = desired.Spec
		return controllerutil.SetControllerReference(access, deployment, r.Scheme)
	}); err != nil {
		return 0, false, err
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: access.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = map[string]string{"app.kubernetes.io/name": name}
		service.Spec.Selector = service.Labels
		service.Spec.Type = corev1.ServiceTypeNodePort
		service.Spec.Ports = []corev1.ServicePort{{Name: "api", Port: bridgePort, TargetPort: intstr.FromInt32(bridgePort)}}
		return controllerutil.SetControllerReference(access, service, r.Scheme)
	})
	if err != nil {
		return 0, false, err
	}
	return service.Spec.Ports[0].NodePort, deployment.Status.AvailableReplicas > 0, nil
}

func setReadyCondition(access *liqov1beta1.RemoteAPIAccess, ready bool) bool {
	status := metav1.ConditionFalse
	reason := "WorkloadsNotReady"
	message := "Waiting for the Zenoh listener and backend deployments to become ready"
	if ready {
		status = metav1.ConditionTrue
		reason = "Ready"
		message = "Zenoh listener and backend deployments are ready"
	}
	condition := metav1.Condition{Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: access.Generation, LastTransitionTime: metav1.Now()}
	for i := range access.Status.Conditions {
		if access.Status.Conditions[i].Type != condition.Type {
			continue
		}
		if access.Status.Conditions[i].Status == condition.Status && access.Status.Conditions[i].Reason == condition.Reason &&
			access.Status.Conditions[i].ObservedGeneration == condition.ObservedGeneration {
			return false
		}
		access.Status.Conditions[i] = condition
		return true
	}
	access.Status.Conditions = append(access.Status.Conditions, condition)
	return true
}

func (r *ZenohRemoteAPIAccessReconciler) bridgeDeployment(name string, args []string) *appsv1.Deployment {
	labels := map[string]string{"app.kubernetes.io/name": name}
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.LiqoNamespace}, Spec: appsv1.DeploymentSpec{
		Replicas: ptr.To[int32](1), Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](1000), RunAsGroup: ptr.To[int64](1000), FSGroup: ptr.To[int64](1000)},
			Containers: []corev1.Container{{
				Name: "zenoh-bridge-tcp", Image: r.BridgeImage,
				Args:            append(args, "--zenoh-config", "/etc/zenoh/"+r.ConfigSecretKey, "--reliability", "stream", "--metrics-addr", "0.0.0.0:9100"),
				Ports:           []corev1.ContainerPort{{Name: "api", ContainerPort: bridgePort}},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "zenoh-config", MountPath: "/etc/zenoh", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "zenoh-config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: r.ConfigSecretName, DefaultMode: ptr.To[int32](0440)}}}},
		}}}}
}

func (r *ZenohRemoteAPIAccessReconciler) annotateIdentitySecrets(ctx context.Context, remote liqov1beta1.ClusterID, address string) error {
	var secrets corev1.SecretList
	if err := r.List(ctx, &secrets, client.MatchingLabels{consts.RemoteClusterID: string(remote)}); err != nil {
		return err
	}
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		if secret.Annotations[consts.ZenohControlPlaneListenerAnnotation] == address {
			continue
		}
		secret.Annotations[consts.ZenohControlPlaneListenerAnnotation] = address
		if err := r.Update(ctx, secret); err != nil {
			return err
		}
	}
	return nil
}
