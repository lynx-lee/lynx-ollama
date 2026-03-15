#!/bin/bash
#
# Ollama Web 管理界面构建脚本
#

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

VERSION="${1:-dev}"
BINARY_NAME="ollama-web"
LDFLAGS="-s -w -X main.Version=${VERSION}"

echo "📦 Building Ollama Web v${VERSION}..."

case "${2:-local}" in
    local)
        echo "  → Local build ($(go env GOOS)/$(go env GOARCH))"
        go build -ldflags="${LDFLAGS}" -o "${BINARY_NAME}" ./cmd/server/
        echo "  ✅ Built: ${SCRIPT_DIR}/${BINARY_NAME}"
        echo ""
        echo "  Usage: ./${BINARY_NAME} --project-dir /opt/ai/ollama"
        ;;
    linux)
        echo "  → Cross-compile for Linux (amd64)"
        CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o "${BINARY_NAME}-linux-amd64" ./cmd/server/
        echo "  ✅ Built: ${SCRIPT_DIR}/${BINARY_NAME}-linux-amd64"
        ;;
    linux-arm64)
        echo "  → Cross-compile for Linux (arm64)"
        CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="${LDFLAGS}" -o "${BINARY_NAME}-linux-arm64" ./cmd/server/
        echo "  ✅ Built: ${SCRIPT_DIR}/${BINARY_NAME}-linux-arm64"
        ;;
    docker)
        echo "  → Docker build"
        docker build --build-arg VERSION="${VERSION}" -t "ollama-web:${VERSION}" .
        echo "  ✅ Image: ollama-web:${VERSION}"
        ;;
    *)
        echo "Usage: $0 [version] [local|linux|linux-arm64|docker]"
        exit 1
        ;;
esac
