#!/bin/bash

# Production Docker build script for LLM Proxy
# This script builds the production Docker image with proper versioning and metadata

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
IMAGE_NAME="llm-proxy"
TAG="latest"
REGISTRY=""
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

Build production Docker image for LLM Proxy

OPTIONS:
    -n, --name IMAGE_NAME       Docker image name (default: llm-proxy)
    -t, --tag TAG              Docker image tag (default: latest)
    -r, --registry REGISTRY    Docker registry prefix (optional)
    -v, --version VERSION      Application version (default: git describe or commit)
    -p, --push                 Push image to registry after build
    -h, --help                 Show this help message

EXAMPLES:
    $0                                          # Build with defaults
    $0 -t v1.0.0 -p                           # Build and push with tag v1.0.0
    $0 -r myregistry.com -n myapp -t latest   # Build with custom registry and name

EOF
}

# Parse command line arguments
PUSH=false
VERSION=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -n|--name)
            IMAGE_NAME="$2"
            shift 2
            ;;
        -t|--tag)
            TAG="$2"
            shift 2
            ;;
        -r|--registry)
            REGISTRY="$2"
            shift 2
            ;;
        -v|--version)
            VERSION="$2"
            shift 2
            ;;
        -p|--push)
            PUSH=true
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

# Generate build metadata
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
VCS_REF=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Generate version if not provided
if [[ -z "$VERSION" ]]; then
    if git describe --tags --exact-match HEAD >/dev/null 2>&1; then
        VERSION=$(git describe --tags --exact-match HEAD)
    elif git describe --tags >/dev/null 2>&1; then
        VERSION=$(git describe --tags)
    else
        VERSION="dev-$VCS_REF"
    fi
fi

# Construct full image name
FULL_IMAGE_NAME="$IMAGE_NAME:$TAG"
if [[ -n "$REGISTRY" ]]; then
    FULL_IMAGE_NAME="$REGISTRY/$FULL_IMAGE_NAME"
fi

# Print build information
print_info "Building production Docker image..."
echo "  Image: $FULL_IMAGE_NAME"
echo "  Version: $VERSION"
echo "  Build Date: $BUILD_DATE"
echo "  VCS Ref: $VCS_REF"
echo "  Dockerfile: $DOCKERFILE"
echo "  Dockerignore: $DOCKERIGNORE"
echo

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
print_info "Starting Docker build..."
docker build \
    --file "$DOCKERFILE" \
    --build-arg BUILD_DATE="$BUILD_DATE" \
    --build-arg VCS_REF="$VCS_REF" \
    --build-arg VERSION="$VERSION" \
    --tag "$FULL_IMAGE_NAME" \
    --progress=plain \
    .

if [[ $? -eq 0 ]]; then
    print_success "Docker build completed successfully!"
    
    # Show image information
    print_info "Image details:"
    docker images "$FULL_IMAGE_NAME" --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}\t{{.CreatedAt}}"
    
    # Push to registry if requested
    if [[ "$PUSH" == true ]]; then
        if [[ -z "$REGISTRY" ]]; then
            print_warning "No registry specified, skipping push"
        else
            print_info "Pushing image to registry..."
            docker push "$FULL_IMAGE_NAME"
            if [[ $? -eq 0 ]]; then
                print_success "Image pushed successfully!"
            else
                print_error "Failed to push image"
                exit 1
            fi
        fi
    fi
    
    print_success "Production build completed!"
    echo
    echo "To run the container:"
    echo "  docker run -p 9002:9002 $FULL_IMAGE_NAME"
    echo
    echo "To push manually:"
    echo "  docker push $FULL_IMAGE_NAME"
    
else
    print_error "Docker build failed!"
    exit 1
fi 
