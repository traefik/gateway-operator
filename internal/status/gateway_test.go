package status

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func gw(addrs ...gwapiv1.GatewaySpecAddress) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{Spec: gwapiv1.GatewaySpec{Addresses: addrs}}
}

func readyDep() *appsv1.Deployment {
	return &appsv1.Deployment{Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}
}

func TestDerive_LoadBalancerReady(t *testing.T) {
	svc := &corev1.Service{
		Spec:   corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}}},
	}
	res := Derive(gw(), svc, readyDep())
	if !res.Programmed {
		t.Fatalf("expected Programmed, got reason %q", res.Reason)
	}
	if len(res.Addresses) != 1 || res.Addresses[0].Value != "1.2.3.4" {
		t.Fatalf("unexpected addresses: %+v", res.Addresses)
	}
}

func TestDerive_NoReadyReplicas(t *testing.T) {
	svc := &corev1.Service{
		Spec:   corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}}},
	}
	res := Derive(gw(), svc, &appsv1.Deployment{})
	if res.Programmed {
		t.Fatal("expected not Programmed when no ready replicas")
	}
	if res.Reason != gwapiv1.GatewayReasonNoResources {
		t.Fatalf("unexpected reason %q", res.Reason)
	}
}

func TestDerive_ServiceNotCreatedWithSpecAddresses(t *testing.T) {
	g := gw(gwapiv1.GatewaySpecAddress{Type: ptr.To(gwapiv1.IPAddressType), Value: "9.9.9.9"})
	res := Derive(g, nil, nil)
	if res.Programmed {
		t.Fatal("expected not Programmed")
	}
	if res.Reason != gwapiv1.GatewayReasonAddressNotUsable {
		t.Fatalf("unexpected reason %q", res.Reason)
	}
	if len(res.Addresses) != 1 || res.Addresses[0].Value != "9.9.9.9" {
		t.Fatalf("expected spec address echoed, got %+v", res.Addresses)
	}
}

func TestDerive_ClusterIP(t *testing.T) {
	svc := &corev1.Service{Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIPs: []string{"10.0.0.5"}}}
	res := Derive(gw(), svc, readyDep())
	if !res.Programmed {
		t.Fatalf("expected Programmed, got %q", res.Reason)
	}
	if res.Addresses[0].Value != "10.0.0.5" {
		t.Fatalf("unexpected addresses %+v", res.Addresses)
	}
}
