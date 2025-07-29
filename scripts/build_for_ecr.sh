#!/bin/bash

# ECR Docker build and push script for LLM Proxy
# This script builds and pushes Docker images to AWS ECR

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
DOCKERFILE="build/Dockerfile.prod"
DOCKERIGNORE=".dockerignore.prod"

# Function to print colored output
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to show usage
show_usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Build and push Docker image to AWS ECR for LLM Proxy

HARDCODED VALUES:
    ECR Registry: 183605072238.dkr.ecr.us-west-2.amazonaws.com
    Repository: llm-proxy
    Region: us-west-2

OPTIONS:
    -v, --version VERSION    Application version (default: dev-{git-sha})
    -s, --sha SHA           Git SHA for tagging (default: current commit)
    --skip-aws-setup        Skip AWS CLI installation and ECR login
    -h, --help              Show this help message

EXAMPLES:
    $0                                    # Build and push with defaults (to llm-proxy repo)
    $0 -v v1.0.0                         # Build and push with specific version
    $0 --skip-aws-setup                  # Skip AWS setup (assume already configured)

EOF
}

# Parse command line arguments
SKIP_AWS_SETUP=false
VERSION=""
SHA=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -v|--version)
            VERSION="$2"
            shift 2
            ;;
        -s|--sha)
            SHA="$2"
            shift 2
            ;;
        --skip-aws-setup)
            SKIP_AWS_SETUP=true
            shift
            ;;
        -h|--help)
            show_usage
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            show_usage
            exit 1
            ;;
    esac
done

# Ensure we're in the project root
if [[ ! -f "go.mod" ]]; then
    print_error "Must be run from project root (where go.mod exists)"
    exit 1
fi

# Set ECR variables (hardcoded values)
ECR_URL_PREFIX=183605072238.dkr.ecr.us-west-2.amazonaws.com
AWS_ECR_REPOSITORY_NAME=llm-proxy
AWS_DEFAULT_REGION=us-west-2

# Generate build metadata
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Generate SHA if not provided
if [[ -z "$SHA" ]]; then
    SHA=$(git rev-parse --short HEAD)
fi

# Generate version if not provided
if [[ -z "$VERSION" ]]; then
    VERSION="dev-${SHA}"
fi

# Set image URL
IMAGE_URL="${ECR_URL_PREFIX}/${AWS_ECR_REPOSITORY_NAME}"

# Print build information
print_info "Building and pushing to AWS ECR..."
echo "  Registry: ${ECR_URL_PREFIX}"
echo "  Repository: ${AWS_ECR_REPOSITORY_NAME}"
echo "  Image URL: ${IMAGE_URL}"
echo "  Version: ${VERSION}"
echo "  SHA Tag: ${SHA}"
echo "  Build Date: ${BUILD_DATE}"
echo "  Dockerfile: ${DOCKERFILE}"
echo "  Region: ${AWS_DEFAULT_REGION}"
echo

# Setup AWS and Docker (unless skipped)
if [[ "$SKIP_AWS_SETUP" == false ]]; then
    print_info "Setting up AWS CLI and Docker..."
    
    # Install AWS CLI if not present
    if ! command -v aws &> /dev/null; then
        print_info "Installing AWS CLI..."
        curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip"
        unzip awscliv2.zip
        sudo ./aws/install
        rm -rf awscliv2.zip aws/
        print_success "AWS CLI installed successfully"
    else
        print_info "AWS CLI already installed"
    fi
    
    # Login to AWS ECR
    print_info "Logging into AWS ECR..."
    
    if [[ $? -eq 0 ]]; then
        print_success "Successfully logged into ECR"
    else
        print_error "Failed to login to ECR"
        exit 1
    fi
    
else
    print_info "Skipping AWS setup (assume already configured)"
fi

# Check if required files exist
if [[ ! -f "$DOCKERFILE" ]]; then
    print_error "Dockerfile not found: $DOCKERFILE"
    exit 1
fi

if [[ ! -f "$DOCKERIGNORE" ]]; then
    print_warning "Dockerignore not found: $DOCKERIGNORE"
fi

# Copy dockerignore file temporarily
if [[ -f "$DOCKERIGNORE" ]]; then
    cp "$DOCKERIGNORE" .dockerignore
    CLEANUP_DOCKERIGNORE=true
else
    CLEANUP_DOCKERIGNORE=false
fi

# Function to cleanup on exit
cleanup() {
    if [[ "$CLEANUP_DOCKERIGNORE" == true ]]; then
        rm -f .dockerignore
    fi
}
trap cleanup EXIT

# Build the Docker image
print_info "Building Docker image..."
print_info "Building image for SHA: ${SHA}"

docker build --progress=plain \
    --build-arg BUILD_DATE="${BUILD_DATE}" \
    --build-arg VCS_REF="${SHA}" \
    --build-arg VERSION="${VERSION}" \
    -f "${DOCKERFILE}" . \
    -t "${IMAGE_URL}:${SHA}" \
    -t "${IMAGE_URL}:latest"

if [[ $? -ne 0 ]]; then
    print_error "Docker build failed!"
    exit 1
fi

print_success "Docker build completed successfully!"

# Show image information
print_info "Image details:"
docker images "${IMAGE_URL}" --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}\t{{.CreatedAt}}"

# Push images to ECR
print_info "Pushing images to ECR..."

print_info "Pushing ${IMAGE_URL}:${SHA}..."
docker push "${IMAGE_URL}:${SHA}"
if [[ $? -ne 0 ]]; then
    print_error "Failed to push image with SHA tag"
    exit 1
fi

print_info "Pushing ${IMAGE_URL}:latest..."
docker push "${IMAGE_URL}:latest"
if [[ $? -ne 0 ]]; then
    print_error "Failed to push image with latest tag"
    exit 1
fi

print_success "Successfully pushed both images to ECR!"
echo
print_success "ECR build and push completed!"
echo "Images pushed:"
echo "  - ${IMAGE_URL}:${SHA}"
echo "  - ${IMAGE_URL}:latest" 
