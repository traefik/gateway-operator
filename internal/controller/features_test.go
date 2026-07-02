package controller

import (
	"testing"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// TestSupportedFeatures guards the published feature set against regressions:
// the operator must declare everything the Traefik v3.7 data plane declares,
// plus the Gateway features the operator implements itself. A feature missing
// here makes the conformance suite skip its tests.
func TestSupportedFeatures(t *testing.T) {
	got := map[gwapiv1.FeatureName]struct{}{}
	for _, f := range supportedFeatures() {
		got[f.Name] = struct{}{}
	}

	want := []gwapiv1.FeatureName{
		// Operator-implemented Gateway features.
		"GatewayStaticAddresses",
		"GatewayInfrastructurePropagation",
		// Mirrored from the Traefik v3.7 in-tree provider.
		"GatewayPort8080",
		"HTTPRoute303RedirectStatusCode",
		"HTTPRoute307RedirectStatusCode",
		"HTTPRoute308RedirectStatusCode",
		"HTTPRouteNamedRouteRule",
		"HTTPRouteParentRefPort",
		"HTTPRouteQueryParamMatching",
		"HTTPRouteMethodMatching",
		"HTTPRoutePortRedirect",
		"HTTPRouteSchemeRedirect",
		"HTTPRouteHostRewrite",
		"HTTPRoutePathRewrite",
		"HTTPRoutePathRedirect",
		"HTTPRouteResponseHeaderModification",
		"HTTPRouteBackendProtocolH2C",
		"HTTPRouteBackendProtocolWebSocket",
		"HTTPRouteDestinationPortMatching",
		"HTTPRouteBackendRequestHeaderModification",
		"GRPCRouteNamedRouteRule",
		"TLSRouteModeTerminate",
		"TLSRouteModeMixed",
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("missing supported feature %q", name)
		}
	}
}
