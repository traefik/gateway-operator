# Configuration reference

## TraefikProxy

`TraefikProxy` (`gateway.traefik.io/v1alpha1`, cluster-scoped) describes how the
operator deploys data planes. It is referenced from
`GatewayClass.spec.parametersRef` and applies to every Gateway of that class.

```yaml
apiVersion: gateway.traefik.io/v1alpha1
kind: TraefikProxy
metadata:
  name: default
spec:
  deployment:
    replicas: 2
    image: traefik:v3.7
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
    podLabels:
      team: platform
    podAnnotations:
      prometheus.io/scrape: "true"

  service:
    type: LoadBalancer        # LoadBalancer | ClusterIP | NodePort
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-type: nlb
    loadBalancerClass: my-lb-class

  autoscaling:
    minReplicas: 2
    maxReplicas: 10
    targetCPUUtilizationPercentage: 80

  podDisruptionBudget:
    minAvailable: 1

  logs:
    level: INFO

  metrics:
    prometheus: true          # metrics entry point on container port 9100

  tracing:
    otlpEndpoint: http://otel-collector:4318
```

### deployment

| Field | Default | Description |
|---|---|---|
| `replicas` | `1` | Data plane replica count. Ignored when `autoscaling` is set. |
| `image` | `traefik:v3.7` | Traefik container image. |
| `resources` | none | Standard container resource requirements. |
| `podLabels`, `podAnnotations` | none | Added to the data plane Pods. |

### service

| Field | Default | Description |
|---|---|---|
| `type` | `LoadBalancer` | How the data plane is exposed. The Gateway address is derived from it: LoadBalancer ingress, cluster IPs, or node addresses for NodePort. |
| `annotations` | none | Added to the Service (cloud LB tuning). |
| `loadBalancerClass` | none | Selects the load balancer implementation. |

The data plane is always provisioned in the Gateway's own namespace. See
[Deployment modes](deployment-modes.md).

### autoscaling, podDisruptionBudget

When `autoscaling` is set, the operator creates a `HorizontalPodAutoscaler`
targeting the data plane Deployment and stops managing the replica count.
`podDisruptionBudget` creates a `PodDisruptionBudget` selecting the data plane
Pods. Both are removed when their configuration is removed.

### logs, metrics, tracing

| Field | Effect on the data plane |
|---|---|
| `logs.level` | `--log.level=<level>` |
| `metrics.prometheus: true` | dedicated `metrics` entry point on container port 9100, `--metrics.prometheus` |
| `tracing.otlpEndpoint` | `--tracing.otlp.http.endpoint=<endpoint>` |

## Operator flags

| Flag | Default | Description |
|---|---|---|
| `-metrics-bind-address` | `:8080` | Operator metrics endpoint. |
| `-health-probe-bind-address` | `:8081` | Liveness and readiness probes. |
| `-leader-elect` | `false` | Enable leader election. |

## Data plane requirements

Provisioned instances are started with:

```
--providers.kubernetesGateway=true
--providers.kubernetesGateway.gateway=<namespace>/<name>[,<namespace>/<name>...]
--providers.kubernetesGateway.disableGatewayClassStatus=true
--providers.kubernetesGateway.experimentalChannel=true
--providers.kubernetesGateway.statusAddress.service.name=<provisioned service>
--providers.kubernetesGateway.statusAddress.service.namespace=<its namespace>
```

The `gateway` (scoping) and `disableGatewayClassStatus` options are required in
the Traefik image. They ensure a data plane only reconciles the Gateways it was
provisioned for, and that the operator remains the single writer of the
GatewayClass status while the data plane owns Gateway, listener and route
status.

## Data plane RBAC

Each data plane ServiceAccount is bound to the `traefik-gateway-dataplane`
ClusterRole (shipped in `config/rbac/dataplane_role.yaml`) through a
ClusterRoleBinding created and garbage-collected by the operator. The role
grants read access to Gateway API resources, Services, Secrets and
EndpointSlices, plus status updates on Gateways, routes and BackendTLSPolicies.
