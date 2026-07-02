package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/operator/api/v1alpha1"
	"github.com/traefik/operator/internal/provisioner"
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

	// Finalizer is present in every mode (cluster-scoped binding cleanup).
	var got gwapiv1.Gateway
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "gw"}, &got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(&got, dataPlaneFinalizer) {
		t.Fatal("expected the data-plane finalizer")
	}

	// A ClusterRoleBinding grants the data plane ServiceAccount its role.
	var crb rbacv1.ClusterRoleBinding
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name}, &crb); err != nil {
		t.Fatalf("expected clusterrolebinding: %v", err)
	}
	if crb.RoleRef.Name != provisioner.DataPlaneClusterRole {
		t.Fatalf("unexpected roleRef %q", crb.RoleRef.Name)
	}
	if len(crb.Subjects) != 1 || crb.Subjects[0].Name != name || crb.Subjects[0].Namespace != "app" {
		t.Fatalf("unexpected subjects %+v", crb.Subjects)
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
	// The cluster-scoped binding is cleaned up by the finalizer.
	if err := cl.Get(ctx, types.NamespacedName{Name: name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected clusterrolebinding deleted, got %v", err)
	}
}

func TestReconcile_MergedGateways(t *testing.T) {
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
			MergeGateways: true,
			Provisioning:  &v1alpha1.ProvisioningConfig{Namespace: "traefik-system"},
		},
	}
	g1 := testGateway("app1", "g1", "traefik")
	g2 := testGateway("app2", "g2", "traefik")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, proxy, g1, g2).
		WithStatusSubresource(&gwapiv1.Gateway{}).
		Build()

	r := &GatewayReconciler{Client: cl, DataPlaneNamespace: "fallback"}
	reconcile(t, r, g1)
	reconcile(t, r, g2)

	ctx := context.Background()
	shared := provisioner.MergedResourceName("traefik")

	// A single shared data plane exists in the merge namespace.
	var dep appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "traefik-system", Name: shared}, &dep); err != nil {
		t.Fatalf("expected shared deployment: %v", err)
	}
	if dep.Labels[provisioner.MergeGroupLabel] != "traefik" {
		t.Fatalf("expected merge-group label, got %q", dep.Labels[provisioner.MergeGroupLabel])
	}

	// No per-Gateway data planes were created.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app1", Name: provisioner.ResourceName(g1)}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("did not expect a per-gateway deployment, got %v", err)
	}

	// Both Gateways carry the finalizer.
	for _, key := range []types.NamespacedName{{Namespace: "app1", Name: "g1"}, {Namespace: "app2", Name: "g2"}} {
		var got gwapiv1.Gateway
		if err := cl.Get(ctx, key, &got); err != nil {
			t.Fatal(err)
		}
		if !controllerutil.ContainsFinalizer(&got, dataPlaneFinalizer) {
			t.Fatalf("expected finalizer on %s", key)
		}
	}

	// Delete g1: the shared data plane survives because g2 remains.
	var got1 gwapiv1.Gateway
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app1", Name: "g1"}, &got1); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete(ctx, &got1); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, g1)

	if err := cl.Get(ctx, types.NamespacedName{Namespace: "traefik-system", Name: shared}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("shared deployment should survive while g2 remains: %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app1", Name: "g1"}, &gwapiv1.Gateway{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected g1 removed, got %v", err)
	}

	// Delete g2: the last member leaves, so the shared data plane is torn down.
	var got2 gwapiv1.Gateway
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app2", Name: "g2"}, &got2); err != nil {
		t.Fatal(err)
	}
	if err := cl.Delete(ctx, &got2); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, g2)

	if err := cl.Get(ctx, types.NamespacedName{Namespace: "traefik-system", Name: shared}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected shared deployment torn down, got %v", err)
	}
}

func TestReconcile_AutoscalingAndPDBLifecycle(t *testing.T) {
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
			Autoscaling:         &v1alpha1.AutoscalingConfig{MaxReplicas: 4},
			PodDisruptionBudget: &v1alpha1.PodDisruptionBudgetConfig{MinAvailable: ptr.To(intstr.FromInt32(1))},
		},
	}
	gw := testGateway("app", "gw", "traefik")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, proxy, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}).
		Build()

	r := &GatewayReconciler{Client: cl, DataPlaneNamespace: "traefik-system"}
	reconcile(t, r, gw)

	ctx := context.Background()
	name := provisioner.ResourceName(gw)

	// HPA and PDB are provisioned alongside the data plane.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &autoscalingv2.HorizontalPodAutoscaler{}); err != nil {
		t.Fatalf("expected HPA: %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &policyv1.PodDisruptionBudget{}); err != nil {
		t.Fatalf("expected PDB: %v", err)
	}

	// Disable both on the TraefikProxy and reconcile again.
	var p v1alpha1.TraefikProxy
	if err := cl.Get(ctx, types.NamespacedName{Name: "dp"}, &p); err != nil {
		t.Fatal(err)
	}
	p.Spec.Autoscaling = nil
	p.Spec.PodDisruptionBudget = nil
	if err := cl.Update(ctx, &p); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, gw)

	// Both are removed.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &autoscalingv2.HorizontalPodAutoscaler{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected HPA removed, got %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &policyv1.PodDisruptionBudget{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected PDB removed, got %v", err)
	}
}
