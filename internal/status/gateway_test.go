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

func lbService(ips ...string) *corev1.Service {
	ing := make([]corev1.LoadBalancerIngress, 0, len(ips))
	for _, ip := range ips {
		ing = append(ing, corev1.LoadBalancerIngress{IP: ip})
	}
	return &corev1.Service{
		Spec:   corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: ing}},
	}
}

func ipAddr(v string) gwapiv1.GatewaySpecAddress {
	return gwapiv1.GatewaySpecAddress{Type: ptr.To(gwapiv1.IPAddressType), Value: v}
}

func TestDerive_LoadBalancerReady(t *testing.T) {
	res := Derive(gw(), lbService("1.2.3.4"), readyDep(), nil)
	if !res.Programmed {
		t.Fatalf("expected Programmed, got reason %q", res.Reason)
	}
	if len(res.Addresses) != 1 || res.Addresses[0].Value != "1.2.3.4" {
		t.Fatalf("unexpected addresses: %+v", res.Addresses)
	}
}

func TestDerive_NoReadyReplicas(t *testing.T) {
	res := Derive(gw(), lbService("1.2.3.4"), &appsv1.Deployment{}, nil)
	if res.Programmed {
		t.Fatal("expected not Programmed when no ready replicas")
	}
	if res.Reason != gwapiv1.GatewayReasonNoResources {
		t.Fatalf("unexpected reason %q", res.Reason)
	}
}

func TestDerive_ServiceNotCreatedWithSpecAddresses(t *testing.T) {
	res := Derive(gw(ipAddr("9.9.9.9")), nil, nil, nil)
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
	res := Derive(gw(), svc, readyDep(), nil)
	if !res.Programmed {
		t.Fatalf("expected Programmed, got %q", res.Reason)
	}
	if res.Addresses[0].Value != "10.0.0.5" {
		t.Fatalf("unexpected addresses %+v", res.Addresses)
	}
}

// Static-address cases mirror the GatewayStaticAddresses conformance flow.

func TestDerive_StaticUsableAddress(t *testing.T) {
	// usable address granted by the LoadBalancer.
	res := Derive(gw(ipAddr("203.0.113.10")), lbService("203.0.113.10"), readyDep(), nil)
	if !res.Programmed {
		t.Fatalf("expected Programmed, got %q", res.Reason)
	}
	if len(res.Addresses) != 1 || res.Addresses[0].Value != "203.0.113.10" {
		t.Fatalf("unexpected addresses %+v", res.Addresses)
	}
	if *res.Addresses[0].Type != gwapiv1.IPAddressType {
		t.Fatalf("expected IPAddress type")
	}
}

func TestDerive_StaticUnusableAddress(t *testing.T) {
	// unusable + usable requested, only usable granted by the LoadBalancer.
	g := gw(ipAddr("198.51.100.1"), ipAddr("203.0.113.10"))
	res := Derive(g, lbService("203.0.113.10"), readyDep(), nil)
	if res.Programmed {
		t.Fatal("expected not Programmed while an unusable address is requested")
	}
	if res.Reason != gwapiv1.GatewayReasonAddressNotUsable {
		t.Fatalf("unexpected reason %q", res.Reason)
	}
	for _, a := range res.Addresses {
		if a.Value == "198.51.100.1" {
			t.Fatal("status must not contain the unusable address")
		}
	}
}

func TestDerive_StaticNotYetAssigned(t *testing.T) {
	// LoadBalancer has not assigned anything yet.
	res := Derive(gw(ipAddr("203.0.113.10")), lbService(), readyDep(), nil)
	if res.Programmed || res.Reason != gwapiv1.GatewayReasonAddressNotUsable {
		t.Fatalf("expected AddressNotUsable, got programmed=%v reason=%q", res.Programmed, res.Reason)
	}
}

func TestAllAddressesSupported(t *testing.T) {
	bad := "test/fake-invalid-type"
	g := gw(gwapiv1.GatewaySpecAddress{Type: ptr.To(gwapiv1.AddressType(bad)), Value: "x"})
	if AllAddressesSupported(g) {
		t.Fatal("expected unsupported address type to be rejected")
	}
	if !AllAddressesSupported(gw(ipAddr("1.2.3.4"))) {
		t.Fatal("expected IPAddress to be supported")
	}
}
