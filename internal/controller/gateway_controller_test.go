package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/traefik-operator/api/v1alpha1"
	"github.com/traefik/traefik-operator/internal/provisioner"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := gwapiv1.Install(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func gatewayClass(name string, ref *gwapiv1.ParametersReference) *gwapiv1.GatewayClass {
	return &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gwapiv1.GatewayClassSpec{
			ControllerName: provisioner.ControllerName,
			ParametersRef:  ref,
		},
	}
}

func testGateway(ns, name, class string) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: gwapiv1.ObjectName(class),
			Listeners: []gwapiv1.Listener{{
				Name:     "http",
				Protocol: gwapiv1.HTTPProtocolType,
				Port:     8080,
			}},
		},
	}
}

func reconcile(t *testing.T, r *GatewayReconciler, gw *gwapiv1.Gateway) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func TestReconcile_GatewayNamespaceMode(t *testing.T) {
	s := testScheme(t)
	gc := gatewayClass("traefik", nil)
	gw := testGateway("app", "gw", "traefik")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}).
		Build()

	r := &GatewayReconciler{Client: cl, DataPlaneNamespace: "traefik-system"}
	reconcile(t, r, gw)

	name := provisioner.ResourceName(gw)

	// Deployment is provisioned in the Gateway namespace, owned by the Gateway.
	var dep appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: name}, &dep); err != nil {
		t.Fatalf("expected deployment in gateway namespace: %v", err)
	}
	if len(dep.OwnerReferences) != 1 || dep.OwnerReferences[0].Name != "gw" {
		t.Fatalf("expected gateway owner reference, got %+v", dep.OwnerReferences)
	}

	// No finalizer in gateway-namespace mode.
	var got gwapiv1.Gateway
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "gw"}, &got); err != nil {
		t.Fatal(err)
	}
	if controllerutil.ContainsFinalizer(&got, dataPlaneFinalizer) {
		t.Fatal("did not expect a finalizer in gateway-namespace mode")
	}
}

func TestReconcile_DedicatedModeLifecycle(t *testing.T) {
	s := testScheme(t)
	ref := &gwapiv1.ParametersReference{
		Group: gwapiv1.Group(v1alpha1.GroupVersion.Group),
		Kind:  "TraefikProxy",
		Name:  "dp",
	}
	gc := gatewayClass("traefik", ref)
	proxy := &v1alpha1.TraefikProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "dp"},
		Spec: v1alpha1.TraefikProxySpec{
			Provisioning: &v1alpha1.ProvisioningConfig{
				Mode:      v1alpha1.DedicatedMode,
				Namespace: "traefik-system",
			},
		},
	}
	gw := testGateway("app", "gw", "traefik")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, proxy, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}).
		Build()

	r := &GatewayReconciler{Client: cl, DataPlaneNamespace: "fallback"}
	reconcile(t, r, gw)

	name := provisioner.ResourceName(gw)
	ctx := context.Background()

	// Deployment is provisioned in the dedicated namespace, without owner ref.
	var dep appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "traefik-system", Name: name}, &dep); err != nil {
		t.Fatalf("expected deployment in dedicated namespace: %v", err)
	}
	if len(dep.OwnerReferences) != 0 {
		t.Fatalf("expected no owner reference in dedicated mode, got %+v", dep.OwnerReferences)
	}
	if dep.Labels[provisioner.GatewayNamespaceLabel] != "app" {
		t.Fatalf("expected gateway-namespace label, got %q", dep.Labels[provisioner.GatewayNamespaceLabel])
	}

	// Gateway carries the finalizer and the provisioned-namespace annotation.
	var got gwapiv1.Gateway
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: "gw"}, &got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(&got, dataPlaneFinalizer) {
		t.Fatal("expected data-plane finalizer in dedicated mode")
	}
	if got.Annotations[provisionedNamespaceAnnotation] != "traefik-system" {
		t.Fatalf("expected provisioned-namespace annotation, got %q", got.Annotations[provisionedNamespaceAnnotation])
	}

	// Delete the Gateway: the finalizer keeps it until cleanup runs.
	if err := cl.Delete(ctx, &got); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, gw)

	// Data plane is gone and the Gateway is fully removed.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "traefik-system", Name: name}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected deployment deleted, got %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "traefik-system", Name: name}, &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected service deleted, got %v", err)
	}
	err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: "gw"}, &gwapiv1.Gateway{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected gateway removed after finalizer release, got %v", err)
	}
}
