// Package provisioner renders and names the data plane resource set for a Gateway.
package provisioner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// ControllerName is the Gateway API controller name claimed by the operator.
	// It is intentionally the same name the in-tree Traefik provider uses, so a
	// cluster runs either the static provider or the operator, never both.
	ControllerName = "traefik.io/gateway-controller"

	// GatewayNameLabel is required by the GatewayInfrastructure conformance test.
	GatewayNameLabel = "gateway.networking.k8s.io/gateway-name"

	// GatewayNamespaceLabel records the namespace of the Gateway a data plane
	// belongs to, keeping the selector labels unambiguous.
	GatewayNamespaceLabel = "gateway.traefik.io/gateway-namespace"

	// ManagedByLabel marks resources owned by the operator.
	ManagedByLabel = "app.kubernetes.io/managed-by"

	// ManagedByValue is the value of ManagedByLabel.
	ManagedByValue = "traefik-gateway-operator"

	// DefaultImage is the data plane image used when none is configured.
	DefaultImage = "traefik:v3.7"

	// DNS label and label value limit.
	maxNameLength = 63

	// Room for a dash and 8 hash chars.
	maxHashedBase = maxNameLength - 1 - 8
)

// ResourceName returns the deterministic name of the resource set provisioned
// for a Gateway: traefik-<ns>-<name>-<hash>, truncated to stay within DNS and
// label limits. The hash disambiguates names that collide after truncation.
// Dots allowed in Gateway names are replaced, as Service names must be DNS-1035
// labels.
func ResourceName(gw *gwapiv1.Gateway) string {
	return strings.ReplaceAll(hashedName(fmt.Sprintf("traefik-%s-%s", gw.Namespace, gw.Name)), ".", "-")
}

// LabelName returns v unchanged when it fits the 63 character label value
// limit, otherwise a truncated value suffixed with a hash of v so that distinct
// long values stay distinct. Gateway names can be up to 253 characters.
func LabelName(v string) string {
	if len(v) <= maxNameLength {
		return v
	}
	return hashedName(v)
}

// hashedName returns base truncated to fit with a dash and the first 8 hex
// chars of the sha256 of the full base.
func hashedName(base string) string {
	sum := sha256.Sum256([]byte(base))
	if len(base) > maxHashedBase {
		base = base[:maxHashedBase]
	}
	// Names and label values must end with an alphanumeric character.
	return strings.TrimRight(base, "-_.") + "-" + hex.EncodeToString(sum[:])[:8]
}
