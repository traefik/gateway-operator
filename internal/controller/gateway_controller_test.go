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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/gateway-operator/api/v1alpha1"
	"github.com/traefik/gateway-operator/internal/provisioner"
)

func TestReconcile_GatewayNamespaceMode(t *testing.T) {
	s := testScheme(t)
	gc := gatewayClass("traefik", nil)
	gw := testGateway("app", "gw", "traefik")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}).
		Build()

	r := &GatewayReconciler{Client: cl}
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

	// Finalizer is present for the cluster-scoped binding cleanup.
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

func TestReconcile_Lifecycle(t *testing.T) {
	s := testScheme(t)
	gc := gatewayClass("traefik", nil)
	gw := testGateway("app", "gw", "traefik")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}).
		Build()

	r := &GatewayReconciler{Client: cl}
	reconcile(t, r, gw)

	name := provisioner.ResourceName(gw)
	ctx := context.Background()

	// Data plane is provisioned in the Gateway's own namespace.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("expected deployment in gateway namespace: %v", err)
	}

	// Gateway carries the finalizer that guards the cluster-scoped binding.
	var got gwapiv1.Gateway
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: "gw"}, &got); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(&got, dataPlaneFinalizer) {
		t.Fatal("expected the data-plane finalizer")
	}

	// Delete the Gateway: the finalizer keeps it until cleanup runs.
	if err := cl.Delete(ctx, &got); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, gw)

	// The Gateway is fully removed and the cluster-scoped binding is cleaned up.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: "gw"}, &gwapiv1.Gateway{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected gateway removed after finalizer release, got %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Name: name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected clusterrolebinding deleted, got %v", err)
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
			PodDisruptionBudget: &v1alpha1.PodDisruptionBudgetConfig{MinAvailable: new(intstr.FromInt32(1))},
		},
	}
	gw := testGateway("app", "gw", "traefik")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, proxy, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}).
		Build()

	r := &GatewayReconciler{Client: cl}
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

func TestReconcile_UnsupportedStaticAddressNotProvisioned(t *testing.T) {
	s := testScheme(t)
	gc := gatewayClass("traefik", nil)
	gw := testGateway("app", "gw", "traefik")
	gw.Spec.Addresses = []gwapiv1.GatewaySpecAddress{{
		Type: ptr.To(gwapiv1.AddressType("test/fake")), Value: "x",
	}}

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}, &corev1.Service{}).
		Build()

	r := &GatewayReconciler{Client: cl}
	reconcile(t, r, gw)

	// The data plane must not be provisioned for a rejected Gateway.
	err := cl.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: provisioner.ResourceName(gw)}, &appsv1.Deployment{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no deployment for a rejected gateway, got %v", err)
	}

	got := getGateway(t, cl, "app", "gw")
	accepted := apimeta.FindStatusCondition(got.Status.Conditions, string(gwapiv1.GatewayConditionAccepted))
	if accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != string(gwapiv1.GatewayReasonUnsupportedAddress) {
		t.Fatalf("expected Accepted=False/UnsupportedAddress, got %+v", got.Status.Conditions)
	}
	programmed := apimeta.FindStatusCondition(got.Status.Conditions, string(gwapiv1.GatewayConditionProgrammed))
	if programmed == nil || programmed.Status != metav1.ConditionFalse {
		t.Fatalf("expected Programmed=False, got %+v", got.Status.Conditions)
	}
}

func TestReconcile_RejectedStaticAddressTearsDownDataPlane(t *testing.T) {
	s := testScheme(t)
	gc := gatewayClass("traefik", nil)
	gw := testGateway("app", "gw", "traefik")

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}, &corev1.Service{}).
		Build()

	r := &GatewayReconciler{Client: cl}
	ctx := context.Background()
	name := provisioner.ResourceName(gw)

	// First reconcile with a valid spec provisions the data plane.
	reconcile(t, r, gw)
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("expected deployment to be provisioned: %v", err)
	}

	// The spec then requests an unsupported address type.
	got := getGateway(t, cl, "app", "gw")
	got.Spec.Addresses = []gwapiv1.GatewaySpecAddress{{
		Type: ptr.To(gwapiv1.AddressType("test/fake")), Value: "x",
	}}
	if err := cl.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, gw)

	// The data plane is torn down.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected deployment torn down after rejection, got %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Name: name}, &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected clusterrolebinding torn down after rejection, got %v", err)
	}
}

func TestReconcile_UnusableStaticAddress(t *testing.T) {
	s := testScheme(t)
	gc := gatewayClass("traefik", nil)
	gw := testGateway("app", "gw", "traefik")
	gw.Spec.Addresses = []gwapiv1.GatewaySpecAddress{{
		Type: ptr.To(gwapiv1.IPAddressType), Value: "203.0.113.5",
	}}

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}, &corev1.Service{}).
		Build()

	r := &GatewayReconciler{Client: cl}
	ctx := context.Background()
	name := provisioner.ResourceName(gw)

	reconcile(t, r, gw)

	// The Service is provisioned (it is what an address is assigned to) but the
	// data plane is not, because the requested address is not realized.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &corev1.Service{}); err != nil {
		t.Fatalf("expected the service to be provisioned: %v", err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no deployment for an unusable address, got %v", err)
	}

	got := getGateway(t, cl, "app", "gw")
	accepted := apimeta.FindStatusCondition(got.Status.Conditions, string(gwapiv1.GatewayConditionAccepted))
	if accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Fatalf("expected Accepted=True, got %+v", got.Status.Conditions)
	}
	programmed := apimeta.FindStatusCondition(got.Status.Conditions, string(gwapiv1.GatewayConditionProgrammed))
	if programmed == nil || programmed.Status != metav1.ConditionFalse || programmed.Reason != string(gwapiv1.GatewayReasonAddressNotUsable) {
		t.Fatalf("expected Programmed=False/AddressNotUsable, got %+v", got.Status.Conditions)
	}
}

func TestReconcile_UsableStaticAddressProvisions(t *testing.T) {
	s := testScheme(t)
	gc := gatewayClass("traefik", nil)
	gw := testGateway("app", "gw", "traefik")
	gw.Spec.Addresses = []gwapiv1.GatewaySpecAddress{{
		Type: ptr.To(gwapiv1.IPAddressType), Value: "203.0.113.5",
	}}

	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gc, gw).
		WithStatusSubresource(&gwapiv1.Gateway{}, &corev1.Service{}).
		Build()

	r := &GatewayReconciler{Client: cl}
	ctx := context.Background()
	name := provisioner.ResourceName(gw)

	// The address is not realized yet, so no data plane runs.
	reconcile(t, r, gw)
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no deployment before the address is realized, got %v", err)
	}

	// The load balancer assigns the requested address to the Service.
	setServiceIngress(t, cl, "app", name, "203.0.113.5")
	reconcile(t, r, gw)

	// The data plane is provisioned once the address is realized, and the
	// operator releases the Gateway status back to it without error.
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "app", Name: name}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("expected deployment once the address is realized: %v", err)
	}
}

func setServiceIngress(t *testing.T, cl client.Client, ns, name, ip string) {
	t.Helper()
	var svc corev1.Service
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &svc); err != nil {
		t.Fatalf("getting service: %v", err)
	}
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: ip}}
	if err := cl.Status().Update(context.Background(), &svc); err != nil {
		t.Fatalf("updating service status: %v", err)
	}
}

func getGateway(t *testing.T, cl client.Client, ns, name string) *gwapiv1.Gateway {
	t.Helper()
	var gw gwapiv1.Gateway
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &gw); err != nil {
		t.Fatal(err)
	}
	return &gw
}

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
