# Conformance

The operator is validated against the upstream
[Gateway API conformance suite](https://github.com/kubernetes-sigs/gateway-api/tree/main/conformance)
v1.5.1.

## Results

GATEWAY-HTTP profile, kind (Kubernetes 1.36) with MetalLB, Traefik data plane
built with the `gateway` and `disableGatewayClassStatus` provider options. The
generated report is committed at
[`conformance/reports/v1.5.1/experimental-dev-default-report.yaml`](../conformance/reports/v1.5.1/experimental-dev-default-report.yaml):

| GATEWAY-HTTP profile | Result |
|---|---|
| Core | success, 33/33 |
| Extended | 21/22 (one known failure, see below) |
| Succeeded provisional tests | `GatewayInfrastructure`, `TLSRouteMixedTerminationSameNamespace` |

Across the whole suite invocation (including tests outside the GATEWAY-HTTP
profile) the run passes 68/73. The per-Gateway provisioning model unlocks the
tests the static shared-instance model cannot support:
`GatewayStaticAddresses` (user-requested addresses honored and gated on
realization) and `GatewayInfrastructure` (metadata propagated onto generated
resources).

### Known failures

| Test | Cause |
|---|---|
| `TLSRouteSimpleSameNamespace`, `TLSRouteTerminateSimpleSameNamespace`, `TLSRouteHostnameIntersection`, `TLSRouteInvalidBackendRefUnknownKind` | Environment: TLS traffic from the test host to fresh MetalLB IPs is unreliable on kind under OrbStack (macOS). The full chain, client TLS through the LB to the raw TCP echo backend, works from inside the cluster. Expected to pass on a Linux cluster with routable LB addresses. |
| `BackendTLSPolicy` (one subtest) | Real gap in the Traefik data plane: rotating the content of a ConfigMap used as CA by a `BackendTLSPolicy` is not re-reflected without a restart. Tracked as upstream Traefik work. |

## Reproducing

1. Create a cluster with routable LoadBalancer addresses (kind + MetalLB, or a
   cloud cluster).
2. Install the Gateway API v1.5.1 experimental CRDs, the operator, a
   GatewayClass named `traefik` and a `TraefikProxy` with a LoadBalancer
   Service (see [Getting started](getting-started.md)).
3. Run the suite from a checkout of the Traefik repository (or any module
   depending on `sigs.k8s.io/gateway-api/conformance`):

```go
package conformance_e2e

import (
	"testing"

	"sigs.k8s.io/gateway-api/conformance"
)

func TestConformance(t *testing.T) {
	conformance.RunConformance(t)
}
```

```sh
go test ./conformance_e2e/ -run TestConformance -v -timeout 3600s -args \
  -gateway-class=traefik \
  -conformance-profiles=GATEWAY-HTTP \
  -usable-address=<free IP in the LB pool> \
  -unusable-address=<IP outside any pool> \
  -report-output=/tmp/conformance-report.yaml
```

`-usable-address` must be an address the load balancer can actually assign
(`GatewayStaticAddresses` verifies the address is realized);
`-unusable-address` must be one it cannot.

The suite discovers supported features from the GatewayClass status, which the
operator publishes: no `-supported-features` flag is needed for the profile
run.
