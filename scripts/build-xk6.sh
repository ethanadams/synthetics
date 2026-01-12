#!/bin/bash

set -e

echo "Building custom k6 binary with Storj extension..."

# Check if xk6 is installed
if ! command -v xk6 &> /dev/null; then
    echo "xk6 not found. Installing..."
    go install go.k6.io/xk6/cmd/xk6@latest
fi

# Get the repository root directory
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "Repository root: $REPO_ROOT"

# Build k6 with the Storj extension
cd "$REPO_ROOT"

xk6 build latest \
    --with github.com/ethanadams/synthetics="$REPO_ROOT" \
    --output ./k6

echo "Successfully built k6 binary at: $REPO_ROOT/k6"
echo ""
echo "Test the binary with:"
echo "  ./k6 version"
echo ""
echo "Run a test with:"
echo "  export STORJ_ACCESS_GRANT='your-access-grant'"
echo "  ./k6 run --env STORJ_ACCESS_GRANT=\$STORJ_ACCESS_GRANT scripts/tests/upload_download.js"
