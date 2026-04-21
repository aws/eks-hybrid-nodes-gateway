REGISTRY   ?=
IMAGE_NAME ?= eks-hybrid-nodes-gateway
TAG        ?= latest
IMAGE      ?= $(REGISTRY)/$(IMAGE_NAME):$(TAG)

BASE_IMAGE_REPO ?= public.ecr.aws/eks-distro-build-tooling
BASE_IMAGE_NAME ?= eks-distro-minimal-base
BASE_IMAGE_TAG  ?= $(shell cat EKS_DISTRO_MINIMAL_BASE_TAG_FILE)
BASE_IMAGE      ?= $(BASE_IMAGE_REPO)/$(BASE_IMAGE_NAME):$(BASE_IMAGE_TAG)
BIN_DIR    := bin
OUTPUT_DIR := _output

CHART_DIR  := charts/eks-hybrid-nodes-gateway
CHART_REPO ?= oci://$(REGISTRY)

GOBIN      := $(shell go env GOPATH)/bin
GINKGO_VERSION := $(word 2,$(shell go list -m github.com/onsi/ginkgo/v2))

.PHONY: build build-amd64 build-arm64 test test-cover lint fmt gather-licenses attribution docker-build docker-push helm-lint helm-template helm-package helm-push ginkgo e2e build-e2e clean help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

build-amd64: ## Build linux/amd64 binary
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BIN_DIR)/linux/amd64/gateway ./cmd/gateway/

build-arm64: ## Build linux/arm64 binary
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o $(BIN_DIR)/linux/arm64/gateway ./cmd/gateway/

build: build-amd64 build-arm64 ## Build for all architectures

test: ## Run unit tests
	go test -count=1 ./...

test-cover: ## Run unit tests with coverage
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

GOLANGCI_LINT_VERSION ?= v2.4.0

lint: ## Run golangci-lint (auto-installs if missing)
	@which golangci-lint > /dev/null 2>&1 || \
		(echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..." && \
		 curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
		   | sh -s -- -b $(shell go env GOPATH)/bin $(GOLANGCI_LINT_VERSION))
	$(shell go env GOPATH)/bin/golangci-lint run --timeout 10m

fmt: lint ## Auto-fix formatting (gofumpt + gci)
	$(shell go env GOPATH)/bin/golangci-lint fmt

gather-licenses: ## Gather dependency licenses and generate attribution (requires builder-base)
	hack/generate-attribution.sh

docker-build: build ## Build multi-arch Docker image (requires REGISTRY)
	@test -n "$(REGISTRY)" || (echo "error: REGISTRY is required"; exit 1)
	@mkdir -p $(OUTPUT_DIR)/LICENSES && touch $(OUTPUT_DIR)/ATTRIBUTION.txt
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg BASE_IMAGE=$(BASE_IMAGE) -t $(IMAGE) .

docker-push: build ## Build and push multi-arch Docker image
	@test -n "$(REGISTRY)" || (echo "error: REGISTRY is required"; exit 1)
	@mkdir -p $(OUTPUT_DIR)/LICENSES && touch $(OUTPUT_DIR)/ATTRIBUTION.txt
	docker buildx build --platform linux/amd64,linux/arm64 --build-arg BASE_IMAGE=$(BASE_IMAGE) -t $(IMAGE) --push .

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) $(OUTPUT_DIR) coverage.out

helm-lint: ## Lint the Helm chart
	helm lint $(CHART_DIR)

helm-template: ## Render Helm templates locally for review
	helm template eks-hybrid-nodes-gateway $(CHART_DIR) \
		--set vpcCIDR=10.0.0.0/16 --set podCIDRs=10.86.0.0/16 --set routeTableIDs=rtb-example

CHART_VERSION ?=
APP_VERSION   ?=

helm-package: ## Package Helm chart (optional CHART_VERSION, APP_VERSION)
	helm package $(CHART_DIR) $(if $(CHART_VERSION),--version $(CHART_VERSION)) $(if $(APP_VERSION),--app-version $(APP_VERSION))

helm-push: helm-package ## Push Helm chart to OCI registry
	@test -n "$(REGISTRY)" || (echo "error: REGISTRY is required"; exit 1)
	helm push eks-hybrid-nodes-gateway-*.tgz $(CHART_REPO)

build-e2e: ## Build e2e test binary
	CGO_ENABLED=0 go build -tags e2e -o $(BIN_DIR)/e2e-test ./test/e2e/cmd/

build-e2e-test: ## Build Ginkgo test binary
	CGO_ENABLED=0 go test -c -tags e2e -o $(BIN_DIR)/gateway.test ./test/e2e/

ginkgo: ## Install ginkgo binary (pinned to module version)
	@test -x $(GOBIN)/ginkgo || \
		(echo "Installing ginkgo $(GINKGO_VERSION)..." && \
		 GOBIN=$(GOBIN) CGO_ENABLED=0 go install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION))

K8S_VERSION    ?= 1.31
AWS_REGION     ?= us-west-2
E2E_TIMEOUT    ?= 60m
SKIP_CLEANUP   ?= false

e2e: ## Run e2e tests (requires GATEWAY_IMAGE, GATEWAY_CHART, GATEWAY_CHART_VERSION)
	@test -n "$(GATEWAY_IMAGE)" || (echo "error: GATEWAY_IMAGE is required (e.g. 123456.dkr.ecr.us-west-2.amazonaws.com/eks-hybrid-nodes-gateway:abc12345)"; exit 1)
	@test -n "$(GATEWAY_CHART)" || (echo "error: GATEWAY_CHART is required (e.g. oci://123456.dkr.ecr.us-west-2.amazonaws.com/eks-hybrid-nodes-gateway)"; exit 1)
	@test -n "$(GATEWAY_CHART_VERSION)" || (echo "error: GATEWAY_CHART_VERSION is required (e.g. 0.0.0-abc12345)"; exit 1)
	@$(MAKE) build-e2e-test
	@$(MAKE) ginkgo
	@$(MAKE) build-e2e
	PATH="$(GOBIN):$(PATH)" \
	GATEWAY_IMAGE=$(GATEWAY_IMAGE) \
	GATEWAY_CHART=$(GATEWAY_CHART) \
	GATEWAY_CHART_VERSION=$(GATEWAY_CHART_VERSION) \
	K8S_VERSION=$(K8S_VERSION) \
	AWS_REGION=$(AWS_REGION) \
	SKIP_CLEANUP=$(SKIP_CLEANUP) \
	E2E_TIMEOUT=$(E2E_TIMEOUT) \
	$(BIN_DIR)/e2e-test
