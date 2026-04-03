#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

# Inputs from CodePipeline environment variables:
#   ECR_REPO           - Repository name (e.g. eks-hybrid-nodes-gateway)
#   AWS_ACCOUNT_ID     - AWS account ID
#   AWS_DEFAULT_REGION - AWS region

ECR_REPO="${ECR_REPO:?ECR_REPO is required}"
AWS_ACCOUNT_ID="${AWS_ACCOUNT_ID:?AWS_ACCOUNT_ID is required}"
AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:?AWS_DEFAULT_REGION is required}"

ECR_REGISTRY="${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_DEFAULT_REGION}.amazonaws.com"
IMAGE_TAG=$(cat _output/IMAGE_TAG | tr -d '[:space:]')

GATEWAY_IMAGE="${ECR_REGISTRY}/${ECR_REPO}:${IMAGE_TAG}"
GATEWAY_CHART="oci://${ECR_REGISTRY}/${ECR_REPO}"
GATEWAY_CHART_VERSION="0.0.0-${IMAGE_TAG}"

GOLANG_VERSION="1.25"
export GOPATH="/go"
export GOBIN="${GOPATH}/bin"
export PATH="/go/go${GOLANG_VERSION}/bin:${GOBIN}:${PATH}"

if command -v ginkgo &>/dev/null; then
  echo "ginkgo already installed: $(ginkgo version)"
else
  echo "Installing ginkgo..."
  GINKGO_VERSION=$(go version -m ./bin/e2e-test 2>/dev/null | awk '/github.com\/onsi\/ginkgo\/v2/{print $3}' || true)
  if [[ -n "${GINKGO_VERSION}" ]]; then
    CGO_ENABLED=0 go install "github.com/onsi/ginkgo/v2/ginkgo@${GINKGO_VERSION}"
  else
    CGO_ENABLED=0 go install github.com/onsi/ginkgo/v2/ginkgo@latest
  fi
fi

echo "Logging in to ECR..."
aws ecr get-login-password --region "${AWS_DEFAULT_REGION}" \
  | docker login --username AWS --password-stdin "${ECR_REGISTRY}"

echo "Running e2e tests..."
echo "  IMAGE_TAG:             ${IMAGE_TAG}"
echo "  GATEWAY_IMAGE:         ${GATEWAY_IMAGE}"
echo "  GATEWAY_CHART:         ${GATEWAY_CHART}"
echo "  GATEWAY_CHART_VERSION: ${GATEWAY_CHART_VERSION}"

GATEWAY_IMAGE="${GATEWAY_IMAGE}" \
GATEWAY_CHART="${GATEWAY_CHART}" \
GATEWAY_CHART_VERSION="${GATEWAY_CHART_VERSION}" \
AWS_REGION="${AWS_DEFAULT_REGION}" \
  ./bin/e2e-test
