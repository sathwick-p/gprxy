# Kubernetes Deployment for gprxy

## Prerequisites

- Kubernetes cluster (EKS recommended)
- kubectl configured
- Docker registry access
- PostgreSQL database (RDS)
- Auth0 tenant configured

## Quick Start

### 1. Build and Push Docker Image

```bash
# Build the image
docker build -t your-registry/gprxy:latest .

# Push to registry
docker push your-registry/gprxy:latest
```

### 2. Configure Secrets

Edit `k8s/secret.yaml` with your actual values:
- RDS endpoint and credentials
- Auth0 tenant information
- Role mappings

```bash
# Apply the secret
kubectl apply -f k8s/secret.yaml
```

### 3. Deploy to Kubernetes

```bash
# Deploy all resources
kubectl apply -f k8s/

# Or use kustomize
kubectl apply -k k8s/

# Check deployment
kubectl get pods -n gprxy
kubectl get svc -n gprxy
```

### 4. Get LoadBalancer Endpoint

```bash
kubectl get svc gprxy -n gprxy -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

Use this endpoint for user connections:
```bash
gprxy login --proxy <loadbalancer-endpoint>
gprxy psql -d mydb
```

## Files Overview

- `namespace.yaml` - Creates gprxy namespace
- `secret.yaml` - Configuration and credentials
- `serviceaccount.yaml` - RBAC for the pods
- `deployment.yaml` - Main gprxy deployment
- `service.yaml` - LoadBalancer service
- `hpa.yaml` - Horizontal Pod Autoscaler
- `kustomization.yaml` - Kustomize configuration

## Security Notes

1. **Secrets Management**: Consider using:
   - AWS Secrets Manager + External Secrets Operator
   - HashiCorp Vault
   - Sealed Secrets

2. **TLS Certificates**: Use cert-manager for automatic TLS:
   ```bash
   kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.0/cert-manager.yaml
   ```

3. **Network Policies**: Add NetworkPolicy to restrict traffic

4. **Pod Security Standards**: Already configured with security contexts

## Monitoring

The deployment includes:
- Liveness/Readiness probes
- Prometheus annotations
- HPA for auto-scaling

## Troubleshooting

```bash
# Check logs
kubectl logs -n gprxy -l app=gprxy -f

# Describe pod
kubectl describe pod -n gprxy <pod-name>

# Test connectivity
kubectl run -it --rm debug --image=postgres:latest --restart=Never -n gprxy -- \
  psql -h gprxy -p 7777 -U test@example.com -d postgres
```

