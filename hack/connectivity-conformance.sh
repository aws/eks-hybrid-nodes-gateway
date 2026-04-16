#!/usr/bin/env bash
# Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0
#
# EKS Hybrid Nodes — Connectivity Conformance Test Suite
#
# Validates east-west network connectivity between cloud nodes (VPC CNI)
# and hybrid nodes (Cilium/Calico) in an EKS cluster. Covers pod-to-pod,
# ClusterIP, NodePort, DNS, API server access, MTU, webhook, and
# LoadBalancer (NLB) paths.
#
# Usage: ./scripts/connectivity-conformance.sh [options]
#        ./scripts/connectivity-conformance.sh --help

set -euo pipefail

# =============================================================================
# Configuration
# =============================================================================

NAMESPACE="${CONFORMANCE_NAMESPACE:-hybrid-conformance}"
TIMEOUT="${CONFORMANCE_TIMEOUT:-120}"
NLB_TIMEOUT="${CONFORMANCE_NLB_TIMEOUT:-300}"
NETSHOOT_IMAGE="${CONFORMANCE_NETSHOOT_IMAGE:-nicolaka/netshoot}"
NGINX_IMAGE="${CONFORMANCE_NGINX_IMAGE:-nginx:stable-alpine}"
WEBHOOK_IMAGE="${CONFORMANCE_WEBHOOK_IMAGE:-python:3-alpine}"

# LoadBalancer test configuration (opt-in via -t loadbalancer)
CLUSTER_NAME="${CONFORMANCE_CLUSTER_NAME:-}"
AWS_REGION="${CONFORMANCE_AWS_REGION:-}"
VPC_ID="${CONFORMANCE_VPC_ID:-}"
AWS_ACCOUNT_ID="${CONFORMANCE_AWS_ACCOUNT_ID:-}"
EKS_ENDPOINT="${CONFORMANCE_EKS_ENDPOINT:-}"

SKIP_CLEANUP=false
NO_COLOR=false
VERBOSE=false
TESTS=""

# Default test categories (loadbalancer is opt-in due to ~3-5 min NLB provisioning)
DEFAULT_TESTS="pod,clusterip,dns,api,mtu,nodeport,webhook"

# =============================================================================
# State (populated during execution)
# =============================================================================

PASS=0 FAIL=0 TOTAL=0
CERT_DIR=""
START_TIME=""
CLOUD_NODE="" CLOUD_NODE_IP=""
HYBRID_NODE="" HYBRID_NODE_IP=""
CLOUD_NGINX_IP="" HYBRID_NGINX_IP=""
CLOUD_NODEPORT="" HYBRID_NODEPORT=""
CA_BUNDLE=""

# LoadBalancer state
LBC_INSTALLED_BY_US=false
NLB_INTERNAL_DNS=""
NLB_EXTERNAL_DNS=""

# =============================================================================
# Usage
# =============================================================================

usage() {
  cat <<EOF
EKS Hybrid Nodes — Connectivity Conformance Test Suite

Validates east-west connectivity between cloud nodes (VPC CNI) and hybrid
nodes (Cilium/Calico) in an EKS cluster with hybrid nodes enabled.

Test Categories:
  pod           Pod-to-pod direct IP connectivity (ICMP + HTTP, bidirectional)
  clusterip     ClusterIP service access via DNS (bidirectional)
  dns           Cross-VPC DNS resolution (bidirectional)
  api           Pod-to-Kubernetes API server access (both node types)
  mtu           Large payload with DF bit set (validates VXLAN MTU overhead)
  nodeport      NodePort service via node IP (bidirectional, cross-VPC)
  webhook       Validating webhook reachability (API server → pod, both sides)
  loadbalancer  AWS NLB → hybrid pods via gateway (opt-in, ~3-5 min extra)

Usage:
  $0 [options]

Options:
  -h, --help            Show this help message
  -n, --namespace NS    Namespace for test resources (default: hybrid-conformance)
  -t, --tests LIST      Comma-separated test categories to run (default: all except loadbalancer)
      --timeout SECS    Pod readiness timeout in seconds (default: 120)
      --skip-cleanup    Preserve test resources after completion for debugging
      --no-color        Disable colored output
  -v, --verbose         Show command output for passing tests

Environment Variables:
  KUBECONFIG                       Path to kubeconfig file
  CONFORMANCE_NAMESPACE            Override default namespace
  CONFORMANCE_TIMEOUT              Override default timeout
  CONFORMANCE_NLB_TIMEOUT          NLB provisioning timeout in seconds (default: 300)
  CONFORMANCE_NETSHOOT_IMAGE       Override netshoot image
  CONFORMANCE_NGINX_IMAGE          Override nginx image
  CONFORMANCE_WEBHOOK_IMAGE        Override webhook server image

  LoadBalancer test (opt-in) — auto-detected when possible:
  CONFORMANCE_CLUSTER_NAME         EKS cluster name
  CONFORMANCE_AWS_REGION           AWS region
  CONFORMANCE_VPC_ID               VPC ID
  CONFORMANCE_AWS_ACCOUNT_ID       AWS account ID
  CONFORMANCE_EKS_ENDPOINT         EKS API endpoint URL (for beta/gamma clusters)

Examples:
  $0                                         # Run all default tests
  $0 -t pod,clusterip,webhook               # Run specific categories
  $0 -t loadbalancer                         # Run NLB tests only (opt-in)
  $0 -t pod,loadbalancer                     # Mix default + opt-in
  $0 --skip-cleanup -v                       # Debug mode
  KUBECONFIG=/tmp/my.kubeconfig $0           # Use specific cluster
EOF
  exit 0
}

# =============================================================================
# Argument Parsing
# =============================================================================

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -h|--help)       usage ;;
      -n|--namespace)  NAMESPACE="$2"; shift 2 ;;
      -t|--tests)      TESTS="$2"; shift 2 ;;
      --timeout)       TIMEOUT="$2"; shift 2 ;;
      --skip-cleanup)  SKIP_CLEANUP=true; shift ;;
      --no-color)      NO_COLOR=true; shift ;;
      -v|--verbose)    VERBOSE=true; shift ;;
      *) echo "Unknown option: $1" >&2; usage ;;
    esac
  done
}

# =============================================================================
# Display Helpers
# =============================================================================

setup_colors() {
  if [[ "${NO_COLOR}" == true ]] || [[ ! -t 1 ]]; then
    RED="" GREEN="" YELLOW="" BOLD="" DIM="" RESET=""
  else
    RED='\033[0;31m' GREEN='\033[0;32m' YELLOW='\033[1;33m'
    BOLD='\033[1m' DIM='\033[2m' RESET='\033[0m'
  fi
}

header() {
  echo ""
  echo -e "${BOLD}EKS Hybrid Nodes — Connectivity Conformance Test Suite${RESET}"
  echo "═══════════════════════════════════════════════════════════════"
  echo ""
}

section() {
  local num="$1" title="$2"
  local pad
  pad=$(printf '%*s' $((55 - ${#title})) '' | tr ' ' '─')
  echo ""
  echo -e "${BOLD}── ${num}. ${title} ${RESET}${pad}"
}

info() {
  echo -e "  ${DIM}$1${RESET}"
}

node_for_side() {
  case "$1" in
    cloud)  echo "$CLOUD_NODE" ;;
    hybrid) echo "$HYBRID_NODE" ;;
  esac
}

# =============================================================================
# Test Runner
# =============================================================================

should_run() {
  if [[ -z "$TESTS" ]]; then
    # Default: run all except opt-in categories (loadbalancer)
    [[ ",${DEFAULT_TESTS}," == *",$1,"* ]]
  else
    [[ ",$TESTS," == *",$1,"* ]]
  fi
}

run_test() {
  local label="$1"; shift
  TOTAL=$((TOTAL + 1))
  printf "  %-60s " "$label"
  local output
  if output=$("$@" 2>&1); then
    echo -e "${GREEN}PASS${RESET}"
    PASS=$((PASS + 1))
    if [[ "$VERBOSE" == true ]]; then
      echo "$output" | head -3 | while IFS= read -r line; do printf "    ${DIM}%s${RESET}\n" "$line"; done
    fi
  else
    echo -e "${RED}FAIL${RESET}"
    FAIL=$((FAIL + 1))
    echo "$output" | tail -5 | while IFS= read -r line; do printf "    ${DIM}%s${RESET}\n" "$line"; done
  fi
}

# =============================================================================
# Prerequisites
# =============================================================================

check_prerequisites() {
  echo "Checking prerequisites..."

  for cmd in kubectl openssl base64; do
    if ! command -v "$cmd" &>/dev/null; then
      echo "  ERROR: '$cmd' is required but not found in PATH" >&2
      exit 1
    fi
  done

  if should_run loadbalancer; then
    for cmd in aws helm eksctl; do
      if ! command -v "$cmd" &>/dev/null; then
        echo "  ERROR: '${cmd}' is required for loadbalancer tests but not found in PATH" >&2
        exit 1
      fi
    done
  fi

  if ! kubectl cluster-info &>/dev/null; then
    echo "  ERROR: Cannot connect to Kubernetes cluster. Check KUBECONFIG." >&2
    exit 1
  fi

  local cloud_count hybrid_count
  cloud_count=$(kubectl get nodes \
    -l 'eks.amazonaws.com/compute-type!=hybrid,!hybrid-gateway-node' \
    --no-headers 2>/dev/null | wc -l | tr -d ' ')
  hybrid_count=$(kubectl get nodes \
    -l 'eks.amazonaws.com/compute-type=hybrid' \
    --no-headers 2>/dev/null | wc -l | tr -d ' ')

  if [[ "$cloud_count" -eq 0 ]]; then
    echo "  ERROR: No cloud nodes found (need at least 1 non-gateway cloud node)" >&2
    exit 1
  fi
  if [[ "$hybrid_count" -eq 0 ]]; then
    echo "  ERROR: No hybrid nodes found (need at least 1 hybrid node)" >&2
    exit 1
  fi

  echo -e "  kubectl: ${GREEN}OK${RESET}  openssl: ${GREEN}OK${RESET}  cluster: ${GREEN}OK${RESET}  cloud nodes: ${cloud_count}  hybrid nodes: ${hybrid_count}"
  if should_run loadbalancer; then
    echo -e "  aws: ${GREEN}OK${RESET}  helm: ${GREEN}OK${RESET}  eksctl: ${GREEN}OK${RESET}  (loadbalancer tests enabled)"
  fi
}

# =============================================================================
# Node Discovery
# =============================================================================

discover_nodes() {
  echo ""
  echo "Discovering nodes..."

  CLOUD_NODE=$(kubectl get nodes \
    -l 'eks.amazonaws.com/compute-type!=hybrid,!hybrid-gateway-node' \
    --field-selector='spec.unschedulable!=true' \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.taints}{"\n"}{end}' \
    | grep -v disk-pressure | head -1 | cut -f1)

  HYBRID_NODE=$(kubectl get nodes \
    -l 'eks.amazonaws.com/compute-type=hybrid' \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.taints}{"\n"}{end}' \
    | grep -v disk-pressure | head -1 | cut -f1)

  CLOUD_NODE_IP=$(kubectl get node "$CLOUD_NODE" \
    -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
  HYBRID_NODE_IP=$(kubectl get node "$HYBRID_NODE" \
    -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')

  echo "  Cloud node:  $CLOUD_NODE ($CLOUD_NODE_IP)"
  echo "  Hybrid node: $HYBRID_NODE ($HYBRID_NODE_IP)"
}

# =============================================================================
# Setup
# =============================================================================

setup_namespace() {
  kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

setup_pods() {
  echo ""
  echo "Setting up test infrastructure in namespace '${NAMESPACE}'..."

  # Nginx servers
  for side in cloud hybrid; do
    local node; node=$(node_for_side "$side")
    kubectl run "${side}-nginx" --namespace "$NAMESPACE" \
      --image="$NGINX_IMAGE" --restart=Never --port=80 \
      --labels="app=${side}-nginx" \
      --overrides="{\"spec\":{\"nodeSelector\":{\"kubernetes.io/hostname\":\"${node}\"},\"tolerations\":[{\"operator\":\"Exists\"}]}}" \
      >/dev/null
  done

  # ClusterIP services
  kubectl expose pod cloud-nginx -n "$NAMESPACE" --name=cloud-nginx-svc --port=80 --target-port=80 >/dev/null
  kubectl expose pod hybrid-nginx -n "$NAMESPACE" --name=hybrid-nginx-svc --port=80 --target-port=80 >/dev/null

  # NodePort services
  kubectl expose pod cloud-nginx -n "$NAMESPACE" --name=cloud-nginx-nodeport \
    --port=80 --target-port=80 --type=NodePort >/dev/null
  kubectl expose pod hybrid-nginx -n "$NAMESPACE" --name=hybrid-nginx-nodeport \
    --port=80 --target-port=80 --type=NodePort >/dev/null

  # Test (netshoot) pods
  for side in cloud hybrid; do
    local node; node=$(node_for_side "$side")
    kubectl run "${side}-test" --namespace "$NAMESPACE" \
      --image="$NETSHOOT_IMAGE" --restart=Never \
      --overrides="{\"spec\":{\"nodeSelector\":{\"kubernetes.io/hostname\":\"${node}\"},\"tolerations\":[{\"operator\":\"Exists\"}]}}" \
      -- sleep infinity >/dev/null
  done

  echo "  Created: nginx pods, test pods, ClusterIP services, NodePort services"
}

setup_webhook() {
  CERT_DIR=$(mktemp -d)

  # Generate CA
  openssl req -x509 -newkey rsa:2048 \
    -keyout "$CERT_DIR/ca.key" -out "$CERT_DIR/ca.crt" \
    -days 1 -nodes -subj "/CN=conformance-webhook-ca" 2>/dev/null

  # Server cert with SANs for both webhook services
  cat > "$CERT_DIR/csr.cnf" <<EOF
[req]
distinguished_name = dn
req_extensions = san
prompt = no
[dn]
CN = webhook.${NAMESPACE}.svc
[san]
subjectAltName = DNS:webhook-cloud-svc.${NAMESPACE}.svc,DNS:webhook-hybrid-svc.${NAMESPACE}.svc,DNS:webhook-cloud-svc.${NAMESPACE}.svc.cluster.local,DNS:webhook-hybrid-svc.${NAMESPACE}.svc.cluster.local
EOF

  cat > "$CERT_DIR/ext.cnf" <<EOF
subjectAltName = DNS:webhook-cloud-svc.${NAMESPACE}.svc,DNS:webhook-hybrid-svc.${NAMESPACE}.svc,DNS:webhook-cloud-svc.${NAMESPACE}.svc.cluster.local,DNS:webhook-hybrid-svc.${NAMESPACE}.svc.cluster.local
EOF

  openssl req -newkey rsa:2048 \
    -keyout "$CERT_DIR/tls.key" -out "$CERT_DIR/tls.csr" \
    -nodes -config "$CERT_DIR/csr.cnf" 2>/dev/null

  openssl x509 -req -in "$CERT_DIR/tls.csr" \
    -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" -CAcreateserial \
    -out "$CERT_DIR/tls.crt" -days 1 \
    -extfile "$CERT_DIR/ext.cnf" 2>/dev/null

  CA_BUNDLE=$(base64 < "$CERT_DIR/ca.crt" | tr -d '\n')

  kubectl create secret tls webhook-certs \
    --cert="$CERT_DIR/tls.crt" --key="$CERT_DIR/tls.key" \
    --namespace "$NAMESPACE" >/dev/null

  # Deploy webhook server pods
  for side in cloud hybrid; do
    local node; node=$(node_for_side "$side")
    cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: webhook-${side}
  namespace: ${NAMESPACE}
  labels:
    app: webhook-${side}
spec:
  nodeSelector:
    kubernetes.io/hostname: "${node}"
  tolerations:
  - operator: Exists
  containers:
  - name: server
    image: ${WEBHOOK_IMAGE}
    command: ["python3", "-c"]
    args:
    - |
      import json, ssl, http.server
      class H(http.server.BaseHTTPRequestHandler):
          def do_POST(self):
              body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
              resp = json.dumps({
                  "apiVersion": "admission.k8s.io/v1",
                  "kind": "AdmissionReview",
                  "response": {"uid": body["request"]["uid"], "allowed": True}
              })
              self.send_response(200)
              self.send_header("Content-Type", "application/json")
              self.end_headers()
              self.wfile.write(resp.encode())
          def log_message(self, *a): pass
      ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
      ctx.load_cert_chain("/certs/tls.crt", "/certs/tls.key")
      s = http.server.HTTPServer(("0.0.0.0", 8443), H)
      s.socket = ctx.wrap_socket(s.socket, server_side=True)
      s.serve_forever()
    ports:
    - containerPort: 8443
    volumeMounts:
    - name: certs
      mountPath: /certs
      readOnly: true
  volumes:
  - name: certs
    secret:
      secretName: webhook-certs
YAML
    kubectl expose pod "webhook-${side}" -n "$NAMESPACE" \
      --name="webhook-${side}-svc" --port=443 --target-port=8443 >/dev/null
  done

  echo "  Created: webhook certs, webhook pods, webhook services"
}

register_webhooks() {
  for side in cloud hybrid; do
    cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: conformance-webhook-${side}
  labels:
    app.kubernetes.io/part-of: hybrid-conformance
webhooks:
- name: conformance.${side}.webhook.test
  failurePolicy: Fail
  sideEffects: None
  admissionReviewVersions: ["v1"]
  timeoutSeconds: 10
  objectSelector:
    matchLabels:
      conformance-webhook-test: "${side}"
  rules:
  - operations: ["CREATE"]
    apiGroups: [""]
    apiVersions: ["v1"]
    resources: ["configmaps"]
  clientConfig:
    service:
      name: webhook-${side}-svc
      namespace: ${NAMESPACE}
      path: /validate
      port: 443
    caBundle: ${CA_BUNDLE}
EOF
  done
  sleep 2
}

# =============================================================================
# LoadBalancer Setup (opt-in)
# =============================================================================

_aws_eks() {
  if [[ -n "$EKS_ENDPOINT" ]]; then
    aws eks --endpoint-url "$EKS_ENDPOINT" "$@"
  else
    aws eks "$@"
  fi
}

detect_cluster_info() {
  echo "  Detecting cluster information..."

  # Cluster name — extract from EKS kubeconfig context (arn:aws:eks:REGION:ACCOUNT:cluster/NAME)
  if [[ -z "$CLUSTER_NAME" ]]; then
    local context
    context=$(kubectl config current-context 2>/dev/null || true)
    CLUSTER_NAME=$(echo "$context" | sed -n 's|.*/||p')
  fi

  # AWS region — from context ARN field or aws config
  if [[ -z "$AWS_REGION" ]]; then
    local context
    context=$(kubectl config current-context 2>/dev/null || true)
    AWS_REGION=$(echo "$context" | cut -d: -f4 2>/dev/null || true)
    if [[ -z "$AWS_REGION" ]]; then
      AWS_REGION=$(aws configure get region 2>/dev/null || true)
    fi
  fi

  # Account ID
  if [[ -z "$AWS_ACCOUNT_ID" ]]; then
    AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query 'Account' --output text 2>/dev/null || true)
  fi

  # VPC ID — from EKS cluster description
  if [[ -z "$VPC_ID" ]]; then
    VPC_ID=$(_aws_eks describe-cluster --name "$CLUSTER_NAME" --region "$AWS_REGION" \
      --query 'cluster.resourcesVpcConfig.vpcId' --output text 2>/dev/null || true)
  fi

  # Validate all required info was found
  local missing=""
  [[ -z "$CLUSTER_NAME" ]] && missing="${missing} CONFORMANCE_CLUSTER_NAME"
  [[ -z "$AWS_REGION" ]] && missing="${missing} CONFORMANCE_AWS_REGION"
  [[ -z "$AWS_ACCOUNT_ID" ]] && missing="${missing} CONFORMANCE_AWS_ACCOUNT_ID"
  [[ -z "$VPC_ID" ]] && missing="${missing} CONFORMANCE_VPC_ID"

  if [[ -n "$missing" ]]; then
    echo "  ERROR: Could not auto-detect:${missing}" >&2
    echo "  Set the corresponding environment variables and retry." >&2
    exit 1
  fi

  echo "  Cluster: ${CLUSTER_NAME}  Region: ${AWS_REGION}  VPC: ${VPC_ID}"
}

setup_loadbalancer() {
  echo ""
  echo "Setting up AWS Load Balancer Controller..."

  # Check if already installed
  if kubectl get deployment aws-load-balancer-controller -n kube-system &>/dev/null; then
    echo "  AWS Load Balancer Controller already installed — skipping setup"
  else
    detect_cluster_info

    # IAM policy
    local policy_name="AWSLoadBalancerControllerIAMPolicy"
    local policy_arn
    policy_arn=$(aws iam list-policies \
      --query "Policies[?PolicyName=='${policy_name}'].Arn" \
      --output text 2>/dev/null || true)

    if [[ -z "$policy_arn" ]]; then
      echo "  Creating IAM policy..."
      local policy_file
      policy_file=$(mktemp)
      if ! curl -sfL -o "$policy_file" \
        "https://raw.githubusercontent.com/kubernetes-sigs/aws-load-balancer-controller/refs/heads/main/docs/install/iam_policy.json"; then
        echo "  ERROR: Failed to download IAM policy document" >&2
        exit 1
      fi
      policy_arn=$(aws iam create-policy \
        --policy-name "$policy_name" \
        --policy-document "file://${policy_file}" \
        --query 'Policy.Arn' --output text 2>&1) || {
        echo "  ERROR: Failed to create IAM policy. Check IAM permissions." >&2
        echo "  $policy_arn" >&2
        rm -f "$policy_file"
        exit 1
      }
      rm -f "$policy_file"
    fi
    echo "  IAM policy: ${policy_arn}"

    # OIDC provider + IRSA service account
    echo "  Associating OIDC provider..."
    eksctl utils associate-iam-oidc-provider \
      --cluster "$CLUSTER_NAME" --region "$AWS_REGION" --approve 2>/dev/null || {
      echo "  ERROR: Failed to associate OIDC provider. Check IAM permissions." >&2
      exit 1
    }

    echo "  Creating IRSA service account..."
    eksctl create iamserviceaccount \
      --cluster="$CLUSTER_NAME" \
      --namespace=kube-system \
      --name=aws-load-balancer-controller \
      --attach-policy-arn="$policy_arn" \
      --override-existing-serviceaccounts \
      --region "$AWS_REGION" \
      --approve 2>/dev/null || {
      echo "  ERROR: Failed to create IRSA service account. Check IAM permissions." >&2
      exit 1
    }

    # Helm install
    echo "  Installing via Helm..."
    helm repo add eks https://aws.github.io/eks-charts 2>/dev/null || true
    helm repo update eks 2>/dev/null

    helm install aws-load-balancer-controller eks/aws-load-balancer-controller \
      -n kube-system \
      --set clusterName="$CLUSTER_NAME" \
      --set region="$AWS_REGION" \
      --set vpcId="$VPC_ID" \
      --set serviceAccount.create=false \
      --set serviceAccount.name=aws-load-balancer-controller \
      --wait --timeout 120s 2>&1 || {
      echo "  ERROR: Failed to install AWS Load Balancer Controller via Helm" >&2
      exit 1
    }

    LBC_INSTALLED_BY_US=true
    echo "  AWS Load Balancer Controller installed"
  fi

  # Create NLB services (start provisioning early — NLBs take 2-3 min)
  echo "  Creating NLB services targeting hybrid-nginx pods..."
  for scheme in internal internet-facing; do
    local svc_name="hybrid-nlb-${scheme}"
    cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Service
metadata:
  name: ${svc_name}
  namespace: ${NAMESPACE}
  annotations:
    service.beta.kubernetes.io/aws-load-balancer-type: external
    service.beta.kubernetes.io/aws-load-balancer-nlb-target-type: ip
    service.beta.kubernetes.io/aws-load-balancer-scheme: ${scheme}
spec:
  ports:
  - port: 80
    targetPort: 80
    protocol: TCP
  type: LoadBalancer
  selector:
    app: hybrid-nginx
EOF
  done
  echo "  Created: NLB services (provisioning in background)"
}

# =============================================================================
# Wait for Resources
# =============================================================================

wait_for_pods() {
  echo ""
  echo "Waiting for pods to be ready (timeout: ${TIMEOUT}s)..."

  local pods="pod/cloud-test pod/hybrid-test pod/cloud-nginx pod/hybrid-nginx"
  if should_run webhook; then
    pods="$pods pod/webhook-cloud pod/webhook-hybrid"
  fi

  kubectl wait --for=condition=Ready $pods \
    --namespace "$NAMESPACE" --timeout="${TIMEOUT}s" >/dev/null

  CLOUD_NGINX_IP=$(kubectl get pod cloud-nginx -n "$NAMESPACE" -o jsonpath='{.status.podIP}')
  HYBRID_NGINX_IP=$(kubectl get pod hybrid-nginx -n "$NAMESPACE" -o jsonpath='{.status.podIP}')
  CLOUD_NODEPORT=$(kubectl get svc cloud-nginx-nodeport -n "$NAMESPACE" -o jsonpath='{.spec.ports[0].nodePort}')
  HYBRID_NODEPORT=$(kubectl get svc hybrid-nginx-nodeport -n "$NAMESPACE" -o jsonpath='{.spec.ports[0].nodePort}')

  echo ""
  echo "Environment:"
  echo "  Cloud node:    ${CLOUD_NODE} (${CLOUD_NODE_IP})"
  echo "  Hybrid node:   ${HYBRID_NODE} (${HYBRID_NODE_IP})"
  echo "  Cloud nginx:   ${CLOUD_NGINX_IP} (NodePort ${CLOUD_NODEPORT})"
  echo "  Hybrid nginx:  ${HYBRID_NGINX_IP} (NodePort ${HYBRID_NODEPORT})"
}

wait_for_nlbs() {
  echo ""
  echo "Waiting for NLBs to provision (timeout: ${NLB_TIMEOUT}s)..."

  local deadline=$((SECONDS + NLB_TIMEOUT))

  # Wait for external addresses to be assigned
  while [[ $SECONDS -lt $deadline ]]; do
    NLB_INTERNAL_DNS=$(kubectl get svc hybrid-nlb-internal -n "$NAMESPACE" \
      -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || true)
    NLB_EXTERNAL_DNS=$(kubectl get svc hybrid-nlb-internet-facing -n "$NAMESPACE" \
      -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || true)

    if [[ -n "$NLB_INTERNAL_DNS" ]] && [[ -n "$NLB_EXTERNAL_DNS" ]]; then
      break
    fi
    sleep 10
  done

  if [[ -z "$NLB_INTERNAL_DNS" ]] || [[ -z "$NLB_EXTERNAL_DNS" ]]; then
    echo -e "  ${YELLOW}WARNING: NLBs did not provision within ${NLB_TIMEOUT}s${RESET}"
    echo "  Check Service events: kubectl describe svc -n ${NAMESPACE} hybrid-nlb-internal"
    return
  fi

  echo "  Internal NLB: ${NLB_INTERNAL_DNS}"
  echo "  External NLB: ${NLB_EXTERNAL_DNS}"

  # Wait for NLB targets to become healthy (NLB routes to hybrid pod IPs via gateway)
  echo "  Waiting for NLB targets to become healthy..."
  while [[ $SECONDS -lt $deadline ]]; do
    if curl -sf --max-time 5 -o /dev/null "http://${NLB_EXTERNAL_DNS}" 2>/dev/null; then
      echo "  NLBs ready"
      return
    fi
    sleep 15
  done

  echo -e "  ${YELLOW}WARNING: NLB targets may not be healthy yet — tests will attempt anyway${RESET}"
}

# =============================================================================
# Teardown
# =============================================================================

teardown() {
  echo ""
  if [[ "${SKIP_CLEANUP}" == true ]]; then
    echo -e "${YELLOW}Skipping cleanup (--skip-cleanup). Resources in namespace: ${NAMESPACE}${RESET}"
    echo "  To clean up manually:"
    echo "    kubectl delete validatingwebhookconfiguration -l app.kubernetes.io/part-of=hybrid-conformance"
    echo "    kubectl delete namespace ${NAMESPACE}"
    if [[ "$LBC_INSTALLED_BY_US" == true ]]; then
      echo "    helm uninstall aws-load-balancer-controller -n kube-system"
    fi
    return
  fi

  echo "Cleaning up..."

  # Delete NLB services first (triggers NLB deprovisioning via LBC)
  if should_run loadbalancer; then
    kubectl delete svc hybrid-nlb-internal hybrid-nlb-internet-facing \
      -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
    echo "  NLB services deleted (NLBs will deprovision in background)"
  fi

  kubectl delete validatingwebhookconfiguration \
    -l app.kubernetes.io/part-of=hybrid-conformance \
    --ignore-not-found 2>/dev/null || true
  kubectl delete namespace "$NAMESPACE" \
    --ignore-not-found --wait=false 2>/dev/null || true
  [[ -n "$CERT_DIR" ]] && rm -rf "$CERT_DIR"

  if [[ "$LBC_INSTALLED_BY_US" == true ]]; then
    echo -e "  ${DIM}Note: AWS LB Controller was installed by this script but NOT removed.${RESET}"
    echo -e "  ${DIM}To uninstall: helm uninstall aws-load-balancer-controller -n kube-system${RESET}"
  fi
}

# =============================================================================
# Test Categories
# =============================================================================

test_pod_connectivity() {
  section 1 "Pod-to-Pod Connectivity"
  info "Direct IP: ICMP ping + HTTP GET, bidirectional"

  run_test "[PING] Hybrid → Cloud (${CLOUD_NGINX_IP})" \
    kubectl exec hybrid-test -n "$NAMESPACE" -- ping -c 3 -W 5 "$CLOUD_NGINX_IP"
  run_test "[PING] Cloud → Hybrid (${HYBRID_NGINX_IP})" \
    kubectl exec cloud-test -n "$NAMESPACE" -- ping -c 3 -W 5 "$HYBRID_NGINX_IP"
  run_test "[HTTP] Hybrid → Cloud nginx (${CLOUD_NGINX_IP})" \
    kubectl exec hybrid-test -n "$NAMESPACE" -- curl -sf --max-time 10 "http://${CLOUD_NGINX_IP}"
  run_test "[HTTP] Cloud → Hybrid nginx (${HYBRID_NGINX_IP})" \
    kubectl exec cloud-test -n "$NAMESPACE" -- curl -sf --max-time 10 "http://${HYBRID_NGINX_IP}"
}

test_clusterip_service() {
  section 2 "ClusterIP Service"
  info "Service discovery via CoreDNS + kube-proxy DNAT, bidirectional"

  run_test "[SVC] Hybrid → cloud-nginx-svc" \
    kubectl exec hybrid-test -n "$NAMESPACE" -- \
      curl -sf --max-time 10 "http://cloud-nginx-svc.${NAMESPACE}.svc.cluster.local"
  run_test "[SVC] Cloud → hybrid-nginx-svc" \
    kubectl exec cloud-test -n "$NAMESPACE" -- \
      curl -sf --max-time 10 "http://hybrid-nginx-svc.${NAMESPACE}.svc.cluster.local"
}

test_dns_resolution() {
  section 3 "DNS Resolution"
  info "Cross-VPC DNS lookups via CoreDNS"

  run_test "[DNS] Cloud resolves hybrid-nginx-svc" \
    kubectl exec cloud-test -n "$NAMESPACE" -- \
      nslookup "hybrid-nginx-svc.${NAMESPACE}.svc.cluster.local"
  run_test "[DNS] Hybrid resolves cloud-nginx-svc" \
    kubectl exec hybrid-test -n "$NAMESPACE" -- \
      nslookup "cloud-nginx-svc.${NAMESPACE}.svc.cluster.local"
}

test_api_server() {
  section 4 "API Server Access"
  info "Pod → kubernetes.default.svc (control plane reachability)"

  run_test "[API] Cloud pod → API server" \
    kubectl exec cloud-test -n "$NAMESPACE" -- \
      curl -sk --max-time 10 -o /dev/null "https://kubernetes.default.svc/healthz"
  run_test "[API] Hybrid pod → API server" \
    kubectl exec hybrid-test -n "$NAMESPACE" -- \
      curl -sk --max-time 10 -o /dev/null "https://kubernetes.default.svc/healthz"
}

test_mtu() {
  section 5 "MTU / Large Payload"
  info "1400-byte ICMP payload with DF bit (validates VXLAN encap overhead)"

  run_test "[MTU] Cloud → Hybrid (1400B, DF)" \
    kubectl exec cloud-test -n "$NAMESPACE" -- \
      ping -c 3 -W 5 -s 1400 -M do "$HYBRID_NGINX_IP"
  run_test "[MTU] Hybrid → Cloud (1400B, DF)" \
    kubectl exec hybrid-test -n "$NAMESPACE" -- \
      ping -c 3 -W 5 -s 1400 -M do "$CLOUD_NGINX_IP"
}

test_nodeport_service() {
  section 6 "NodePort Service"
  info "Cross-VPC access via node IP + NodePort"

  run_test "[NODEPORT] Cloud → Hybrid (${HYBRID_NODE_IP}:${HYBRID_NODEPORT})" \
    kubectl exec cloud-test -n "$NAMESPACE" -- \
      curl -sf --max-time 10 "http://${HYBRID_NODE_IP}:${HYBRID_NODEPORT}"
  run_test "[NODEPORT] Hybrid → Cloud (${CLOUD_NODE_IP}:${CLOUD_NODEPORT})" \
    kubectl exec hybrid-test -n "$NAMESPACE" -- \
      curl -sf --max-time 10 "http://${CLOUD_NODE_IP}:${CLOUD_NODEPORT}"
}

test_webhook() {
  section 7 "Webhook (API Server → Pod)"
  info "ValidatingWebhookConfiguration: API server calls webhook pods cross-VPC"

  run_test "[WEBHOOK] API server → Cloud webhook pod" \
    _create_webhook_trigger cloud
  run_test "[WEBHOOK] API server → Hybrid webhook pod" \
    _create_webhook_trigger hybrid
}

_create_webhook_trigger() {
  local side="$1"
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: webhook-trigger-${side}
  namespace: ${NAMESPACE}
  labels:
    conformance-webhook-test: "${side}"
EOF
}

test_loadbalancer() {
  section 8 "LoadBalancer Service (NLB → Hybrid Pods)"
  info "AWS NLB with IP targets forwarding to hybrid pods via gateway"

  if [[ -z "$NLB_INTERNAL_DNS" ]] || [[ -z "$NLB_EXTERNAL_DNS" ]]; then
    info "NLBs did not provision — marking tests as FAIL"
    run_test "[NLB-INTERNAL] Cloud pod → Hybrid via internal NLB" false
    run_test "[NLB-EXTERNAL] Test runner → Hybrid via internet-facing NLB" false
    return
  fi

  info "Internal:  ${NLB_INTERNAL_DNS}"
  info "External:  ${NLB_EXTERNAL_DNS}"

  run_test "[NLB-INTERNAL] Cloud pod → Hybrid via internal NLB" \
    kubectl exec cloud-test -n "$NAMESPACE" -- \
      curl -sf --max-time 30 "http://${NLB_INTERNAL_DNS}"

  run_test "[NLB-EXTERNAL] Test runner → Hybrid via internet-facing NLB" \
    curl -sf --max-time 30 "http://${NLB_EXTERNAL_DNS}"
}

# =============================================================================
# Summary
# =============================================================================

print_summary() {
  local elapsed=$((SECONDS - START_TIME))
  echo ""
  echo "═══════════════════════════════════════════════════════════════"

  if [[ "$FAIL" -eq 0 ]]; then
    echo -e "${GREEN}${BOLD}CONFORMANT${RESET}  ${PASS}/${TOTAL} passed in ${elapsed}s"
  else
    echo -e "${RED}${BOLD}NON-CONFORMANT${RESET}  ${PASS}/${TOTAL} passed, ${FAIL} failed in ${elapsed}s"
  fi

  echo "═══════════════════════════════════════════════════════════════"
}

# =============================================================================
# Main
# =============================================================================

main() {
  parse_args "$@"
  setup_colors
  header

  START_TIME=$SECONDS

  check_prerequisites
  discover_nodes

  trap teardown EXIT

  setup_namespace
  setup_pods
  should_run webhook && setup_webhook
  should_run loadbalancer && setup_loadbalancer
  wait_for_pods
  should_run webhook && register_webhooks

  # Run default test categories
  should_run pod       && test_pod_connectivity
  should_run clusterip && test_clusterip_service
  should_run dns       && test_dns_resolution
  should_run api       && test_api_server
  should_run mtu       && test_mtu
  should_run nodeport  && test_nodeport_service
  should_run webhook   && test_webhook

  # Opt-in test categories (run after defaults to maximize NLB provisioning time)
  if should_run loadbalancer; then
    wait_for_nlbs
    test_loadbalancer
  fi

  print_summary

  [[ "$FAIL" -gt 0 ]] && exit 1
  exit 0
}

main "$@"
