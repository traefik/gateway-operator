IMG ?= traefik/traefik-operator:latest
CONTROLLER_GEN_VERSION ?= v0.18.0
CONTROLLER_GEN = go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: all
all: build

.PHONY: generate
generate: ## Generate deepcopy methods.
	$(CONTROLLER_GEN) object paths=./api/...

.PHONY: manifests
manifests: ## Generate CRD and RBAC manifests.
	$(CONTROLLER_GEN) crd rbac:roleName=manager-role paths=./... \
		output:crd:dir=config/crd output:rbac:dir=config/rbac

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test ./...

.PHONY: build
build: generate fmt vet ## Build the operator binary.
	go build -o bin/traefik-operator ./cmd

.PHONY: run
run: generate fmt vet ## Run the operator against the current kubeconfig.
	go run ./cmd

.PHONY: docker-build
docker-build:
	docker build -t $(IMG) .

.PHONY: install
install: manifests ## Install CRDs into the cluster.
	kubectl apply -f config/crd

.PHONY: deploy
deploy: manifests ## Deploy the operator into the cluster.
	kubectl apply -f config/crd
	kubectl apply -f config/manager
