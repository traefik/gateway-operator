# Deployment modes

The operator supports three topologies, selected through the `TraefikProxy`
resource referenced by the GatewayClass.

## Gateway namespace (default)

```yaml
spec:
  provisioning:
    mode: GatewayNamespace
```

The data plane is created in the Gateway's own namespace. Generated resources
carry an owner reference to the Gateway, so Kubernetes garbage-collects them on
Gateway deletion.

Best for: tenant isolation. The data plane lives and dies with the Gateway, in
the tenant's namespace, counted against the tenant's quotas.

## Dedicated namespace

```yaml
spec:
  provisioning:
    mode: Dedicated
    namespace: traefik-system   # optional, defaults to -data-plane-namespace
```

All data planes are created in one namespace, regardless of where their
Gateways live. Cross-namespace ownership is not expressible with owner
references, so the operator tracks each Gateway with a finalizer
(`gateway.traefik.io/data-plane`) and deletes the data plane explicitly when
the Gateway is deleted.

Best for: centralized operations. Data planes are managed, monitored and
secured in one place; tenants keep only their Gateway objects.

## Merged Gateways

```yaml
spec:
  mergeGateways: true
  provisioning:
    namespace: traefik-system   # optional
```

All Gateways of the GatewayClass share a single data plane, provisioned in the
dedicated namespace. The data plane listens on the union of all member listener
ports and is scoped to every member Gateway. The shared instance shrinks as
members leave and is torn down when the last member is deleted.

Listener conflicts across members (same port with different protocols, same
port and protocol with the same hostname) are detected and reported as
`Conflicted` listener conditions.

Best for: cost. This is the closest analog to Traefik's classic shared-instance
model, while keeping provisioning declarative.

## Resource naming and labels

Generated resources are named `traefik-<gateway-namespace>-<gateway-name>-<hash>`
(truncated to DNS limits; merged data planes use `traefik-merged-<class>-<hash>`)
and carry:

| Label | Purpose |
|---|---|
| `gateway.networking.k8s.io/gateway-name` | Gateway API standard, used by conformance |
| `gateway.traefik.io/gateway-namespace` | disambiguation across namespaces |
| `gateway.traefik.io/merge-group` | GatewayClass name, merged mode only |
| `app.kubernetes.io/managed-by: traefik-gateway-operator` | operator marker |

`spec.infrastructure.labels` and `annotations` from the Gateway are added to
every generated resource, including the Pod template.
