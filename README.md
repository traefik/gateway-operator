# traefik-operator

Kubernetes operator that provisions and manages Traefik data planes per Gateway
API `Gateway`, mirroring the Envoy Gateway model.

Status: Phases 0 to 2 implemented (see [docs/DESIGN.md](docs/DESIGN.md)
section 11). The operator provisions a data plane per Gateway, propagates
`spec.infrastructure` metadata, honors `spec.addresses` static addresses
(rejecting unsupported address types, gating `Programmed` on the address
actually being realized), supports LoadBalancer, ClusterIP and NodePort Service
types, derives Gateway status from the resources it created, and supports both
deployment modes: Gateway-namespace (owner-reference garbage collection) and
Dedicated namespace (finalizer based cleanup). Merged Gateways are not
implemented yet.

## Why

Traefik already routes Gateway API traffic through a single shared instance via
the in tree provider (`pkg/provider/kubernetes/gateway`). That static model
cannot satisfy Gateway API features that assume a data plane is provisioned per
Gateway:

- `GatewayStaticAddresses`
- `GatewayInfrastructurePropagation`
- a correct, per Gateway `status.addresses`

This operator provisions a Traefik `Deployment` plus `Service` per Gateway and
derives each Gateway status from the resources it created.

## What it is not

It does not reimplement Gateway API routing. Provisioned Traefik instances keep
using the existing in tree provider to translate routes. The operator owns
infrastructure provisioning and Gateway status only.

## Layout

- `api/v1alpha1`: the `TraefikProxy` CRD types (data plane configuration).
- `internal/provisioner`: renders the data plane resource set (ServiceAccount,
  Deployment, Service) for a Gateway.
- `internal/status`: derives `status.addresses` and the `Programmed` condition
  from the provisioned Service and Deployment, mirroring Envoy Gateway.
- `internal/controller`: the GatewayClass and Gateway reconcilers.
- `cmd`: the manager entrypoint.
- `config`: CRD, RBAC, manager Deployment, and sample manifests.

## Develop

```sh
make generate manifests   # regenerate deepcopy, CRD, RBAC
make build                # build the binary
make test                 # run unit tests
make run                  # run against the current kubeconfig
```

## Deploy

```sh
make install              # install the TraefikProxy CRD
make deploy               # install CRD + RBAC + operator Deployment
kubectl apply -f config/samples/gatewayclass.yaml
```

## Data plane flags

The provisioned Traefik instances run the existing in tree Gateway provider,
scoped to their own Gateway. Two provider options make this work, both
implemented in the in-tree provider (see DESIGN.md section 8):

- `--providers.kubernetesGateway.gateway=<namespace>/<name>`: scope the data
  plane to the single Gateway it was provisioned for.
- `--providers.kubernetesGateway.disableGatewayStatus=true`: stop the data
  plane from writing Gateway and GatewayClass status, so the operator is the
  single status writer. Route status is still written by the data plane.

## Design

See [docs/DESIGN.md](docs/DESIGN.md).
