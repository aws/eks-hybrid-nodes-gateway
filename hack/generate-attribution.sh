#!/bin/bash
set -o errexit
set -o nounset
set -o pipefail

# Generates dependency licenses and attribution files for the container image.
# Must run inside the builder-base image which provides go-licenses and generate-attribution.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
OUTPUT_DIR="${REPO_ROOT}/_output"
ATTRIBUTION_THRESHOLD="${ATTRIBUTION_THRESHOLD:-0.9}"
TAG="${TAG:-latest}"

for cmd in go-licenses generate-attribution jq; do
  if ! command -v "${cmd}" &>/dev/null; then
    echo "error: ${cmd} not found (available in builder-base image)"
    exit 1
  fi
done

MODULE_NAME=$(go list -m)

mkdir -p "${OUTPUT_DIR}/LICENSES" "${OUTPUT_DIR}/attribution"

echo "Saving dependency licenses..."
GOFLAGS=-mod=mod GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
go-licenses save ./cmd/gateway/... \
  --confidence_threshold "${ATTRIBUTION_THRESHOLD}" \
  --force \
  --save_path "${OUTPUT_DIR}/LICENSES"

echo "Generating license CSV..."
GOFLAGS=-mod=mod GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
go-licenses csv ./cmd/gateway/... \
  --confidence_threshold "${ATTRIBUTION_THRESHOLD}" \
  > "${OUTPUT_DIR}/attribution/go-license.csv"

echo "Generating dependency metadata..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
go list -deps=true -json ./... | jq -s '.' > "${OUTPUT_DIR}/attribution/go-deps.json"

echo "${MODULE_NAME}" > "${OUTPUT_DIR}/attribution/root-module.txt"

echo "${TAG}" > "${REPO_ROOT}/GIT_TAG"
trap 'rm -f "${REPO_ROOT}/GIT_TAG"' EXIT

GO_VERSION=$(go version | grep -o 'go[0-9][^ ]*')

echo "Generating ATTRIBUTION.txt..."
generate-attribution "${MODULE_NAME}" "${REPO_ROOT}" "${GO_VERSION}" "${OUTPUT_DIR}"

cp "${OUTPUT_DIR}/attribution/ATTRIBUTION.txt" "${OUTPUT_DIR}/ATTRIBUTION.txt"

echo "Attribution complete: $(wc -l < "${OUTPUT_DIR}/ATTRIBUTION.txt") lines, $(ls "${OUTPUT_DIR}/LICENSES" | wc -l) license dirs"
