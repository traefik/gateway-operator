# Traefik Operator: Design

Status: draft
Owners: @traefik/maintainers
Last updated: 2026-06-24

## 1. Motivation

Traefik already implements the Gateway API routing contract in
`pkg/provider/kubernetes/gateway` (Gateway, HTTPRoute, GRPCRoute, TLSRoute,
ReferenceGrant, BackendTLSPolicy translated into Traefik dynamic configuration).
That provider runs inside a single, statically deployed Traefik instance that
watches every Gateway in the cluster and routes all of them through one shared
data plane.

This static model passes the Gateway API conformance Core profiles and a large
share of Extended features, but it cannot satisfy the features that assume the
implementation provisions and owns a data plane per Gateway:

- `GatewayStaticAddresses`: honor user requested `spec.addresses` on the data
  plane that actually serves the Gateway.
- `GatewayInfrastructurePropagation`: propagate `spec.infrastructure.labels` and
  `spec.infrastructure.annotations` onto the resources generated for the Gateway.
- A semantically correct, per Gateway `status.addresses` (the static model can
  only report one shared address copied to every Gateway).

The current workaround is a provider level `statusAddress` option that copies a
single address into every Gateway status. It satisfies the letter of the
`GatewayAddressEmpty` test but is wrong as soon as Gateways are meant to carry
distinct addresses.

Envoy Gateway solves this by being an operator: it provisions a dedicated Envoy
data plane per Gateway and derives each Gateway status from the resources it
created. This document designs the equivalent for Traefik.

## 2. Goals and non goals

### Goals

1. Provision a Traefik data plane (Deployment or DaemonSet, Service,
   ServiceAccount) per Gateway, or per merged group of Gateways.
2. Populate each Gateway `status.addresses` from the Service the operator
   created for that Gateway, and gate the `Programmed` condition on the real
   data plane readiness.
3. Honor `spec.addresses` (static addresses) on the provisioned Service.
4. Propagate `spec.infrastructure.labels` and `spec.infrastructure.annotations`
   onto the generated resources.
5. Expose data plane configuration through a `TraefikProxy` CRD referenced from
   `GatewayClass.spec.parametersRef`, mirroring the `EnvoyProxy` model.
6. Support multiple deployment topologies: dedicated namespace, Gateway
   namespace, and merged Gateways.

### Non goals

1. Reimplementing Gateway API routing translation. The provisioned Traefik
   instances keep using the existing `pkg/provider/kubernetes/gateway` provider
   to turn routes into dynamic configuration. The operator provisions and
   configures data planes, it does not route traffic.
2. Replacing the Traefik Helm chart for users who are happy with a single shared
   instance. The operator is an additional, opt in deployment model.
3. Mesh profile support.

## 3. Architecture overview

Two planes, clearly separated:

```
                 control plane (new)                      data plane (existing Traefik)
  +-------------------------------------------+        +------------------------------+
  |  traefik-operator                         |        |  Traefik Deployment (per GW) |
  |                                           | create |   - kubernetesGateway        |
  |  GatewayClass controller                  +------->|     provider enabled         |
  |  Gateway provisioner (reconciler)         |        |   - watches its own Gateway  |
  |  Infrastructure manager (apply/GC)        |        |   - serves routes            |
  |  Status manager (addresses, Programmed)   |<-------+  Service (per GW)            |
  |  TraefikProxy config resolver             | watch  |   - LB / ClusterIP / NodePort|
  +-------------------------------------------+ status +------------------------------+
```

The operator is the only component that writes Gateway and GatewayClass status.
The provisioned Traefik instances are pure data planes; they are configured to
NOT write Gateway status (a new flag, see section 8), so there is a single
writer.

## 4. Components

### 4.1 GatewayClass controller

- Watches `GatewayClass` with `spec.controllerName == traefik.io/gateway-controller`.
- Sets `Accepted` and publishes `SupportedFeatures`.
- Resolves `spec.parametersRef` to a `TraefikProxy` resource and validates it.
  Invalid or missing parameters set `Accepted=False` with reason
  `InvalidParameters`.

### 4.2 Gateway provisioner (core reconciler)

For each accepted Gateway, reconciles the desired data plane:

1. Compute the effective `TraefikProxy` config (GatewayClass default merged with
   any Gateway scoped override).
2. Compute the infrastructure resource set (see 4.3).
3. Apply (server side apply) the resource set with the Gateway as owner.
4. Trigger the status manager (see 4.4).

Triggers: Gateway create/update/delete, owned resource changes, referenced
`TraefikProxy` changes, and the provisioned Service status changes.

### 4.3 Infrastructure manager

Owns the generated resource set per Gateway (or per merged group):

- `ServiceAccount`
- `Deployment` (default) or `DaemonSet` (configurable via `TraefikProxy`)
- `Service` (type from `TraefikProxy`, default `LoadBalancer`)
- optional `ConfigMap` for static Traefik configuration
- optional `PodDisruptionBudget`, `HorizontalPodAutoscaler`

Naming: `traefik-<gateway-namespace>-<gateway-name>-<hash>` to stay within label
and DNS limits, mirroring Envoy Gateway. All resources carry:

- `gateway.networking.k8s.io/gateway-name: <gateway-name>` (required by the
  `GatewayInfrastructure` conformance test)
- `app.kubernetes.io/managed-by: traefik-operator`
- the Gateway `spec.infrastructure.labels` and `annotations`

Resources set `metadata.ownerReferences` to the Gateway so Kubernetes garbage
collects them when the Gateway is deleted (within namespace scope; cross
namespace provisioning uses a finalizer plus explicit cleanup, see 6).

### 4.4 Status manager

Single writer for Gateway status. Logic mirrors Envoy Gateway
`UpdateGatewayStatusProgrammedCondition`:

1. Service not created yet but `spec.addresses` set: populate `status.addresses`
   from spec, set `Programmed=False`, reason `AddressNotUsable`.
2. Service present, no `spec.addresses`: derive addresses from the Service:
   - `LoadBalancer`: the LoadBalancer ingress IP or hostname.
   - `ClusterIP`: the cluster IPs (filtering `None`).
   - `NodePort`: node addresses.
3. Service present and `spec.addresses` set (static addresses): use the Service
   `externalIPs` or cluster IPs; hostname addresses that cannot be bound report
   `AddressNotUsable`.
4. `Programmed=True` only when `status.addresses` is non empty AND the data plane
   Deployment or DaemonSet reports ready replicas. Cap at 16 addresses.

### 4.5 TraefikProxy config resolver

Resolves and merges configuration (section 5) used by the infrastructure manager
to render the data plane.

## 5. TraefikProxy CRD

Group: `gateway.traefik.io`, Version: `v1alpha1`, Kind: `TraefikProxy`.
Referenced from `GatewayClass.spec.parametersRef` (cluster default) and
optionally overridden per Gateway through an annotation or a future
`infrastructure.parametersRef` once the upstream API supports it.

Indicative schema (not exhaustive):

```yaml
apiVersion: gateway.traefik.io/v1alpha1
kind: TraefikProxy
metadata:
  name: default
spec:
  provider:
    kubernetes:
      # how the data plane Service is exposed
      envoyService:        # name kept generic, see open questions
        type: LoadBalancer # LoadBalancer | ClusterIP | NodePort
        annotations: {}
        loadBalancerClass: ""
      deployment:
        replicas: 2
        strategy: {}
        pod:
          annotations: {}
          labels: {}
          tolerations: []
          nodeSelector: {}
          affinity: {}
        container:
          image: traefik:v3.7
          resources: {}
          env: []
    # daemonset alternative
    # daemonSet: { ... }
  traefik:
    # passthrough of static Traefik configuration knobs the operator is
    # allowed to set (entrypoints, log level, metrics, etc.)
    entryPoints:
      web:      { address: ":8080" }
      websecure:{ address: ":8443" }
    logs:
      level: INFO
  mergeGateways: false
  telemetry: {}
```

The resolver merges the GatewayClass default `TraefikProxy` with any Gateway
scoped override (last wins, deep merge of maps and lists by key).

## 6. Deployment modes

Mirror the Envoy Gateway topologies:

1. Dedicated namespace (default). All data planes live in the operator namespace
   (for example `traefik-system`). Cross namespace ownership is not expressible
   through `ownerReferences`, so the operator uses a finalizer on the Gateway and
   deletes owned resources explicitly. Resources are labeled with the owning
   Gateway namespace and name.
2. Gateway namespace mode. The data plane is provisioned in the Gateway own
   namespace. `ownerReferences` work directly and Kubernetes garbage collects on
   Gateway deletion. This mode is what the `GatewayInfrastructure` conformance
   test exercises (it lists ServiceAccounts, Pods, Services in the Gateway
   namespace and checks the `gateway-name` label and propagated metadata).
3. Merged Gateways. When `mergeGateways: true`, all Gateways of a GatewayClass
   share one data plane. Listeners are merged by port and protocol; conflicting
   listeners are rejected with a listener condition. `status.addresses` for every
   merged Gateway points at the shared Service. This is the cost optimized mode
   and the closest analog to today static Traefik instance.

## 7. Reconciliation flow (per Gateway)

```
Gateway event
   |
   v
resolve TraefikProxy config (GatewayClass default + override)
   |
   v
render resource set (SA, Deployment/DaemonSet, Service, [ConfigMap])
   - inject gateway-name label
   - inject spec.infrastructure.labels + annotations
   - set ownerReferences (or finalizer in dedicated-namespace mode)
   |
   v
server-side apply resource set
   |
   v
read provisioned Service + Deployment status
   |
   v
status manager:
   - compute status.addresses (LB ingress / clusterIP / nodePort / spec.addresses)
   - set Accepted, Programmed (gated on ready replicas)
   |
   v
write Gateway status (single writer)
```

## 8. Relationship to the existing Traefik Gateway provider

The provisioned Traefik instances run the existing
`pkg/provider/kubernetes/gateway` provider to translate routes attached to their
Gateway into dynamic configuration. Two provider options make this work, both
implemented in the in-tree provider:

1. `--providers.kubernetesGateway.gateway=<namespace>/<name>` scopes a data
   plane instance to the single Gateway it was provisioned for, instead of all
   Gateways in the cluster. Without this, every provisioned instance would try
   to serve every Gateway. A label selector or a comma list of references can be
   added later for merged Gateways.
2. `--providers.kubernetesGateway.disableGatewayStatus=true` stops the data
   plane from writing Gateway and GatewayClass status, so the operator is the
   single status writer. Route status is still written by the data plane, which
   is the component that actually routes.

The operator does not duplicate routing logic. This keeps the routing
conformance surface (the bulk of the features) in one place.

## 9. Feature coverage matrix

| Capability                              | Static instance (today) | Operator (this design) |
|-----------------------------------------|--------------------------|------------------------|
| Core HTTP / GRPC / TLS routing          | yes                      | yes (delegated)        |
| Extended HTTPRoute features             | yes                      | yes (delegated)        |
| `GatewayAddressEmpty`                   | workaround (shared addr) | yes (per Gateway)      |
| `GatewayOptionalAddressValue`           | workaround (shared addr) | yes (per Gateway)      |
| `GatewayStaticAddresses`                | no                       | yes                    |
| `GatewayInfrastructurePropagation`      | no                       | yes                    |
| Per Gateway `status.addresses`          | no (shared)              | yes                    |
| `Programmed` gated on real data plane   | no                       | yes                    |
| Merged Gateways                         | implicit (one instance)  | yes (opt in)           |

## 10. RBAC

The operator needs cluster scoped permissions to:

- get, list, watch: `gateways`, `gatewayclasses`, `httproutes`, `grpcroutes`,
  `tlsroutes`, `tcproutes`, `referencegrants`, `backendtlspolicies`,
  `traefikproxies`.
- update, patch: `gateways/status`, `gatewayclasses/status`.
- create, update, patch, delete: `deployments`, `daemonsets`, `services`,
  `serviceaccounts`, `configmaps`, and optionally `poddisruptionbudgets`,
  `horizontalpodautoscalers`, in the namespaces it provisions into.
- create, patch: `events`.

Provisioned data plane ServiceAccounts get the route read permissions the
existing Gateway provider already requires.

## 11. Phasing

1. Phase 0: repository scaffolding, `TraefikProxy` CRD, GatewayClass controller,
   Gateway provisioner in Gateway namespace mode, Deployment plus LoadBalancer
   Service, status.addresses from LB ingress, `Programmed` gating. Unlocks
   per Gateway addresses and `GatewayInfrastructurePropagation`.
2. Phase 1: `spec.addresses` static address support, NodePort and ClusterIP
   Service types. Unlocks `GatewayStaticAddresses`.
3. Phase 2: dedicated namespace mode plus finalizer based GC.
4. Phase 3: merged Gateways.
5. Phase 4: autoscaling, PDB, advanced `TraefikProxy` passthrough, metrics and
   tracing wiring.

## 12. Open questions

1. CRD naming and grouping: `TraefikProxy` in `gateway.traefik.io` versus reusing
   the existing `traefik.io` CRD group. Needs alignment with the Traefik CRD
   conventions.
2. Per Gateway config override mechanism before the upstream API exposes a field
   for it: annotation versus a Traefik specific policy attachment.
3. How much static Traefik configuration to expose through `TraefikProxy` versus
   keeping it minimal and opinionated.
4. Whether the data plane scoping flag should be a single Gateway ref or a label
   selector (relevant for merged Gateways).
5. Image and version pinning policy for the provisioned Traefik (operator pins a
   compatible Traefik version, or user selectable).
