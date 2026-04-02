# EKS Hybrid Nodes Gateway

VXLAN gateway for EKS Hybrid Nodes that enables pod-to-pod communication between AWS VPC and on-premises nodes using Cilium VTEP.

## Architecture

```
┌─── AWS VPC ───────────────────────────────────┐    ┌─── On-Premises ──────────┐
│                                                │    │                          │
│  ┌──────────────┐    ┌──────────────┐          │    │  ┌──────────────┐        │
│  │   Gateway    │    │   Gateway    │          │    │  │ Hybrid Node  │        │
│  │   (Leader)   │    │  (Standby)   │          │    │  │   (Cilium)   │        │
│  │              │    │              │          │    │  │              │        │
│  │ hybrid_vxlan0│    │   (idle)     │          │    │  │  cilium_vxlan│        │
│  └──────┬───────┘    └──────────────┘          │    │  └──────┬───────┘        │
│         │                                      │    │         │                │
│         │ VXLAN (VNI 2, UDP 8472)              │    │         │                │
│         └──────────────────────────────────────┼────┼─────────┘                │
│                                                │    │                          │
│  VPC Route Table:                              │    │                          │
│    hybrid-pod-cidr → leader ENI                │    │                          │
└────────────────────────────────────────────────┘    └──────────────────────────┘
```

**How it works:**

1. Two gateway pods run as a Deployment on labeled nodes with leader election
2. The leader creates a VXLAN interface and establishes tunnels to each hybrid node
3. VPC route tables are updated to point hybrid pod CIDRs to the leader's ENI
4. Cilium's VTEP configuration is updated so hybrid nodes know where to send VPC-bound traffic
5. If the leader fails, the standby acquires the lease and takes over

## Prerequisites

- EKS cluster with Cilium CNI and VTEP support enabled
- 2 nodes labeled `hybrid-gateway-node=true` (MNG), or a NodePool that provisions them (Auto)
- IAM permissions for EC2 route table management (if using `ROUTE_TABLE_IDS`)

## Quick Start

### Build

```bash
# Build for both architectures
make build

# Build and push multi-arch Docker image
make docker-push REGISTRY=<your-ecr-registry>
```

### Deploy

Choose the variant that matches your node type:

**Managed Node Groups (MNG):**

Label 2 nodes and deploy:

```bash
kubectl label node <node-1> hybrid-gateway-node=true
kubectl label node <node-2> hybrid-gateway-node=true

make deploy-mng \
  IMAGE=<your-ecr-registry>/hybrid-gateway:latest \
  VPC_CIDR=10.0.0.0/16 \
  POD_CIDR=10.250.0.0/16 \
  ROUTE_TABLE_IDS=rtb-xxx,rtb-yyy
```

**EKS Auto Mode:**

Includes a NodePool that automatically provisions gateway nodes:

```bash
make deploy-auto \
  IMAGE=<your-ecr-registry>/hybrid-gateway:latest \
  VPC_CIDR=10.0.0.0/16 \
  POD_CIDR=10.250.0.0/16 \
  ROUTE_TABLE_IDS=rtb-xxx,rtb-yyy
```

### Remove

```bash
make undeploy-mng   # or undeploy-auto
```

## Helm Chart

The recommended way to install the gateway is via the Helm chart.

### Lint and Preview

```bash
# Lint the chart
make helm-lint

# Render templates locally for review
make helm-template
```

### Install

**EKS Auto Mode (default):**

```bash
helm install eks-hybrid-nodes-gateway ./charts/eks-hybrid-nodes-gateway \
  --namespace hybrid-gateway --create-namespace \
  --set image.repository=<your-ecr-registry>/hybrid-gateway \
  --set vpcCIDR=10.0.0.0/16 \
  --set podCIDR=10.250.0.0/16 \
  --set routeTableIDs=rtb-xxx,rtb-yyy
```

**Managed Node Groups:**

```bash
helm install eks-hybrid-nodes-gateway ./charts/eks-hybrid-nodes-gateway \
  --namespace hybrid-gateway --create-namespace \
  --set autoMode.enabled=false \
  --set image.repository=<your-ecr-registry>/hybrid-gateway \
  --set vpcCIDR=10.0.0.0/16 \
  --set podCIDR=10.250.0.0/16 \
  --set routeTableIDs=rtb-xxx,rtb-yyy
```

### Package and Push

```bash
# Package chart to .tgz
make helm-package

# Push to OCI registry
make helm-push REGISTRY=<your-ecr-registry>
```

### Uninstall

```bash
helm uninstall eks-hybrid-nodes-gateway -n hybrid-gateway
```

## Configuration

All configuration is via environment variables or CLI flags:

| Variable | Flag | Default | Description |
|----------|------|---------|-------------|
| `NODE_IP` | `--node-ip` | **required** | Gateway node IP (auto-set via fieldRef) |
| `VPC_CIDR` | `--vpc-cidr` | **required** | VPC CIDR for SNAT destination |
| `POD_CIDR` | `--pod-cidr` | **required** | Hybrid pod CIDR for SNAT source |
| `VXLAN_CIDR` | `--vxlan-cidr` | `192.168.0.0/25` | CIDR for gateway VXLAN IP allocation |
| `ENABLE_SNAT` | `--enable-snat` | `false` | Enable SNAT for hybrid pod traffic |
| `LEADER_ELECTION_NAMESPACE` | `--leader-election-namespace` | `kube-system` | Namespace for leader lease |
| `LEADER_ELECTION_ID` | `--leader-election-id` | `hybrid-gateway-leader` | Leader election lease name |
| `ROUTE_TABLE_IDS` | `--route-table-ids` | | Comma-separated VPC route table IDs |
| `AWS_REGION` | `--aws-region` | auto-detected | AWS region |
| `AWS_INSTANCE_ID` | `--aws-instance-id` | auto-detected | EC2 instance ID |
| `CILIUM_CONFIGMAP_NAME` | `--cilium-configmap-name` | `cilium-config` | Cilium ConfigMap name |
| `CILIUM_CONFIGMAP_NAMESPACE` | `--cilium-configmap-namespace` | `kube-system` | Cilium ConfigMap namespace |
| `DEBUG` | `--debug` | `false` | Enable debug logging |

## Leader Election

Leader election is always enabled. Two gateway pods run on separate nodes via pod anti-affinity. One is elected leader using a Kubernetes Lease; the other idles as standby.

**Leader-only operations:**

- VXLAN interface creation and tunnel management
- SNAT rules for hybrid-to-cloud traffic
- VPC route table updates (point hybrid pod CIDRs to leader's ENI)
- Cilium ConfigMap updates (VTEP endpoint and MAC)

The standby runs the controller-runtime manager but performs none of the above until it acquires the lease.

**Failover:**

1. Standby detects lease expiration
2. Acquires lease and runs full leader setup: VXLAN → SNAT → route tables → Cilium ConfigMap
3. VPC routes now point to the new leader's ENI
4. Cilium agents restart to pick up the new VTEP endpoint

Expected failover time: **~15–30 seconds**

## Project Structure

```
├── cmd/gateway/main.go          Entry point and configuration
├── internal/
│   ├── gateway/setup.go         Leader lifecycle (VXLAN, SNAT, routes, Cilium)
│   ├── aws/
│   │   ├── metadata.go          EC2 IMDS client
│   │   └── routetable.go        VPC route table management
│   ├── cilium/configmap.go      Cilium VTEP ConfigMap updates
│   ├── controller/node.go       Node watcher (CiliumNode + Node objects)
│   ├── health/server.go         Health and readiness probes
│   ├── nat/snat.go              nftables SNAT rules
│   └── vxlan/
│       ├── interface.go         VXLAN interface lifecycle
│       └── vtep.go              Route, ARP, and FDB management
├── deploy/
│   ├── auto.yaml                EKS Auto Mode (includes NodePool)
│   └── mng.yaml                 Managed Node Groups
├── charts/
│   └── eks-hybrid-nodes-gateway/ Helm chart
├── Dockerfile
└── Makefile
```

## Security

See [CONTRIBUTING](CONTRIBUTING.md#security-issue-notifications) for more information.

## License

This project is licensed under the Apache-2.0 License.