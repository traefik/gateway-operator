# Deployment topology

The operator provisions one data plane per Gateway, in the Gateway's own
namespace.

## Gateway namespace

The data plane is created in the Gateway's own namespace. Generated resources
carry an owner reference to the Gateway, so Kubernetes garbage-collects them on
Gateway deletion.

The only exception is the cluster-scoped `ClusterRoleBinding` granting the data
plane ServiceAccount its role: an owner reference cannot point from a
cluster-scoped object to a namespaced one, so the operator tracks each Gateway
with a finalizer (`gateway.traefik.io/data-plane`) and deletes the binding
explicitly when the Gateway is deleted.

Best for: tenant isolation. The data plane lives and dies with the Gateway, in
the tenant's namespace, counted against the tenant's quotas.

## Resource naming and labels

Generated resources are named `traefik-<gateway-namespace>-<gateway-name>-<hash>`
(truncated to DNS limits) and carry:

| Label | Purpose |
|---|---|
| `gateway.networking.k8s.io/gateway-name` | Gateway API standard, used by conformance |
| `gateway.traefik.io/gateway-namespace` | Gateway namespace, keeps selectors unambiguous |
| `app.kubernetes.io/managed-by: traefik-gateway-operator` | operator marker |

`spec.infrastructure.labels` and `annotations` from the Gateway are added to
every generated resource, including the Pod template.