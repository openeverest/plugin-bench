# k3d cluster name (must match dev/k3d_config.yaml metadata.name).
K3D_CLUSTER_NAME ?= plugin-bench-test

.PHONY: help frontend backend helm-lint

help: ## Display available commands.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make <target>\n\n"} /^[a-zA-Z_-]+:.*##/ { printf "  %-16s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

frontend: ## Type-check and build the frontend bundle.
	npm run build

backend: ## Run backend formatting, vetting, tests, and build.
	gofmt -w backend/*.go
	cd backend && go vet ./... && go test ./... && go build -o /dev/null .

helm-lint: ## Lint the Helm chart.
	helm lint charts/plugin-bench/

##@ Cluster

.PHONY: k3d-cluster-up
k3d-cluster-up: ## Create a local k3d cluster for development.
	$(info Creating k3d cluster for testing)
	k3d cluster create --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-down
k3d-cluster-down: ## Delete the local k3d cluster.
	$(info Destroying k3d test cluster)
	k3d cluster delete --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-reset
k3d-cluster-reset: k3d-cluster-down k3d-cluster-up ## Reset the local k3d cluster.

##@ Tilt Development

.PHONY: dev-up
dev-up: k3d-cluster-up ## Create the k3d cluster and start the Tilt dev environment.
	tilt up -f dev/Tiltfile

.PHONY: dev-down
dev-down: ## Stop the Tilt dev environment (keeps the cluster).
	tilt down -f dev/Tiltfile

.PHONY: dev-destroy
dev-destroy: k3d-cluster-down ## Stop Tilt and delete the k3d cluster.
