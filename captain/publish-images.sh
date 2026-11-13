#!/bin/sh
# Publica manualmente las imágenes multi-arquitectura; no requiere GitHub Actions.
set -eu

TAG=${1:-}
if [ -z "$TAG" ]; then
  echo "Uso: $0 <tag> (por ejemplo: v1.0.1)" >&2
  exit 2
fi

REGISTRY=ghcr.io/srdinotito
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --file "$ROOT/docker/backend.Dockerfile" \
  --tag "$REGISTRY/rls-legacy-backend:$TAG" \
  --tag "$REGISTRY/rls-legacy-backend:latest" \
  --push "$ROOT"

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --file "$ROOT/docker/frontend.Dockerfile" \
  --tag "$REGISTRY/rls-legacy-frontend:$TAG" \
  --tag "$REGISTRY/rls-legacy-frontend:latest" \
  --push "$ROOT"
