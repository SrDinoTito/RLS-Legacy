# rls-captain-backend — imagen del dashboard (TASK-008, REQ-030, DEC-006)
#
# Multi-stage: la compilacion Go corre en la etapa de build (nunca dentro de
# los limites de recursos del runtime de compose). Imagen final alpine + el
# binario estatico: objetivo < 30MB.
#
# NOTA toolchain (DECISION-008): go.mod exige Go >= 1.26.0 (modernc.org/sqlite
# v1.61.0), la base es golang:1.22-alpine (GOTOOLCHAIN=local, PATH reseteado);
# por eso se exporta PATH y GOTOOLCHAIN=auto para auto-descargar 1.26.
# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS build
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    GOTOOLCHAIN=auto \
    CGO_ENABLED=0

WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY backend/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go build -trimpath -ldflags "-s -w" -o /out/server ./cmd/server && \
    mkdir -p /out/downloads && \
    GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o /out/downloads/rls-stowaway-linux-amd64 ./cmd/stowaway && \
    GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o /out/downloads/rls-stowaway-linux-arm64 ./cmd/stowaway && \
    cd /out/downloads && \
    sha256sum rls-stowaway-linux-amd64 > rls-stowaway-linux-amd64.sha256 && \
    sha256sum rls-stowaway-linux-arm64 > rls-stowaway-linux-arm64.sha256

# ---------------------------------------------------------------- runtime ---
FROM alpine:3.20
# `nobody` (uid 65534) ya existe en alpine. El volumen /data se copia con
# ownership nobody:root al crearse, así USER nobody puede escribir SQLite/WAL.
RUN apk add --no-cache ca-certificates \
 && mkdir -p /data \
 && chown nobody:root /data \
 && chmod 0770 /data

COPY --from=build /out/server /srv/server
COPY --from=build /out/downloads/ /srv/downloads/
RUN chmod 0755 /srv/server

# Datos de SQLite (usuarios, agentes, metricas, alertas) fuera del contenedor.
VOLUME /data
EXPOSE 8080

USER nobody

# Binario estatico (CGO_ENABLED=0): corre sin dependencias libc.
ENTRYPOINT ["/srv/server"]

LABEL org.opencontainers.image.title="rls-captain-backend" \
      org.opencontainers.image.description="Backend Go + Chi + SQLite del dashboard RLS Legacy (heartbeat, offline, tokens, thresholds, alertas, SSE)." \
      org.opencontainers.image.version="1.0.0" \
      org.opencontainers.image.licenses="MIT"
