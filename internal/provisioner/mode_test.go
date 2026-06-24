package provisioner

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/traefik/traefik-operator/api/v1alpha1"
)

func TestResolve(t *testing.T) {
	gw := &gwapiv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "gw"}}

	testCases := []struct {
		desc      string
		proxy     *v1alpha1.TraefikProxy
		defaultNS string
		wantMode  v1alpha1.DeploymentMode
		wantNS    string
	}{
		{
			desc:      "no proxy defaults to gateway namespace",
			proxy:     nil,
			defaultNS: "traefik-system",
			wantMode:  v1alpha1.GatewayNamespaceMode,
			wantNS:    "app",
		},
		{
			desc:      "dedicated uses proxy namespace",
			proxy:     &v1alpha1.TraefikProxy{Spec: v1alpha1.TraefikProxySpec{Provisioning: &v1alpha1.ProvisioningConfig{Mode: v1alpha1.DedicatedMode, Namespace: "dp"}}},
			defaultNS: "traefik-system",
			wantMode:  v1alpha1.DedicatedMode,
			wantNS:    "dp",
		},
		{
			desc:      "dedicated falls back to operator default namespace",
			proxy:     &v1alpha1.TraefikProxy{Spec: v1alpha1.TraefikProxySpec{Provisioning: &v1alpha1.ProvisioningConfig{Mode: v1alpha1.DedicatedMode}}},
			defaultNS: "traefik-system",
			wantMode:  v1alpha1.DedicatedMode,
			wantNS:    "traefik-system",
		},
	}

	for _, test := range testCases {
		t.Run(test.desc, func(t *testing.T) {
			got := Resolve(gw, test.proxy, test.defaultNS)
			if got.Mode != test.wantMode {
				t.Fatalf("mode: got %q want %q", got.Mode, test.wantMode)
			}
			if got.Namespace != test.wantNS {
				t.Fatalf("namespace: got %q want %q", got.Namespace, test.wantNS)
			}
			if got.Dedicated() != (test.wantMode == v1alpha1.DedicatedMode) {
				t.Fatalf("Dedicated() mismatch")
			}
		})
	}
}
