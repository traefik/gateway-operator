package controller

import (
	"sort"

	"k8s.io/apimachinery/pkg/util/sets"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/gateway-api/pkg/features"
)

// supportedFeatures returns the Gateway API features the operator and its
// provisioned Traefik data plane support, published on the GatewayClass status.
// Routing features mirror the in-tree kubernetesGateway provider; the operator
// adds the Gateway features it implements itself (static addresses,
// infrastructure propagation).
func supportedFeatures() []gwapiv1.SupportedFeature {
	featureSet := sets.New[features.Feature]().
		Insert(features.GatewayCoreFeatures.UnsortedList()...).
		Insert(features.GatewayExtendedFeatures.Intersection(extendedGatewayFeatures()).UnsortedList()...).
		Insert(features.HTTPRouteCoreFeatures.UnsortedList()...).
		Insert(features.HTTPRouteExtendedFeatures.Intersection(extendedHTTPRouteFeatures()).UnsortedList()...).
		Insert(features.ReferenceGrantCoreFeatures.UnsortedList()...).
		Insert(features.BackendTLSPolicyCoreFeatures.UnsortedList()...).
		Insert(features.GRPCRouteCoreFeatures.UnsortedList()...).
		Insert(features.TLSRouteCoreFeatures.UnsortedList()...).
		Insert(features.TLSRouteExtendedFeatures.Intersection(extendedTLSRouteFeatures()).UnsortedList()...)

	out := make([]gwapiv1.SupportedFeature, 0, featureSet.Len())
	for f := range featureSet {
		out = append(out, gwapiv1.SupportedFeature{Name: gwapiv1.FeatureName(f.Name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// extendedGatewayFeatures are the extended Gateway features the operator
// supports: the in-tree provider's GatewayPort8080 plus the two features the
// operator itself implements.
func extendedGatewayFeatures() sets.Set[features.Feature] {
	return sets.New(
		features.GatewayPort8080Feature,
		features.GatewayStaticAddressesFeature,
		features.GatewayInfrastructurePropagationFeature,
	)
}

func extendedTLSRouteFeatures() sets.Set[features.Feature] {
	return sets.New(
		features.TLSRouteModeTerminateFeature,
		features.TLSRouteModeMixedFeature,
	)
}

func extendedHTTPRouteFeatures() sets.Set[features.Feature] {
	return sets.New(
		features.HTTPRouteQueryParamMatchingFeature,
		features.HTTPRouteMethodMatchingFeature,
		features.HTTPRoutePortRedirectFeature,
		features.HTTPRouteSchemeRedirectFeature,
		features.HTTPRouteHostRewriteFeature,
		features.HTTPRoutePathRewriteFeature,
		features.HTTPRoutePathRedirectFeature,
		features.HTTPRouteResponseHeaderModificationFeature,
		features.HTTPRouteBackendProtocolH2CFeature,
		features.HTTPRouteBackendProtocolWebSocketFeature,
		features.HTTPRouteDestinationPortMatchingFeature,
		features.HTTPRouteBackendRequestHeaderModificationFeature,
	)
}
