# traefik-operator

Kubernetes operator that provisions and manages Traefik data planes per Gateway
API `Gateway`, mirroring the Envoy Gateway model.

Status: design phase. No implementation yet.

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

## Design

See [docs/DESIGN.md](docs/DESIGN.md).
