// Package provisioner renders and names the data plane resource set for a Gateway.
package provisioner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// ControllerName is the Gateway API controller name claimed by the operator.
	// It is intentionally the same name the in-tree Traefik provider uses, so a
	// cluster runs either the static provider or the operator, never both.
	ControllerName = "traefik.io/gateway-controller"

	// GatewayNameLabel is required by the GatewayInfrastructure conformance test.
	GatewayNameLabel = "gateway.networking.k8s.io/gateway-name"

	// GatewayNamespaceLabel disambiguates data planes of Gateways that share a
	// name across namespaces, which happens in Dedicated mode where every data
	// plane lives in one namespace.
	GatewayNamespaceLabel = "gateway.traefik.io/gateway-namespace"

	// ManagedByLabel marks resources owned by the operator.
	ManagedByLabel = "app.kubernetes.io/managed-by"

	// ManagedByValue is the value of ManagedByLabel.
	ManagedByValue = "traefik-operator"

	// DefaultImage is the data plane image used when none is configured.
	DefaultImage = "traefik:v3.7"
)

// ResourceName returns the deterministic name of the resource set provisioned
// for a Gateway: traefik-<ns>-<name>-<hash>, truncated to stay within DNS and
// label limits. The hash disambiguates names that collide after truncation.
func ResourceName(gw *gwapiv1.Gateway) string {
	base := fmt.Sprintf("traefik-%s-%s", gw.Namespace, gw.Name)
	sum := sha256.Sum256([]byte(gw.Namespace + "/" + gw.Name))
	suffix := hex.EncodeToString(sum[:])[:8]

	const maxBase = 63 - 1 - 8 // 63 DNS label limit, dash, 8 hash chars.
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	return base + "-" + suffix
}
