#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

# ---------------------------------------------------------------------------
# prod-release.sh — Promote a staging image + Helm chart to ECR Public.
#
# Prerequisites (handled by buildspecs/prod-release.yml pre_build phase):
#   The working directory has been checked out at the RELEASE_TAG commit.
#
# Expected environment variables (set by CDK / CodePipeline):
#   RELEASE_TAG        – semver tag, e.g. v1.0.0 (pipeline variable)
#   STAGING_ECR_URI    – private staging repo URI
#   PUBLIC_ECR_URI     – public ECR repo URI
#   ECR_REPO           – repo name (eks-hybrid-nodes-gateway)
#   AWS_ACCOUNT_ID     – staging account ID
#   AWS_DEFAULT_REGION – staging region
# ---------------------------------------------------------------------------

# ── 1. Validate inputs ─────────────────────────────────────────────────────

if [[ -z "${RELEASE_TAG:-}" ]]; then
  echo "ERROR: RELEASE_TAG is not set. Pass it as a pipeline variable." >&2
  exit 1
fi

echo "==> Starting release for ${RELEASE_TAG}"

# ── 2. Resolve commit SHA → IMAGE_TAG ─────────────────────────────────────
# The pre_build phase already checked out the repo at the release tag, so
# HEAD is the tagged commit. The first 8 characters match the staging image
# tag convention used by the build pipeline.

COMMIT_SHA=$(git rev-parse HEAD)
IMAGE_TAG="${COMMIT_SHA:0:8}"

echo "    ${RELEASE_TAG} → commit ${COMMIT_SHA} → image tag ${IMAGE_TAG}"

# ── 3. Login to private staging ECR ────────────────────────────────────────

ECR_REGISTRY="${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_DEFAULT_REGION}.amazonaws.com"

echo "==> Logging in to staging ECR (${ECR_REGISTRY})"
aws ecr get-login-password --region "${AWS_DEFAULT_REGION}" \
  | docker login --username AWS --password-stdin "${ECR_REGISTRY}"

# ── 4. Login to ECR Public ─────────────────────────────────────────────────

echo "==> Logging in to ECR Public"
aws ecr-public get-login-password --region us-east-1 \
  | docker login --username AWS --password-stdin public.ecr.aws

# ── 5. Copy multi-arch container image to ECR Public ──────────────────────
# Use skopeo to do a registry-to-registry copy that preserves the full
# multi-arch manifest list (linux/amd64 + linux/arm64).

STAGING_IMAGE="${STAGING_ECR_URI}:${IMAGE_TAG}"
PUBLIC_IMAGE="${PUBLIC_ECR_URI}:${RELEASE_TAG}"

SKOPEO_POLICY="$(mktemp)"
trap 'rm -f "${SKOPEO_POLICY}"' EXIT
cat > "${SKOPEO_POLICY}" <<'EOF'
{ "default": [{ "type": "insecureAcceptAnything" }] }
EOF

echo "==> Copying ${STAGING_IMAGE} → ${PUBLIC_IMAGE} (multi-arch)"
skopeo copy --policy "${SKOPEO_POLICY}" --all \
  "docker://${STAGING_IMAGE}" \
  "docker://${PUBLIC_IMAGE}"

# ── 6. Package and push Helm chart to ECR Public ───────────────────────────
# Strip the leading 'v' for semver compliance (v1.0.0 → 1.0.0).
# helm push expects the OCI registry path without the chart name, e.g.
# oci://public.ecr.aws/i7k6m1j7/eks — so we strip the trailing repo name.

CHART_VERSION="${RELEASE_TAG#v}"
CHART_OCI_REPO="oci://$(dirname "${PUBLIC_ECR_URI}")"

echo "==> Packaging Helm chart (version=${CHART_VERSION}, appVersion=${RELEASE_TAG})"
make helm-push \
  CHART_REPO="${CHART_OCI_REPO}" \
  CHART_VERSION="${CHART_VERSION}" \
  APP_VERSION="${RELEASE_TAG}"

echo "==> Release ${RELEASE_TAG} complete"
echo "    Image: ${PUBLIC_IMAGE}"
echo "    Chart: ${CHART_OCI_REPO}/${ECR_REPO}:${CHART_VERSION}"
