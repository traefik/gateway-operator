# Getting started

This walkthrough installs the operator on a fresh cluster and provisions a first
Gateway with a route. It assumes a cluster that can allocate `LoadBalancer`
Services (a cloud cluster, or kind with MetalLB or cloud-provider-kind).

## 1. Install the Gateway API CRDs

The operator targets Gateway API v1.5.1. The experimental channel is required
for `TLSRoute` and `TCPRoute` support:

```sh
kubectl apply --server-side \
  -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/experimental-install.yaml
```

Kubernetes >= 1.31 is required: the v1.5 CRDs use CEL validation features that
older API servers reject.

## 2. Install the operator

```sh
kubectl apply -f config/crd      # TraefikProxy CRD
kubectl apply -f config/rbac     # operator ClusterRole + data plane ClusterRole
kubectl apply -f config/manager  # namespace, ServiceAccount, Deployment
```

Check that the operator is running:

```sh
kubectl -n traefik-gateway-operator-system get pods
```

## 3. Create a GatewayClass and its data plane configuration

The `GatewayClass` claims the `traefik.io/gateway-controller` controller name
and points to a `TraefikProxy` resource describing how data planes are deployed:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: traefik
spec:
  controllerName: traefik.io/gateway-controller
  parametersRef:
    group: gateway.traefik.io
    kind: TraefikProxy
    name: default
---
apiVersion: gateway.traefik.io/v1alpha1
kind: TraefikProxy
metadata:
  name: default
spec:
  deployment:
    replicas: 2
  service:
    type: LoadBalancer
```

The operator accepts the class and publishes the supported feature set on its
status:

```sh
kubectl get gatewayclass traefik \
  -o jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'
```

## 4. Create a Gateway

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-gateway
  namespace: default
spec:
  gatewayClassName: traefik
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    allowedRoutes:
      namespaces:
        from: Same
```

The operator provisions a dedicated data plane in the Gateway's namespace:

```sh
kubectl -n default get deploy,svc,sa \
  -l gateway.networking.k8s.io/gateway-name=my-gateway
```

Once the Service has an address and the Deployment is ready, the data plane
reports the Gateway as programmed:

```sh
kubectl -n default get gateway my-gateway
# NAME         CLASS     ADDRESS          PROGRAMMED   AGE
# my-gateway   traefik   192.168.1.240    True         30s
```

## 5. Attach a route

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: whoami
  namespace: default
spec:
  parentRefs:
  - name: my-gateway
  rules:
  - backendRefs:
    - name: whoami
      port: 80
```

```sh
ADDRESS=$(kubectl -n default get gateway my-gateway \
  -o jsonpath='{.status.addresses[0].value}')
curl http://$ADDRESS/
```

## 6. Clean up

Deleting the Gateway garbage-collects its data plane (Deployment, Service,
ServiceAccount, RBAC binding):

```sh
kubectl -n default delete gateway my-gateway
```

## Next steps

- Tune the data plane through the [`TraefikProxy` reference](configuration.md).
- Learn how data planes are provisioned in [Deployment modes](deployment-modes.md).
