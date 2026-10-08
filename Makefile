# Kalapa platform — Kubernetes / GitOps lab
#
# Normal path:   make up   ->  make hosts  ->  make smoke
# When stuck:    make status   then   make logs SVC=kyc

SHELL := /bin/bash
CLUSTER ?= kalapa
SVC     ?= gateway
NS      ?= kalapa

export CLUSTER_NAME := $(CLUSTER)

.DEFAULT_GOAL := help

## ---------------------------------------------------------------- cluster ---

.PHONY: cluster
cluster: ## Start the minikube cluster and its addons
	@./scripts/00-start-cluster.sh

.PHONY: hosts
hosts: ## Point *.kalapa.local at the minikube node (needs sudo)
	@./scripts/hosts.sh

.PHONY: init
init: ## Create the GitHub repo and write its URL into the Argo CD manifests
	@./scripts/init-repo.sh

.PHONY: bootstrap
bootstrap: ## Install Argo CD and apply the root Application
	@./scripts/01-bootstrap-argocd.sh

.PHONY: up
up: cluster bootstrap ## cluster + bootstrap in one go
	@echo
	@echo "Argo CD is syncing. Next:  make hosts && make watch"

.PHONY: down
down: ## Delete the cluster and all its data
	@./scripts/teardown.sh

## ----------------------------------------------------------------- verify ---

.PHONY: smoke
smoke: ## End-to-end test through the Ingress, including telemetry
	@./scripts/smoke-test.sh

.PHONY: status
status: ## One screen: Argo apps, broken pods, warnings, memory
	@./scripts/status.sh

.PHONY: watch
watch: ## Follow Argo CD convergence
	@watch -n 3 'kubectl --context $(CLUSTER) -n argocd get applications \
		-o custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status'

.PHONY: logs
logs: ## Tail a service: make logs SVC=kyc
	@kubectl --context $(CLUSTER) -n $(NS) logs -f deploy/$(SVC) --tail=100

.PHONY: load
load: ## Generate traffic: make load DURATION=300 RPS=10
	@./scripts/load.sh $(or $(DURATION),120) $(or $(RPS),5)

## -------------------------------------------------------------------- dev ---

.PHONY: test
test: ## Unit tests with the race detector
	@go test -race ./...

.PHONY: lint
lint: ## go vet + gofmt + helm lint
	@go vet ./...
	@test -z "$$(gofmt -l ./cmd ./internal)" || { echo "not gofmt'd:"; gofmt -l ./cmd ./internal; exit 1; }
	@for c in deploy/charts/*/; do helm lint "$$c" >/dev/null && echo "  ✓ $$(basename $$c)"; done

.PHONY: render
render: ## Render every chart to /tmp/kalapa-rendered (what Argo CD will apply)
	@mkdir -p /tmp/kalapa-rendered
	@for c in deploy/charts/*/; do \
		n=$$(basename $$c); \
		helm dependency build $$c >/dev/null 2>&1 || true; \
		helm template $$n $$c -n kalapa > /tmp/kalapa-rendered/$$n.yaml && echo "  ✓ $$n"; \
	done
	@echo "  -> /tmp/kalapa-rendered"

.PHONY: build
build: ## Build all three images straight into the cluster's Docker daemon
	@# The repository prefix is read from the chart rather than hardcoded, so
	@# this keeps working after scripts/init-repo.sh rewrites it to your owner.
	@eval $$(minikube -p $(CLUSTER) docker-env) && \
	for s in gateway kyc scoring; do \
		repo=$$(awk '/repository:/ {print $$2; exit}' deploy/charts/$$s/values.yaml); \
		echo "  building $$s -> $$repo:latest"; \
		docker build -q -f build/Dockerfile --build-arg "SERVICE=$$s" \
			-t "$$repo:latest" . >/dev/null && echo "    ✓ $$s"; \
	done
	@echo "  Images live in the cluster's daemon; pullPolicy IfNotPresent finds them."

## --------------------------------------------------------------- consoles ---

.PHONY: argocd-password
argocd-password: ## Print the Argo CD admin password
	@kubectl --context $(CLUSTER) -n argocd get secret argocd-initial-admin-secret \
		-o jsonpath='{.data.password}' | base64 -d; echo

.PHONY: pf-prometheus
pf-prometheus: ## Prometheus UI on localhost:9090
	@kubectl --context $(CLUSTER) -n observability port-forward \
		svc/prometheus-kube-prometheus-prometheus 9090:9090

.PHONY: pf-grafana
pf-grafana: ## Grafana on localhost:3000 (admin/admin)
	@kubectl --context $(CLUSTER) -n observability port-forward svc/prometheus-grafana 3000:80

.PHONY: pf-argocd
pf-argocd: ## Argo CD UI on localhost:8080
	@kubectl --context $(CLUSTER) -n argocd port-forward svc/argocd-server 8080:80

.PHONY: psql
psql: ## Open a psql shell on the application database
	@kubectl --context $(CLUSTER) -n data exec -it kalapa-db-1 -- psql -U kalapa -d kalapa

.PHONY: kafka-console
kafka-console: ## Read the event topic from the beginning
	@kubectl --context $(CLUSTER) -n kafka run kafka-consumer-$$RANDOM --rm -it --restart=Never \
		--image=quay.io/strimzi/kafka:latest-kafka-4.3.1 -- \
		bin/kafka-console-consumer.sh \
		--bootstrap-server kalapa-kafka-bootstrap:9092 \
		--topic kyc.application.submitted --from-beginning

.PHONY: help
help:
	@echo "Kalapa platform"
	@echo
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: up-local
up-local: ## Install everything with plain Helm (no Argo CD, no GitHub)
	@./scripts/90-install-local.sh
