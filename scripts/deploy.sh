#!/bin/bash

# Enable 'exit on error' and 'pipefail' options
set -eo pipefail

# Deployment script for LLM Proxy
# Usage: ./deploy.sh <environment> <git_sha>

DEPLOY_ENV=$1
GIT_SHA=$2

# Validate arguments
if [[ -z "$DEPLOY_ENV" ]]; then
    echo "ERROR: Environment is required!"
    echo "Usage: ./deploy.sh <production> <git_sha>"
    exit 1
fi

if [[ -z "$GIT_SHA" ]]; then
    echo "ERROR: Git SHA is required!"
    echo "Usage: ./deploy.sh <production> <git_sha>"
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

# Set AWS region
AWS_DEFAULT_REGION=us-west-2
aws configure set region ${AWS_DEFAULT_REGION}

# ECR configuration (matching build_for_ecr.sh)
ECR_URL_PREFIX=183605072238.dkr.ecr.us-west-2.amazonaws.com
AWS_ECR_REPOSITORY_NAME=llm-proxy
IMAGE_URL="${ECR_URL_PREFIX}/${AWS_ECR_REPOSITORY_NAME}:${GIT_SHA}"

# Default resource values for production
CPU=512
MEMORY=1024

echo "==========================================="
echo "Deploying LLM Proxy to ${DEPLOY_ENV}"
echo "==========================================="
echo "Image URL: ${IMAGE_URL}"
echo "CPU: ${CPU}"
echo "Memory: ${MEMORY}"
echo "==========================================="

# Clone infrastructure repository
echo "Cloning infrastructure repository..."
git clone git@github.com:Instawork/infrastructure.git

# Navigate to the correct Terraform directory
cd infrastructure/live/production/services/llm-proxy/ecs

# Download Terraform
export TF_IN_AUTOMATION=true
TF_VERSION=$(cat .terraform-version)
echo "Downloading Terraform ${TF_VERSION}..."
wget -q https://releases.hashicorp.com/terraform/${TF_VERSION}/terraform_${TF_VERSION}_linux_amd64.zip
unzip -q terraform_${TF_VERSION}_linux_amd64.zip

# Initialize Terraform
echo "Initializing Terraform..."
./terraform init -input=false

# Apply Terraform changes
echo "Applying Terraform configuration..."
./terraform apply -input=false -auto-approve \
    -var env_name=prod \
    -var ecr_image_url=${IMAGE_URL} \
    -var memory=${MEMORY} \
    -var cpu=${CPU} 

echo "==========================================="
echo "Deployment completed successfully!"
echo "Environment: ${DEPLOY_ENV}"
echo "Image: ${IMAGE_URL}"
echo "==========================================="
