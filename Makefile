REGISTRY   ?=
IMAGE_NAME ?= eks-hybrid-nodes-gateway
TAG        ?= latest
IMAGE      ?= $(REGISTRY)/$(IMAGE_NAME):$(TAG)

BIN_DIR    := bin

CHART_DIR  := charts/eks-hybrid-nodes-gateway
CHART_REPO ?= oci://$(REGISTRY)

.PHONY: build build-amd64 build-arm64 test test-cover lint fmt docker-build docker-push deploy-mng deploy-auto undeploy-mng undeploy-auto helm-lint helm-template helm-package helm-push clean help

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

docker-build: build ## Build multi-arch Docker image (requires REGISTRY)
	@test -n "$(REGISTRY)" || (echo "error: REGISTRY is required"; exit 1)
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMAGE) .

docker-push: build ## Build and push multi-arch Docker image
	@test -n "$(REGISTRY)" || (echo "error: REGISTRY is required"; exit 1)
	docker buildx build --platform linux/amd64,linux/arm64 -t $(IMAGE) --push .

deploy-mng: ## Deploy to MNG nodes (requires IMAGE, VPC_CIDR, POD_CIDR)
	@test -n "$(IMAGE)" || (echo "error: IMAGE is required"; exit 1)
	@test -n "$(VPC_CIDR)" || (echo "error: VPC_CIDR is required"; exit 1)
	@test -n "$(POD_CIDR)" || (echo "error: POD_CIDR is required"; exit 1)
	sed -e 's|__IMAGE__|$(IMAGE)|g' \
	    -e 's|__VPC_CIDR__|$(VPC_CIDR)|g' \
	    -e 's|__POD_CIDR__|$(POD_CIDR)|g' \
	    -e 's|__VXLAN_CIDR__|$(or $(VXLAN_CIDR),192.168.0.0/25)|g' \
	    -e 's|__ROUTE_TABLE_IDS__|$(ROUTE_TABLE_IDS)|g' \
	    deploy/mng.yaml | kubectl apply -f -

deploy-auto: ## Deploy to Auto nodes (requires IMAGE, VPC_CIDR, POD_CIDR)
	@test -n "$(IMAGE)" || (echo "error: IMAGE is required"; exit 1)
	@test -n "$(VPC_CIDR)" || (echo "error: VPC_CIDR is required"; exit 1)
	@test -n "$(POD_CIDR)" || (echo "error: POD_CIDR is required"; exit 1)
	sed -e 's|__IMAGE__|$(IMAGE)|g' \
	    -e 's|__VPC_CIDR__|$(VPC_CIDR)|g' \
	    -e 's|__POD_CIDR__|$(POD_CIDR)|g' \
	    -e 's|__VXLAN_CIDR__|$(or $(VXLAN_CIDR),192.168.0.0/25)|g' \
	    -e 's|__ROUTE_TABLE_IDS__|$(ROUTE_TABLE_IDS)|g' \
	    deploy/auto.yaml | kubectl apply -f -

undeploy-mng: ## Remove MNG deployment
	kubectl delete -f deploy/mng.yaml --ignore-not-found

undeploy-auto: ## Remove Auto deployment
	kubectl delete -f deploy/auto.yaml --ignore-not-found

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

helm-lint: ## Lint the Helm chart
	helm lint $(CHART_DIR)

helm-template: ## Render Helm templates locally for review
	helm template eks-hybrid-nodes-gateway $(CHART_DIR) \
		--set image.repository=example.com/hybrid-gateway \
		--set vpcCIDR=10.0.0.0/16 --set podCIDRs=10.86.0.0/16

CHART_VERSION ?=
APP_VERSION   ?=

helm-package: ## Package Helm chart (optional CHART_VERSION, APP_VERSION)
	helm package $(CHART_DIR) $(if $(CHART_VERSION),--version $(CHART_VERSION)) $(if $(APP_VERSION),--app-version $(APP_VERSION))

helm-push: helm-package ## Push Helm chart to OCI registry
	helm push eks-hybrid-nodes-gateway-*.tgz $(CHART_REPO)
