package provisioner

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func gwWithAddresses(addrs ...gwapiv1.GatewaySpecAddress) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{Spec: gwapiv1.GatewaySpec{Addresses: addrs}}
}

func TestStaticAddressRejection(t *testing.T) {
	t.Run("no spec addresses is not rejected", func(t *testing.T) {
		if conds := StaticAddressRejection(&gwapiv1.Gateway{}); conds != nil {
			t.Fatalf("expected no rejection, got %+v", conds)
		}
	})

	t.Run("supported IP type is not rejected", func(t *testing.T) {
		gw := gwWithAddresses(gwapiv1.GatewaySpecAddress{Type: ptr.To(gwapiv1.IPAddressType), Value: "1.2.3.4"})
		if conds := StaticAddressRejection(gw); conds != nil {
			t.Fatalf("expected no rejection, got %+v", conds)
		}
	})

	t.Run("nil type defaults to IP and is not rejected", func(t *testing.T) {
		gw := gwWithAddresses(gwapiv1.GatewaySpecAddress{Value: "1.2.3.4"})
		if conds := StaticAddressRejection(gw); conds != nil {
			t.Fatalf("expected no rejection, got %+v", conds)
		}
	})

	t.Run("supported hostname type is not rejected", func(t *testing.T) {
		gw := gwWithAddresses(gwapiv1.GatewaySpecAddress{Type: ptr.To(gwapiv1.HostnameAddressType), Value: "lb.example.com"})
		if conds := StaticAddressRejection(gw); conds != nil {
			t.Fatalf("expected no rejection, got %+v", conds)
		}
	})

	t.Run("unsupported type is rejected", func(t *testing.T) {
		gw := gwWithAddresses(gwapiv1.GatewaySpecAddress{
			Type: ptr.To(gwapiv1.AddressType("test/fake")), Value: "x",
		})
		conds := StaticAddressRejection(gw)
		if len(conds) != 2 {
			t.Fatalf("expected Accepted and Programmed conditions, got %+v", conds)
		}
		if conds[0].Type != string(gwapiv1.GatewayConditionAccepted) ||
			conds[0].Reason != string(gwapiv1.GatewayReasonUnsupportedAddress) ||
			conds[0].Status != metav1.ConditionFalse {
			t.Fatalf("unexpected Accepted condition: %+v", conds[0])
		}
		if conds[1].Type != string(gwapiv1.GatewayConditionProgrammed) ||
			conds[1].Reason != string(gwapiv1.GatewayReasonInvalid) ||
			conds[1].Status != metav1.ConditionFalse {
			t.Fatalf("unexpected Programmed condition: %+v", conds[1])
		}
	})

	t.Run("one unsupported type among supported ones is rejected", func(t *testing.T) {
		gw := gwWithAddresses(
			gwapiv1.GatewaySpecAddress{Type: ptr.To(gwapiv1.IPAddressType), Value: "1.2.3.4"},
			gwapiv1.GatewaySpecAddress{Type: ptr.To(gwapiv1.AddressType("test/fake")), Value: "x"},
		)
		if conds := StaticAddressRejection(gw); len(conds) != 2 {
			t.Fatalf("expected a rejection, got %+v", conds)
		}
	})
}

func specIP(v string) gwapiv1.GatewaySpecAddress {
	return gwapiv1.GatewaySpecAddress{Type: ptr.To(gwapiv1.IPAddressType), Value: v}
}

func TestStaticAddressStatus_NoSpecAddresses(t *testing.T) {
	realized := []gwapiv1.GatewayStatusAddress{{Type: ptr.To(gwapiv1.IPAddressType), Value: "1.2.3.4"}}

	addrs, conds, provision := StaticAddressStatus(&gwapiv1.Gateway{}, realized)
	if !provision {
		t.Fatal("expected to provision")
	}
	if addrs != nil || conds != nil {
		t.Fatalf("expected the operator to publish nothing, got addrs=%+v conds=%+v", addrs, conds)
	}
}

func TestStaticAddressStatus_AllRealized(t *testing.T) {
	realized := []gwapiv1.GatewayStatusAddress{{Type: ptr.To(gwapiv1.IPAddressType), Value: "1.2.3.4"}}

	addrs, conds, provision := StaticAddressStatus(gwWithAddresses(specIP("1.2.3.4")), realized)
	if !provision {
		t.Fatal("expected to provision")
	}
	if addrs != nil || conds != nil {
		t.Fatalf("expected the operator to publish nothing, got addrs=%+v conds=%+v", addrs, conds)
	}
}

func TestStaticAddressStatus_UnrealizedAddress(t *testing.T) {
	realized := []gwapiv1.GatewayStatusAddress{{Type: ptr.To(gwapiv1.IPAddressType), Value: "1.2.3.4"}}

	gw := gwWithAddresses(specIP("9.9.9.9"), specIP("1.2.3.4"))
	addrs, conds, provision := StaticAddressStatus(gw, realized)
	if provision {
		t.Fatal("expected not to provision")
	}
	// Only the realized subset is reported.
	if len(addrs) != 1 || addrs[0].Value != "1.2.3.4" {
		t.Fatalf("expected only the realized address, got %+v", addrs)
	}
	accepted := findCond(conds, gwapiv1.GatewayConditionAccepted)
	if accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Fatalf("expected Accepted=True, got %+v", conds)
	}
	programmed := findCond(conds, gwapiv1.GatewayConditionProgrammed)
	if programmed == nil || programmed.Status != metav1.ConditionFalse || programmed.Reason != string(gwapiv1.GatewayReasonAddressNotUsable) {
		t.Fatalf("expected Programmed=False/AddressNotUsable, got %+v", conds)
	}
}

func TestStaticAddressStatus_NoRealizedAddress(t *testing.T) {
	_, _, provision := StaticAddressStatus(gwWithAddresses(specIP("1.2.3.4")), nil)
	if provision {
		t.Fatal("expected not to provision without a realized address")
	}
}

func TestRealizedAddresses(t *testing.T) {
	svc := &corev1.Service{
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{IP: "1.2.3.4"},
					{Hostname: "lb.example.com"},
					{}, // empty ingress entries are skipped
				},
			},
		},
	}

	addrs := RealizedAddresses(svc)
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses, got %+v", addrs)
	}
	if *addrs[0].Type != gwapiv1.IPAddressType || addrs[0].Value != "1.2.3.4" {
		t.Fatalf("unexpected first address: %+v", addrs[0])
	}
	if *addrs[1].Type != gwapiv1.HostnameAddressType || addrs[1].Value != "lb.example.com" {
		t.Fatalf("unexpected second address: %+v", addrs[1])
	}

	if RealizedAddresses(nil) != nil {
		t.Fatal("expected nil for a nil service")
	}
}

func findCond(conds []metav1.Condition, t gwapiv1.GatewayConditionType) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == string(t) {
			return &conds[i]
		}
	}
	return nil
}
