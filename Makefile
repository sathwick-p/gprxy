.PHONY: help build run test clean docker-build docker-push k8s-deploy k8s-delete k8s-status all

# Variables
APP_NAME := gprxy
NAMESPACE := gprxy
ECR_REGISTRY = 123456789.dkr.ecr.us-west-2.amazonaws.com
ECR_REPOSITORY = go-automations
IMAGE_TAG = gprxy
REGION = us-west-2
FULL_IMAGE_NAME = $(ECR_REGISTRY)/$(ECR_REPOSITORY):$(IMAGE_TAG)
PLATFORMS = linux/amd64,linux/arm64
BUILDER_NAME = multiplatform-builder

# Colors for output
GREEN  := \033[0;32m
YELLOW := \033[1;33m
NC     := \033[0m # No Color

help:
	@echo "$(GREEN)Available targets:$(NC)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(YELLOW)%-20s$(NC) %s\n", $$1, $$2}'

build:
	@echo "$(GREEN)Building $(APP_NAME)$(NC)"
	go build -o $(APP_NAME) .
	@echo "$(GREEN)Build complete$(NC)"

run:
	@echo "$(GREEN)Starting $(APP_NAME)$(NC)"
	./$(APP_NAME) start

test:
	@echo "$(GREEN)Running tests$(NC)"
	go test -v ./...

clean:
	@echo "$(GREEN)Cleaning up$(NC)"
	rm -f $(APP_NAME)
	go clean
	@echo "$(GREEN)Clean complete$(NC)"

docker-build:
	@echo "$(GREEN)Setting up Docker Buildx$(NC)"
	@docker buildx inspect $(BUILDER_NAME) >/dev/null 2>&1 || docker buildx create --name $(BUILDER_NAME) --use
	@echo "$(GREEN)Building multi-platform Docker image: $(FULL_IMAGE_NAME)$(NC)"
	docker buildx build --platform $(PLATFORMS) -t $(FULL_IMAGE_NAME) --load .
	@echo "$(GREEN)Docker build complete$(NC)"

docker-push:
	@echo "$(GREEN)Logging in to ECR$(NC)"
	aws ecr get-login-password --region $(REGION) | docker login --username AWS --password-stdin $(ECR_REGISTRY)
	@echo "$(GREEN)Setting up Docker Buildx$(NC)"
	@docker buildx inspect $(BUILDER_NAME) >/dev/null 2>&1 || docker buildx create --name $(BUILDER_NAME) --use
	@echo "$(GREEN)Building and pushing multi-platform image: $(FULL_IMAGE_NAME)$(NC)"
	docker buildx build --platform $(PLATFORMS) -t $(FULL_IMAGE_NAME) --push .
	@echo "$(GREEN)Docker push complete$(NC)"
	@echo "$(YELLOW)Image pushed: $(FULL_IMAGE_NAME)$(NC)"

k8s-deploy: docker-push
	@echo "$(GREEN)Deploying to Kubernetes$(NC)"
	kubectl apply -f k8s/namespace.yaml
	kubectl apply -f k8s/secret.yaml
	kubectl apply -f k8s/configmap.yaml
	kubectl apply -f k8s/serviceaccount.yaml
	@echo "$(YELLOW)Updating deployment image to: $(FULL_IMAGE_NAME)$(NC)"
	kubectl set image deployment/$(APP_NAME) $(APP_NAME)=$(FULL_IMAGE_NAME) -n $(NAMESPACE) || \
		(kubectl apply -f k8s/deployment.yaml && kubectl set image deployment/$(APP_NAME) $(APP_NAME)=$(FULL_IMAGE_NAME) -n $(NAMESPACE))
	kubectl apply -f k8s/service.yaml
	kubectl apply -f k8s/service-nlb.yaml
	@echo "$(GREEN)Deployment complete$(NC)"
	@echo "$(YELLOW)Waiting for rollout$(NC)"
	kubectl rollout status deployment/$(APP_NAME) -n $(NAMESPACE) --timeout=5m
	@echo "$(GREEN)Rollout complete$(NC)"
	@echo "\n$(YELLOW)Getting NLB endpoint...$(NC)"
	@echo "$(GREEN)NLB Endpoint:$(NC)"
	@kubectl get svc gprxy-nlb -n $(NAMESPACE) -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || echo "Pending..."

k8s-delete:
	@echo "$(YELLOW)Deleting Kubernetes resources$(NC)"
	kubectl delete -f k8s/ --ignore-not-found=true
	@echo "$(GREEN)Resources deleted$(NC)"

all: clean build docker-push k8s-deploy
	@echo "$(GREEN)Deployment finished.$(NC)"
	@echo "\n$(GREEN)Service Endpoints:$(NC)"
	@echo "$(YELLOW)NLB (for PostgreSQL connections):$(NC)"
	@kubectl get svc gprxy-nlb -n $(NAMESPACE) -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || echo "  Pending..."
	@echo "\n$(YELLOW)Pods:$(NC)"
	@kubectl get pods -n $(NAMESPACE)

# ECR specific targets
ecr-login:
	@echo "$(GREEN)Logging in to ECR $(ECR_REGISTRY)$(NC)"
	aws ecr get-login-password --region $(REGION) | docker login --username AWS --password-stdin $(ECR_REGISTRY)
	@echo "$(GREEN)ECR login successful$(NC)"