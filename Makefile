.PHONY: help build run test clean docker-build docker-push k8s-deploy k8s-delete k8s-status all

# Variables
APP_NAME := gprxy
REGISTRY ?= your-registry
VERSION ?= latest
IMAGE := $(REGISTRY)/$(APP_NAME):$(VERSION)
NAMESPACE := gprxy

# Colors for output
GREEN  := \033[0;32m
YELLOW := \033[1;33m
NC     := \033[0m # No Color

help: ## Show this help message
	@echo "$(GREEN)Available targets:$(NC)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(YELLOW)%-20s$(NC) %s\n", $$1, $$2}'

build: ## Build the Go binary
	@echo "$(GREEN)Building $(APP_NAME)...$(NC)"
	go build -o $(APP_NAME) .
	@echo "$(GREEN)Build complete$(NC)"

run: ## Run the proxy locally
	@echo "$(GREEN)Starting $(APP_NAME) proxy...$(NC)"
	./$(APP_NAME) start

test: ## Run tests
	@echo "$(GREEN)Running tests...$(NC)"
	go test -v ./...

clean: ## Clean build artifacts
	@echo "$(GREEN)Cleaning up...$(NC)"
	rm -f $(APP_NAME)
	go clean
	@echo "$(GREEN)Clean complete$(NC)"

docker-build: ## Build Docker image
	@echo "$(GREEN)Building Docker image: $(IMAGE)$(NC)"
	docker build -t $(IMAGE) .
	docker tag $(IMAGE) $(REGISTRY)/$(APP_NAME):latest
	@echo "$(GREEN)Docker build complete$(NC)"

docker-push: docker-build ## Push Docker image to registry
	@echo "$(GREEN)Pushing Docker image: $(IMAGE)$(NC)"
	docker push $(IMAGE)
	docker push $(REGISTRY)/$(APP_NAME):latest
	@echo "$(GREEN)Docker push complete$(NC)"

k8s-deploy: ## Deploy to Kubernetes
	@echo "$(GREEN)Deploying to Kubernetes...$(NC)"
	kubectl apply -f k8s/namespace.yaml
	kubectl apply -f k8s/secret.yaml
	kubectl apply -f k8s/configmap.yaml
	kubectl apply -f k8s/serviceaccount.yaml
	kubectl apply -f k8s/deployment.yaml
	kubectl apply -f k8s/service.yaml
	@echo "$(GREEN)Deployment complete$(NC)"
	@echo "$(YELLOW)Waiting for rollout...$(NC)"
	kubectl rollout status deployment/$(APP_NAME) -n $(NAMESPACE) --timeout=5m
	@echo "$(GREEN)Rollout complete$(NC)"

k8s-delete: ## Delete Kubernetes resources
	@echo "$(YELLOW)Deleting Kubernetes resources...$(NC)"
	kubectl delete -f k8s/ --ignore-not-found=true
	@echo "$(GREEN)Resources deleted$(NC)"

k8s-status: ## Show Kubernetes deployment status
	@echo "$(GREEN)Deployment Status:$(NC)"
	kubectl get all -n $(NAMESPACE)
	@echo "\n$(GREEN)LoadBalancer Endpoint:$(NC)"
	@kubectl get svc $(APP_NAME) -n $(NAMESPACE) -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || echo "Pending..."

k8s-logs: ## Tail logs from Kubernetes pods
	kubectl logs -n $(NAMESPACE) -l app=$(APP_NAME) -f --tail=100

k8s-restart: ## Restart deployment
	kubectl rollout restart deployment/$(APP_NAME) -n $(NAMESPACE)
	kubectl rollout status deployment/$(APP_NAME) -n $(NAMESPACE)


all: clean build docker-push k8s-deploy ## Clean, build, push, and deploy everything
	@echo "$(GREEN)Complete deployment finished.$(NC)"
	@make k8s-status

# Development targets
dev-build: build ## Quick local build for development
	@echo "$(GREEN)Development build ready$(NC)"

dev-run: build run ## Build and run locally

# Docker compose for local testing (if docker-compose.yml exists)
dev-up: ## Start local environment with docker-compose
	docker-compose up -d

dev-down: ## Stop local environment
	docker-compose down

# Utility targets
fmt: ## Format Go code
	go fmt ./...

vet: ## Run go vet
	go vet ./...

lint: fmt vet ## Run formatters and linters
	@echo "$(GREEN)Linting complete$(NC)"

