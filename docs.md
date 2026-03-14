# Tanugate Documentation

## Deployment Architecture

### Load Balancing

Tanugate **does not implement load balancing**. Load balancing is handled by the container orchestration platform:

- **Docker Swarm**: Uses DNS-based Virtual IP (VIP) load balancing. When tanugate routes to an upstream service name (e.g., `http://users-service:8080`), Swarm's internal DNS resolves it to a VIP that distributes traffic across all healthy replicas using round-robin.

- **Kubernetes**: Uses ClusterIP Services backed by kube-proxy (iptables or IPVS mode). Upstream service names resolve to a stable ClusterIP that load-balances across healthy pods. Readiness probes ensure traffic only reaches pods that are ready to serve.

In both cases, upstream URLs in tanugate's configuration should use **service names**, not individual container or pod IPs:

```yaml
routes:
  - name: users
    path: /api/users/{path:*}
    upstream:
      url: http://users-service:8080
```

Scaling upstream services (e.g., `docker service scale users-service=10` or `kubectl scale deployment users-service --replicas=10`) is transparent to tanugate — no configuration changes required.

### Reverse Proxy and TLS

Tanugate is designed to run **behind a reverse proxy** that handles TLS termination. It does not provide built-in HTTPS support.

Recommended reverse proxy setups:

- **Docker Swarm**: Use [Traefik](https://traefik.io/) or [NGINX](https://nginx.org/) as the ingress, configured to terminate TLS and forward plain HTTP to tanugate.
- **Kubernetes**: Use an [Ingress Controller](https://kubernetes.io/docs/concepts/services-networking/ingress-controllers/) (e.g., NGINX Ingress, Traefik, or cloud provider load balancers) with [cert-manager](https://cert-manager.io/) for automated certificate management.
- **Service Mesh**: In environments using Istio or Linkerd, mTLS between services is handled by sidecar proxies. Tanugate communicates over plain HTTP while the mesh encrypts traffic transparently.

A typical deployment looks like:

```
Client (HTTPS) → Reverse Proxy / Ingress (TLS termination) → Tanugate (HTTP) → Upstream Services (HTTP)
```

Tanugate listens on HTTP (default port `8080`) and should not be exposed directly to the internet without a TLS-terminating proxy in front of it.
