#!/bin/bash
set -e

# Script to build and push multi-arch Docker images
# Usage: ./scripts/docker-build-push.sh [version]
#   If no version provided, uses VERSION file
#   Set DRY_RUN=1 to test without pushing

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
DOCKER_REPO="${DOCKER_REPO:-ghcr.io/ethanadams/synthetics}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
DRY_RUN="${DRY_RUN:-0}"

# Detect current platform for dry run
CURRENT_PLATFORM="linux/amd64"
if [ "$(uname -m)" = "arm64" ] || [ "$(uname -m)" = "aarch64" ]; then
    CURRENT_PLATFORM="linux/arm64"
fi

# Get version
if [ -n "$1" ]; then
    VERSION="$1"
elif [ -f "$ROOT_DIR/VERSION" ]; then
    VERSION=$(cat "$ROOT_DIR/VERSION" | tr -d '\n')
else
    echo -e "${RED}Error: No version provided and VERSION file not found${NC}"
    echo "Usage: $0 [version]"
    exit 1
fi

echo -e "${GREEN}Building Docker image${NC}"
echo "  Repository: $DOCKER_REPO"
echo "  Version:    $VERSION"
if [ "$DRY_RUN" = "1" ]; then
    echo "  Platform:   $CURRENT_PLATFORM (dry run - local only)"
else
    echo "  Platforms:  $PLATFORMS (multi-arch)"
fi
echo "  Dry Run:    $DRY_RUN"
echo ""

# Check if logged in to container registry
if [ "$DRY_RUN" = "0" ]; then
    if ! docker info > /dev/null 2>&1; then
        echo -e "${RED}Error: Docker daemon not running${NC}"
        exit 1
    fi

    echo -e "${YELLOW}Checking container registry authentication...${NC}"
    if ! docker pull $DOCKER_REPO:latest > /dev/null 2>&1 && ! echo "$DOCKER_REPO" | grep -q "^ghcr.io/"; then
        echo -e "${RED}Warning: Not authenticated to container registry or no access to $DOCKER_REPO${NC}"
        echo "Run: docker login ghcr.io"
        read -p "Continue anyway? (y/N) " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi
fi

# Setup buildx builder
echo -e "${YELLOW}Setting up Docker buildx...${NC}"
if ! docker buildx inspect synthetics-builder > /dev/null 2>&1; then
    docker buildx create --name synthetics-builder --use --bootstrap
else
    docker buildx use synthetics-builder
fi

# Build tags
TAGS=(
    "${DOCKER_REPO}:${VERSION}"
    "${DOCKER_REPO}:latest"
)

# Check if version is semver
if [[ $VERSION =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    MAJOR="${BASH_REMATCH[1]}"
    MINOR="${BASH_REMATCH[2]}"
    TAGS+=("${DOCKER_REPO}:${MAJOR}")
    TAGS+=("${DOCKER_REPO}:${MAJOR}.${MINOR}")
fi

# Build tag arguments
TAG_ARGS=()
for tag in "${TAGS[@]}"; do
    TAG_ARGS+=("--tag" "$tag")
done

echo -e "${YELLOW}Building images with tags:${NC}"
for tag in "${TAGS[@]}"; do
    echo "  - $tag"
done
echo ""

# Determine platform(s) to build
if [ "$DRY_RUN" = "1" ]; then
    BUILD_PLATFORMS="$CURRENT_PLATFORM"
    echo -e "${YELLOW}DRY RUN MODE: Building for current platform only ($CURRENT_PLATFORM)${NC}"
else
    BUILD_PLATFORMS="$PLATFORMS"
fi

# Build command
BUILD_CMD=(
    docker buildx build
    --platform "$BUILD_PLATFORMS"
    --file "$ROOT_DIR/deployments/Dockerfile"
    "${TAG_ARGS[@]}"
)

# Add cache args only for non-dry-run (registry push)
if [ "$DRY_RUN" = "0" ]; then
    BUILD_CMD+=(
        --cache-from "type=registry,ref=${DOCKER_REPO}:buildcache"
        --cache-to "type=registry,ref=${DOCKER_REPO}:buildcache,mode=max"
        --push
    )
else
    BUILD_CMD+=(--load)
fi

BUILD_CMD+=("$ROOT_DIR")

# Execute build
echo -e "${GREEN}Executing build...${NC}"
echo "${BUILD_CMD[@]}"
echo ""

"${BUILD_CMD[@]}"

if [ "$DRY_RUN" = "0" ]; then
    echo ""
    echo -e "${GREEN}✓ Successfully built and pushed images!${NC}"
    echo ""
    echo "Images pushed:"
    for tag in "${TAGS[@]}"; do
        echo "  - $tag"
    done
    echo ""
    echo "Pull with: docker pull ${DOCKER_REPO}:${VERSION}"
else
    echo ""
    echo -e "${GREEN}✓ Build completed (dry run)${NC}"
    echo "To push images, run without DRY_RUN:"
    echo "  $0 $VERSION"
fi
