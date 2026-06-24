package provisioner

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func listener(name string, port gwapiv1.PortNumber, proto gwapiv1.ProtocolType, host string) gwapiv1.Listener {
	l := gwapiv1.Listener{Name: gwapiv1.SectionName(name), Port: port, Protocol: proto}
	if host != "" {
		l.Hostname = ptr.To(gwapiv1.Hostname(host))
	}
	return l
}

func mkGateway(ns, name string, ls ...gwapiv1.Listener) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       gwapiv1.GatewaySpec{Listeners: ls},
	}
}

func TestDetectListenerConflicts(t *testing.T) {
	t.Run("no conflict for distinct hostnames on same port", func(t *testing.T) {
		gws := []*gwapiv1.Gateway{
			mkGateway("a", "g1", listener("h", 443, gwapiv1.HTTPSProtocolType, "a.example.com")),
			mkGateway("b", "g2", listener("h", 443, gwapiv1.HTTPSProtocolType, "b.example.com")),
		}
		if got := DetectListenerConflicts(gws); len(got) != 0 {
			t.Fatalf("expected no conflicts, got %v", got)
		}
	})

	t.Run("protocol conflict on same port", func(t *testing.T) {
		gws := []*gwapiv1.Gateway{
			mkGateway("a", "g1", listener("h", 80, gwapiv1.HTTPProtocolType, "")),
			mkGateway("b", "g2", listener("t", 80, gwapiv1.TCPProtocolType, "")),
		}
		got := DetectListenerConflicts(gws)
		if len(got) != 2 {
			t.Fatalf("expected both listeners conflicted, got %v", got)
		}
		for ref, reason := range got {
			if reason != gwapiv1.ListenerReasonProtocolConflict {
				t.Fatalf("expected ProtocolConflict for %v, got %q", ref, reason)
			}
		}
	})

	t.Run("hostname conflict on same port and protocol", func(t *testing.T) {
		gws := []*gwapiv1.Gateway{
			mkGateway("a", "g1", listener("h", 80, gwapiv1.HTTPProtocolType, "")),
			mkGateway("b", "g2", listener("h", 80, gwapiv1.HTTPProtocolType, "")),
		}
		got := DetectListenerConflicts(gws)
		if len(got) != 2 {
			t.Fatalf("expected both listeners conflicted, got %v", got)
		}
		for ref, reason := range got {
			if reason != gwapiv1.ListenerReasonHostnameConflict {
				t.Fatalf("expected HostnameConflict for %v, got %q", ref, reason)
			}
		}
	})
}

func TestRenderMerged(t *testing.T) {
	gws := []*gwapiv1.Gateway{
		mkGateway("a", "g1", listener("http", 80, gwapiv1.HTTPProtocolType, "")),
		mkGateway("b", "g2", listener("https", 443, gwapiv1.HTTPSProtocolType, "x.example.com")),
	}

	set := RenderMerged("traefik", gws, nil, "traefik-system")

	if set.Deployment.Namespace != "traefik-system" {
		t.Fatalf("unexpected namespace %q", set.Deployment.Namespace)
	}
	if set.Deployment.Name != MergedResourceName("traefik") {
		t.Fatalf("unexpected name %q", set.Deployment.Name)
	}
	if set.Deployment.Labels[MergeGroupLabel] != "traefik" {
		t.Fatalf("expected merge-group label, got %q", set.Deployment.Labels[MergeGroupLabel])
	}

	// Union of ports across both Gateways.
	if len(set.Service.Spec.Ports) != 2 {
		t.Fatalf("expected 2 service ports (union), got %d", len(set.Service.Spec.Ports))
	}

	// Scoping argument lists both Gateways.
	var gatewayArg string
	for _, a := range set.Deployment.Spec.Template.Spec.Containers[0].Args {
		if len(a) > len("--providers.kubernetesGateway.gateway=") && a[:len("--providers.kubernetesGateway.gateway=")] == "--providers.kubernetesGateway.gateway=" {
			gatewayArg = a
		}
	}
	want := "--providers.kubernetesGateway.gateway=a/g1,b/g2"
	if gatewayArg != want {
		t.Fatalf("unexpected scoping arg: %q want %q", gatewayArg, want)
	}
}
