#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

COMMIT_SHA="${1:?Usage: build-gateway.sh <commit-sha>}"
IMAGE_TAG="${COMMIT_SHA:0:8}"

GOLANG_VERSION="1.25"
export GOPATH="/go"
export PATH="/go/go${GOLANG_VERSION}/bin:${PATH}"
go version

echo "Running unit tests..."
make test

echo "Running linter..."
make lint

echo "Building binaries..."
make build

echo "Building e2e test binary..."
make build-e2e

echo "Building Ginkgo test binary..."
make build-e2e-test

echo "Generating checksums..."
for ARCH in amd64 arm64; do
  (cd bin/linux/${ARCH} && sha256sum gateway > gateway.sha256)
done

echo "Gathering dependency licenses..."
make gather-licenses

ECR_REGISTRY="${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_DEFAULT_REGION}.amazonaws.com"
IMAGE="${ECR_REGISTRY}/eks-hybrid-nodes-gateway:${IMAGE_TAG}"

BASE_IMAGE_REPO="public.ecr.aws/eks-distro-build-tooling"
BASE_IMAGE_NAME="eks-distro-minimal-base"
BASE_IMAGE_TAG=$(cat EKS_DISTRO_MINIMAL_BASE_TAG_FILE)
BASE_IMAGE="${BASE_IMAGE_REPO}/${BASE_IMAGE_NAME}:${BASE_IMAGE_TAG}"

echo "Logging in to ECR..."
aws ecr get-login-password --region "${AWS_DEFAULT_REGION}" \
  | docker login --username AWS --password-stdin "${ECR_REGISTRY}"

echo "Building and pushing image ${IMAGE}..."
/buildkit.sh build \
  --frontend dockerfile.v0 \
  --opt platform=linux/amd64,linux/arm64 \
  --opt build-arg:BASE_IMAGE="${BASE_IMAGE}" \
  --local context=. \
  --local dockerfile=. \
  --output type=image,name="${IMAGE}",push=true \
  --progress plain

echo "Linting Helm chart..."
make helm-lint

echo "Packaging and pushing Helm chart..."
make helm-push \
  REGISTRY="${ECR_REGISTRY}" \
  CHART_REPO="oci://${ECR_REGISTRY}" \
  CHART_VERSION="0.0.0-${IMAGE_TAG}" \
  APP_VERSION="${IMAGE_TAG}"

mkdir -p _output
echo "${IMAGE_TAG}" > _output/IMAGE_TAG
echo "Build complete: ${IMAGE}"
