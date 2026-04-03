# End-to-End Tests

These tests validate the EKS Hybrid Nodes Gateway in a real AWS environment. They create
all infrastructure from scratch — VPCs connected via Transit Gateway, EKS cluster, hybrid
nodes, managed node groups — upgrade Cilium with VTEP support, install the gateway from
published artifacts (OCI chart + ECR image), run cross-VPC connectivity tests, and tear
everything down.

The test framework is built on top of
[`github.com/aws/eks-hybrid/test/e2e`](https://github.com/aws/eks-hybrid/tree/main/test/e2e),
which handles VPC/cluster/node lifecycle. We add gateway-specific infrastructure setup and
test specs on top.

## Prerequisites

- AWS credentials configured (`aws sts get-caller-identity` should work)
- The gateway container image pushed to ECR
- The gateway Helm chart pushed to an OCI registry (ECR)
- Go 1.25+
- `ginkgo` CLI (`make ginkgo` to install)

## Quick Start

```bash
ACCOUNT=123456789012
REGION=us-west-2
TAG=$(git rev-parse --short HEAD)

# 1. Build and push the gateway image
make docker-push REGISTRY=${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com TAG=${TAG}

# 2. Package and push the Helm chart
make helm-push \
  CHART_REPO=oci://${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com \
  CHART_VERSION=0.0.0-${TAG} \
  APP_VERSION=${TAG}

# 3. Run the e2e tests
make e2e \
  GATEWAY_IMAGE=${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com/eks-hybrid-nodes-gateway:${TAG} \
  GATEWAY_CHART=oci://${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com/eks-hybrid-nodes-gateway \
  GATEWAY_CHART_VERSION=0.0.0-${TAG}
```

The full run takes approximately **30–45 minutes** (cluster creation is the bottleneck).

To keep the cluster alive after tests for debugging:

```bash
make e2e \
  GATEWAY_IMAGE=... GATEWAY_CHART=... GATEWAY_CHART_VERSION=... \
  SKIP_CLEANUP=true
```

## How It Works

### Orchestrator (`test/e2e/cmd/main.go`)

`make e2e` runs a Go orchestrator that:

1. **Sweeps** old `gateway-e2e-*` clusters from previous runs (>6h old)
2. **Builds** the Ginkgo test binary (`go test -c -tags=e2e -o gateway.test`)
3. **Configures** `run.E2E` with nodeadm URLs, cluster name, region, CNI=cilium
4. **Calls** `run.E2E.Run()` which:
   - Creates VPCs + Transit Gateway + IAM via CloudFormation
   - Creates EKS cluster with hybrid nodes enabled
   - Installs Cilium CNI (framework default for K8s 1.31)
   - Writes a config file with cluster details
   - Spawns `ginkgo ./gateway.test -- -filepath=<config>`
   - Cleans up all resources (unless `SKIP_CLEANUP=true`)

### Test Suite Setup (`gateway_suite_test.go`)

The `SynchronizedBeforeSuite` performs these steps in order:

| Step | What | Why |
|------|------|-----|
| 1 | Create 1 hybrid node via nodeadm + SSM | On-prem side for cross-VPC testing |
| 1b | Upgrade Cilium to 1.19.0 with VTEP enabled | Framework's Cilium doesn't have VTEP; we apply a pre-rendered template via direct API (not Helm) |
| 2 | Create 1-node cloud MNG (`t3.medium`) | Cloud-side pods for connectivity tests |
| 3 | Create 2-node gateway MNG (`t3.large`, labeled `hybrid-gateway-node=true`) | Runs the gateway pods with anti-affinity |
| 4 | Disable source/dest check on gateway ENIs | Gateway forwards VXLAN-decapsulated packets with foreign source IPs |
| 4b | Add VXLAN ingress (UDP 8472) on **cluster SG** | Allows hybrid node → gateway VXLAN traffic |
| 4c | Add VXLAN ingress (UDP 8472) on **hybrid node SG** | Allows gateway → hybrid node VXLAN return traffic |
| 5 | Attach IAM policy to MNG role | Gateway needs `ec2:CreateRoute`, `ec2:ReplaceRoute`, `ec2:DescribeRouteTables`, `ec2:DescribeInstances` |
| 6 | Look up route table IDs for cluster subnets | Passed to gateway so it can add routes for hybrid pod CIDRs |
| 7 | Install gateway Helm chart from OCI registry | Uses `action.NewInstall` with `TakeOwnership=true` (handles pre-existing ClusterRole from Cilium CRDs) |
| 8 | Wait for gateway pods to be Running+Ready | Ensures gateway is operational before tests run |

### Test Specs (`gateway_test.go`)

Each test creates pods on specific nodes and validates cross-VPC connectivity.

## Data Path

```
Hybrid Pod (10.87.x.x)
    │
    ▼ Cilium VTEP BPF (matches 10.20.0.0/16 → VXLAN encap)
    │
    ▼ VXLAN outer: hybrid-node:8472 → gateway-node:8472 (via Transit Gateway)
    │
Gateway Node (hybrid_vxlan0)
    │
    ▼ Decapsulate → IP forward via ens5
    │
Cloud Pod (10.20.x.x)
    │
    ▼ Reply: cloud-pod → hybrid-pod (10.87.x.x)
    │
    ▼ VPC route table: 10.87.0.0/16 → gateway ENI
    │
Gateway Node (ens5 → hybrid_vxlan0 → VXLAN encap)
    │
    ▼ VXLAN outer: gateway-node:8472 → hybrid-node:8472 (via Transit Gateway)
    │
Hybrid Pod receives reply
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `GATEWAY_IMAGE` | Yes | — | Full ECR image URI (e.g. `123456.dkr.ecr.us-west-2.amazonaws.com/eks-hybrid-nodes-gateway:abc12345`) |
| `GATEWAY_CHART` | Yes | — | OCI chart URI (e.g. `oci://123456.dkr.ecr.us-west-2.amazonaws.com/eks-hybrid-nodes-gateway`) |
| `GATEWAY_CHART_VERSION` | Yes | — | Chart version to pull (e.g. `0.0.0-abc12345`) |
| `K8S_VERSION` | No | `1.31` | Kubernetes version for the EKS cluster |
| `AWS_REGION` | No | `us-west-2` | AWS region to create resources in |
| `SKIP_CLEANUP` | No | `false` | Set `true` to keep the cluster after tests |
| `E2E_TIMEOUT` | No | `60m` | Total timeout for the test run |

## What Gets Created

| Resource | Details |
|----------|---------|
| VPC (cluster) | `10.20.0.0/16` with public subnets |
| VPC (hybrid) | `10.80.0.0/16` with public subnet |
| Transit Gateway | Connects the two VPCs (not VPC peering) |
| EKS Cluster | Hybrid nodes enabled, Cilium CNI, pod CIDR `10.87.0.0/16` |
| Hybrid Node | 1× Ubuntu 22.04 in hybrid VPC, joined via SSM + nodeadm |
| Cloud MNG | 1× `t3.medium` for cloud-side test pods |
| Gateway MNG | 2× `t3.large` labeled `hybrid-gateway-node=true`, source/dest check disabled |
| Cilium 1.19.0 | Upgraded from framework default with VTEP enabled via pre-rendered template |
| Gateway | Installed via Helm from OCI registry with route table IDs and VPC/pod CIDRs |
| IAM Roles | Cluster role, hybrid node roles (SSM), MNG role + gateway inline policy |
| Jumpbox | EC2 instance in hybrid VPC for SSH-over-SSM to hybrid nodes |
| Security Group Rules | VXLAN UDP 8472 on both cluster SG and hybrid node SG |

All resources are tagged with the cluster name and automatically cleaned up after tests
complete (unless `SKIP_CLEANUP=true`).

## Test Cases

### Active — Pod-to-Pod Connectivity
- **Hybrid → Cloud**: curl from pod on hybrid node to nginx pod on cloud node
- **Cloud → Hybrid**: curl from pod on cloud node to nginx pod on hybrid node

### Pending — Cross-VPC Service Discovery
- **Hybrid → Cloud service**: ClusterIP service on cloud, reached from hybrid pod
- **Cloud → Hybrid service**: ClusterIP service on hybrid, reached from cloud pod

### Pending — Gateway Resilience
- **Leader failover**: delete all gateway pods, verify standby recovers connectivity

## Architecture

```
test/e2e/
├── cmd/
│   └── main.go                 # Orchestrator: sweeps, builds, configures run.E2E
├── testdata/
│   └── cilium-template-1.19.0.yaml  # Pre-rendered Cilium 1.19.0 with VTEP enabled
├── gateway_suite_test.go       # BeforeSuite: infra setup (nodes, MNGs, Cilium, SGs, gateway)
├── gateway_test.go             # Ginkgo specs: connectivity, service discovery, failover
└── README.md
```

## Debugging a Failed Run

```bash
# Keep the cluster alive
make e2e GATEWAY_IMAGE=... GATEWAY_CHART=... GATEWAY_CHART_VERSION=... SKIP_CLEANUP=true

# Get kubeconfig (cluster name is in test output)
aws eks update-kubeconfig --name <cluster-name> --region us-west-2

# Check gateway pods
kubectl get pods -n eks-hybrid-nodes-gateway -o wide
kubectl logs -n eks-hybrid-nodes-gateway -l app.kubernetes.io/name=eks-hybrid-nodes-gateway

# Check all nodes (hybrid + cloud + gateway)
kubectl get nodes -o wide

# Check Cilium VTEP is enabled and synced
kubectl get configmap cilium-config -n kube-system -o yaml | grep enable-vtep
kubectl get ciliumvtepconfig -o yaml
kubectl exec -n kube-system <cilium-pod> -- cilium bpf vtep list

# Check gateway VXLAN interface on the leader gateway node
kubectl debug node/<gateway-node> -it --image=nicolaka/netshoot -- \
  bash -c "chroot /host ip addr show hybrid_vxlan0"

# Check VPC route tables have hybrid pod CIDR route
aws ec2 describe-route-tables --route-table-ids <rtb-id> \
  --query 'RouteTables[].Routes[?DestinationCidrBlock==`10.87.0.0/16`]'

# Check security groups allow VXLAN (UDP 8472)
aws ec2 describe-security-groups --group-ids <sg-id> \
  --query 'SecurityGroups[].IpPermissions[?FromPort==`8472`]'

# Packet capture on gateway node (run tcpdump while testing connectivity)
kubectl debug node/<gateway-node> -it --image=nicolaka/netshoot -- \
  bash -c "chroot /host tcpdump -i hybrid_vxlan0 -nn -c 20"

# Manual connectivity test with debug pods
kubectl run debug-hybrid --image=nicolaka/netshoot --rm -it --restart=Never \
  --overrides='{"spec":{"nodeSelector":{"eks.amazonaws.com/compute-type":"hybrid"}}}' \
  -- ping -c 3 <cloud-pod-ip>
```

## Key Gotchas

- **`test.Cluster.SecurityGroupID`** is the hybrid VPC default SG, NOT the EKS cluster SG.
  Use `DescribeCluster` → `ClusterSecurityGroupId` for the cluster SG.
- **Source/dest check** must be disabled via `ModifyNetworkInterfaceAttribute` on the
  primary ENI (DeviceIndex 0), not `ModifyInstanceAttribute` (fails on multi-ENI instances).
- **Cilium upgrade** uses a pre-rendered template applied via `kubernetes.UpsertManifestsWithRetries`,
  not Helm. Helm's three-way merge with `TakeOwnership` fails to apply ConfigMap values
  (like `enable-vtep`) because the adopted baseline matches live state.
- **Helm kubeconfig**: `cli.New()` uses `~/.kube/config` which may point to a stale cluster.
  Always set `settings.KubeConfig` to the framework's kubeconfig at `/tmp/<cluster>.kubeconfig`.
- **Transit Gateway**, not VPC peering, connects the cluster and hybrid VPCs. The framework
  discovers the TGW by querying TGW attachments on the cluster VPC.
- **VXLAN SG rules** are needed on BOTH the cluster SG (inbound from hybrid) AND the hybrid
  node SG (return path from gateway). Missing the return path rule was a common failure mode.
