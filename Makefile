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
