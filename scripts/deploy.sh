#!/bin/bash

# Enable 'exit on error' and 'pipefail' options
set -eo pipefail

# Deployment script for LLM Proxy and its sibling services.
# Usage: ./deploy.sh <environment> <git_sha> [service]
#   service: llm-proxy (default) | ocr-gate

DEPLOY_ENV=$1
GIT_SHA=$2
SERVICE="${3:-llm-proxy}"

# Validate arguments
if [[ -z "$DEPLOY_ENV" ]]; then
    echo "ERROR: Environment is required!"
    echo "Usage: ./deploy.sh <production> <git_sha> [service]"
    exit 1
fi

if [[ -z "$GIT_SHA" ]]; then
    echo "ERROR: Git SHA is required!"
    echo "Usage: ./deploy.sh <production> <git_sha> [service]"
    exit 1
fi

if [[ "$SERVICE" != "llm-proxy" && "$SERVICE" != "ocr-gate" ]]; then
    echo "ERROR: Unknown service '${SERVICE}' (expected llm-proxy or ocr-gate)"
    exit 1
fi

# Only allow production deployment
if [[ "$DEPLOY_ENV" != "production" ]]; then
    echo "ERROR: Only 'production' environment is supported"
    exit 1
fi

# Set up SSH for GitHub access
mkdir -p ~/.ssh
ssh-keyscan github.com >>~/.ssh/known_hosts

# ECR coordinates from the environment (see build_for_ecr.sh for the same
# contract). AWS_ECR_REGISTRY_ID is required; everything else falls back to
# the historical default so existing CI contexts keep working unchanged.
AWS_ECR_REGISTRY_ID="${AWS_ECR_REGISTRY_ID:?AWS_ECR_REGISTRY_ID must be set (12-digit AWS account ID that owns the ECR registry)}"
AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-us-west-2}"
# The image repo always matches the service being deployed, regardless of any
# AWS_ECR_REPOSITORY_NAME default inherited from the CI context.
AWS_ECR_REPOSITORY_NAME="${SERVICE}"
aws configure set region "${AWS_DEFAULT_REGION}"

ECR_URL_PREFIX="${AWS_ECR_REGISTRY_ID}.dkr.ecr.${AWS_DEFAULT_REGION}.amazonaws.com"
IMAGE_URL="${ECR_URL_PREFIX}/${AWS_ECR_REPOSITORY_NAME}:${GIT_SHA}"

# Default resource values for production (llm-proxy container only; presidio
# sidecar sizing lives in the infrastructure locals). These must keep the
# derived Fargate task_cpu/task_memory (llm-proxy + presidio) on a valid
# Fargate tier: 1024 + presidio 1024 = 2048 cpu, 2048 + presidio 4096 = 6144 mem.
CPU=1024
MEMORY=2048

echo "==========================================="
echo "Deploying ${SERVICE} to ${DEPLOY_ENV}"
echo "==========================================="
echo "Image URL: ${IMAGE_URL}"
echo "CPU: ${CPU}"
echo "Memory: ${MEMORY}"
echo "==========================================="

# Interactive guard for accidental local runs. CircleCI sets CI=true so
# the gated path runs unattended in the pipeline. A developer running
# `./scripts/deploy.sh production <sha>` from their laptop will get a
# prompt and must type the literal SHA to proceed, eliminating the
# fat-finger-into-prod failure mode.
if [[ -t 0 && "${CI:-}" != "true" ]]; then
    echo ""
    echo "⚠️  You are about to apply Terraform against PRODUCTION."
    echo "    Type the full git SHA (${GIT_SHA}) to confirm, or anything else to abort:"
    read -r confirmation
    if [[ "$confirmation" != "$GIT_SHA" ]]; then
        echo "❌ Confirmation did not match. Aborting."
        exit 1
    fi
fi

# Clone infrastructure repository
echo "Cloning infrastructure repository..."
git clone git@github.com:Instawork/infrastructure.git

# Navigate to the correct Terraform directory
cd "infrastructure/live/production/services/${SERVICE}/ecs"

# Download Terraform
export TF_IN_AUTOMATION=true
TF_VERSION=$(cat .terraform-version)
echo "Downloading Terraform ${TF_VERSION}..."
wget -q https://releases.hashicorp.com/terraform/${TF_VERSION}/terraform_${TF_VERSION}_linux_amd64.zip
unzip -q terraform_${TF_VERSION}_linux_amd64.zip

# Initialize Terraform
echo "Initializing Terraform..."
./terraform init -input=false

# Apply Terraform changes. The OCR gate sizes itself (8 vCPU / 16 GB defaults)
# and has no cpu/memory overrides, so only the proxy passes those vars.
echo "Applying Terraform configuration..."
if [[ "$SERVICE" == "ocr-gate" ]]; then
    ./terraform apply -input=false -auto-approve \
        -var env_name=prod \
        -var ecr_image_url=${IMAGE_URL}
else
    ./terraform apply -input=false -auto-approve \
        -var env_name=prod \
        -var ecr_image_url=${IMAGE_URL} \
        -var memory=${MEMORY} \
        -var cpu=${CPU}
fi

echo "==========================================="
echo "Deployment completed successfully!"
echo "Service: ${SERVICE}"
echo "Environment: ${DEPLOY_ENV}"
echo "Image: ${IMAGE_URL}"
echo "==========================================="
