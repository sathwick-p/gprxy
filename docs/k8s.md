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




# Network Exposure Options

## Important: PostgreSQL wire protocol considerations

PostgreSQL wire protocol is **TCP-based**, not HTTP. This means:
- Standard HTTP/HTTPS ALB Ingress **will NOT work** for PostgreSQL connections
- You need **TCP load balancing** (NLB) for native PostgreSQL protocol
- ALB only works if you're using HTTP-based connections (not recommended for PostgreSQL)

## Option 1: Network Load Balancer (NLB) - RECOMMENDED

Use this for native PostgreSQL wire protocol connections.

```bash
# Deploy NLB service
kubectl apply -f k8s/service-nlb.yaml

# Get NLB endpoint
kubectl get svc gprxy-nlb -n gprxy
```

Pros:
- Works with native PostgreSQL protocol
- Full TCP support
- Better performance (Layer 4)
- Lower latency
- Works with `gprxy connect` and `psql`

Cons:
- Separate endpoint (not domain-based routing)
- No path-based routing

**User Connection:**
```bash
# Get NLB endpoint
NLB_ENDPOINT=$(kubectl get svc gprxy-nlb -n gprxy -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')

# Connect
gprxy login --proxy $NLB_ENDPOINT
gprxy connect -s $NLB_ENDPOINT -d mydb
```

## Option 2: Application Load Balancer (ALB) Ingress

**Only use if you're implementing HTTP-based proxy (future feature).**

The provided `ingress.yaml` creates an ALB with HTTPS on port 443 and 7777.

```bash
# Deploy ingress
kubectl apply -f k8s/ingress.yaml

# Get ALB endpoint
kubectl get ingress gprxy-ingress -n gprxy
```

**Important Notes:**
- **Will NOT work** with current PostgreSQL wire protocol implementation
- Only works if gprxy implements HTTP/WebSocket tunneling (not current)
- Requires code changes to support HTTP transport

Current status: Not compatible

## Option 3: Both (Hybrid)

Deploy both NLB and ALB for different use cases:

```bash
# NLB for PostgreSQL protocol
kubectl apply -f k8s/service-nlb.yaml

# ALB for future HTTP API/Web UI
kubectl apply -f k8s/ingress.yaml
```

## Recommendation

**Use Option 1 (NLB)** for your current setup:

1. Deploy the NLB service:
```bash
kubectl apply -f k8s/service-nlb.yaml
```

2. Update your DNS (Route53):
```bash
# Get NLB endpoint
kubectl get svc gprxy-nlb -n gprxy

# Create CNAME record:
# gprxy.aidash.io -> <NLB-endpoint>
```

3. Users connect via domain:
```bash
gprxy login --proxy gprxy.aidash.io
gprxy connect -s gprxy.aidash.io -d cloudfront_data
```

## Configuration

### NLB Service (`service-nlb.yaml`)
- Creates internet-facing NLB
- TCP port 7777
- Cross-zone load balancing
- Session affinity (sticky connections)

### ALB Ingress (`ingress.yaml`)
- HTTPS on port 443 and 7777
- Certificate from ACM
- Health checks
- **Update these fields:**
  - `certificate-arn`: Your ACM certificate ARN
  - `host`: Your domain (e.g., `gprxy.aidash.io`)

## Deployment

```bash
# Option 1: NLB only (recommended)
kubectl apply -f k8s/service-nlb.yaml

# Option 2: ALB only (for future HTTP support)
kubectl apply -f k8s/ingress.yaml

# Option 3: Both
kubectl apply -f k8s/service-nlb.yaml
kubectl apply -f k8s/ingress.yaml
```

## DNS Setup

After deploying NLB:

```bash
# Get NLB hostname
NLB_HOST=$(kubectl get svc gprxy-nlb -n gprxy -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')

# Create Route53 CNAME record
aws route53 change-resource-record-sets --hosted-zone-id YOUR-ZONE-ID --change-batch '{
  "Changes": [{
    "Action": "CREATE",
    "ResourceRecordSet": {
      "Name": "gprxy.aidash.io",
      "Type": "CNAME",
      "TTL": 300,
      "ResourceRecords": [{"Value": "'$NLB_HOST'"}]
    }
  }]
}'
```

## Testing

```bash
# Test connection
telnet gprxy.aidash.io 7777

# Or use gprxy
gprxy connect -s gprxy.aidash.io -d mydb
```

## Summary

| Option | Protocol | Works Today | Performance | Cost |
|--------|----------|-------------|-------------|------|
| NLB    | TCP      | Yes         | Excellent   | Low  |
| ALB    | HTTP     | No          | Good        | Low  |

**Choose NLB for production use.**

