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
│  │ hybrid_vxlan0│    │ hybrid_vxlan0│          │    │  │  cilium_vxlan│        │
│  └──────┬───────┘    └──────┬───────┘          │    │  └──────┬───────┘        │
│         │                   │                  │    │         │                │
│         │  VXLAN (VNI 2, UDP 8472)             │    │         │                │
│         └───────────┬──────────────────────────┼────┼─────────┘                │
│                     │                          │    │                          │
│  VPC Route Table:                              │    │                          │
│    hybrid-pod-cidr → leader ENI                │    │                          │
│    (failover → standby ENI)                    │    │                          │
└────────────────────────────────────────────────┘    └──────────────────────────┘
```

**How it works:**

1. Two gateway pods run as a Deployment on labeled nodes with leader election
2. Every gateway pod creates a VXLAN interface at startup so it is always ready to forward traffic
3. The Node reconciler watches CiliumNode objects for hybrid nodes and configures VTEP entries (routes, ARP, FDB) on every gateway pod
4. The leader updates VPC route tables to point hybrid pod CIDRs to its primary ENI
5. The leader upserts the `CiliumVTEPConfig` CRD so hybrid nodes route VPC-bound traffic through the leader
6. If the leader fails, the standby acquires the lease, updates route tables and CiliumVTEPConfig to point to itself

## Prerequisites

- EKS cluster with Cilium CNI and VTEP support enabled
- 2 nodes labeled `hybrid-gateway-node=true` (MNG), or a NodePool that provisions them (Auto)
- IAM permissions for EC2 route table management (if using `ROUTE_TABLE_IDS`)
- IP forwarding enabled on gateway nodes (`/proc/sys/net/ipv4/ip_forward = 1`)

## Build

```bash
# Build for both architectures
make build

# Run unit tests
make test
```

## Deploy

The gateway is deployed via Helm. The workflow is: build and push a container image, then install the chart.

### 1. Build and Push the Image

```bash
make docker-push REGISTRY=<your-ecr-registry>
```

### 2. Install with Helm

Label 2 nodes for MNG deployments, or create a NodePool for Auto Mode (see `helm install` notes for details).

**EKS Auto Mode (default):**

```bash
helm install eks-hybrid-nodes-gateway ./charts/eks-hybrid-nodes-gateway \
  --namespace hybrid-gateway --create-namespace \
  --set image.repository=<your-ecr-registry>/hybrid-gateway \
  --set vpcCIDR=10.0.0.0/16 \
  --set podCIDRs=10.250.0.0/16 \
  --set routeTableIDs=rtb-xxx,rtb-yyy
```

**Managed Node Groups:**

```bash
helm install eks-hybrid-nodes-gateway ./charts/eks-hybrid-nodes-gateway \
  --namespace hybrid-gateway --create-namespace \
  --set autoMode.enabled=false \
  --set image.repository=<your-ecr-registry>/hybrid-gateway \
  --set vpcCIDR=10.0.0.0/16 \
  --set podCIDRs=10.250.0.0/16 \
  --set routeTableIDs=rtb-xxx,rtb-yyy
```

### Package and Push the Chart

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
| `NODE_IP` | `--node-ip` | **required** | Gateway node IP address (auto-set via downward API fieldRef) |
| `VPC_CIDR` | `--vpc-cidr` | **required** | Cluster VPC CIDR |
| `POD_CIDRS` | `--pod-cidrs` | **required** | Comma-separated hybrid pod CIDRs (e.g. `10.250.0.0/16,10.251.0.0/16`) |
| `LEADER_ELECTION_ID` | `--leader-election-id` | `hybrid-gateway-leader` | Leader election lease name |
| `ROUTE_TABLE_IDS` | `--route-table-ids` | | Comma-separated VPC route table IDs to program |
| `AWS_REGION` | `--aws-region` | auto-detected | AWS region (auto-detected from IMDS if not set) |
| `AWS_INSTANCE_ID` | `--aws-instance-id` | auto-detected | EC2 instance ID (auto-detected from IMDS if not set) |
| `DEBUG` | `--debug` | `false` | Enable debug logging |

**Leader election timing (CLI-only flags):**

| Flag | Default | Description |
|------|---------|-------------|
| `--leader-election-lease-duration` | `3s` | Lease duration |
| `--leader-election-renew-deadline` | `2s` | Renew deadline |
| `--leader-election-retry-period` | `1s` | Retry period |

## Leader Election & Failover

Leader election is always enabled. Two gateway pods run on separate nodes via pod anti-affinity. One is elected leader using a Kubernetes Lease; the other runs as standby.

**All pods (leader and standby):**

- VXLAN interface setup at startup (ensures immediate readiness for traffic forwarding)
- Node reconciler watching CiliumNode objects and maintaining VTEP entries (routes, ARP, FDB)
- Health and readiness probes

**Leader-only operations:**

- Updating VPC route tables to point hybrid pod CIDRs to the leader's primary ENI
- Upserting the `CiliumVTEPConfig` CRD with the leader's node IP as the VTEP endpoint

**Failover sequence:**

1. Standby detects leader lease expiration
2. Standby acquires lease and becomes leader
3. New leader updates VPC route tables to point hybrid pod CIDRs to its ENI
4. New leader upserts `CiliumVTEPConfig` CRD with its node IP
5. Cilium agents on hybrid nodes pick up the new VTEP endpoint

Expected failover time: **~15–30 seconds** (tunable via lease duration and renew deadline flags)

## Node Reconciler

The gateway runs a Node reconciler that watches `CiliumNode` objects labeled with `eks.amazonaws.com/compute-type: hybrid`. For each hybrid node it:

1. Extracts the internal IP from `CiliumNode.Spec.Addresses`
2. Extracts the pod CIDR from `CiliumNode.Spec.IPAM.PodCIDRs`
3. Configures VTEP entries: a route through the VXLAN interface, a static ARP entry (deterministic MAC from the node IP), and an FDB entry for unicast forwarding

The reconciler runs on **all** gateway pods (leader election disabled for this controller), so every gateway node maintains a complete set of tunnel entries and is ready to forward traffic immediately on failover.

## Monitoring

The gateway exposes Prometheus metrics on `:10080/metrics`.

**Gateway info:**
- `hybrid_gateway_info` — static gauge with labels: `node_ip`, `node_name`, `vxlan_interface`, `vpc_cidr`, `pod_cidrs`

**Hybrid nodes:**
- `hybrid_gateway_hybrid_nodes_configured` — current count of hybrid nodes with VTEP entries

**VTEP operations:**
- `hybrid_gateway_vtep_{add,remove}_total` — successful add/remove operations
- `hybrid_gateway_vtep_{add,remove}_errors_total` — failed add/remove operations

**Leader & route tables:**
- `hybrid_gateway_leader_is_active` — 1 if this pod is the leader, 0 otherwise
- `hybrid_gateway_leader_setup_duration_seconds` — time to complete leader setup
- `hybrid_gateway_route_table_update_total` / `_errors_total` — route table update counters
- `hybrid_gateway_route_table_update_duration_seconds` — route table update latency histogram

**Network statistics (collected on-demand per scrape):**
- `hybrid_gateway_vxlan_{rx,tx}_{bytes,packets,dropped,errors}_total` — VXLAN interface stats
- `hybrid_gateway_vxlan_up` — VXLAN interface state
- `hybrid_gateway_vxlan_fdb_entries` / `_route_count` — FDB and route counts
- `hybrid_gateway_primary_nic_{rx,tx}_{bytes,packets,dropped,errors}_total` — primary NIC stats

**Health probes (port 8088):**
- Liveness: `/healthz`
- Readiness: `/readyz`

## Project Structure

```
├── cmd/gateway/main.go          Entry point, CLI flags, component wiring
├── internal/
│   ├── gateway/setup.go         Leader lifecycle (route tables, CiliumVTEPConfig)
│   ├── aws/
│   │   ├── metadata.go          EC2 IMDS client (region, instance ID)
│   │   └── routetable.go        VPC route table management via AWS SDK v2
│   ├── cilium/vtep.go           CiliumVTEPConfig CRD upsert
│   ├── controller/node.go       Node reconciler (CiliumNode → VTEP updates)
│   ├── health/server.go         Health and readiness probe handlers
│   ├── metrics/
│   │   ├── metrics.go           Prometheus metric definitions
│   │   └── collector.go         On-demand network stats collector
│   └── vxlan/
│       ├── interface.go         VXLAN interface lifecycle (setup, teardown)
│       └── vtep.go              VTEP operations (routes, ARP, FDB)
├── charts/
│   └── eks-hybrid-nodes-gateway/ Helm chart with RBAC and Deployment
├── hack/build-gateway.sh        CI build script (test → lint → build → Docker → Helm)
├── Makefile                     Build, test, lint, and Helm targets
└── .github/workflows/           CI: build+test, golangci-lint, helm validation, govulncheck
```

## End-to-End Tests

See [test/e2e/README.md](test/e2e/README.md) for details.

```bash
make e2e \
  GATEWAY_IMAGE=<ecr-uri>:tag \
  GATEWAY_CHART=oci://<ecr-uri> \
  GATEWAY_CHART_VERSION=0.0.0-tag
```

## Security

See [CONTRIBUTING](CONTRIBUTING.md#security-issue-notifications) for more information.

## License

This project is licensed under the Apache-2.0 License.
