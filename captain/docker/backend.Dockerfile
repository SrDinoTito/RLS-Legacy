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

FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    GOTOOLCHAIN=auto \
    CGO_ENABLED=0
ARG TARGETARCH

WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY backend/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/server ./cmd/server && \
    mkdir -p /out/downloads && \
    mkdir -p /out/data && chmod 0770 /out/data && \
    GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o /out/downloads/rls-stowaway-linux-amd64 ./cmd/stowaway && \
    GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o /out/downloads/rls-stowaway-linux-arm64 ./cmd/stowaway && \
    cd /out/downloads && \
    sha256sum rls-stowaway-linux-amd64 > rls-stowaway-linux-amd64.sha256 && \
    sha256sum rls-stowaway-linux-arm64 > rls-stowaway-linux-arm64.sha256

# ---------------------------------------------------------------- runtime ---
FROM alpine:3.20
COPY --from=build /out/server /srv/server
COPY --from=build /out/downloads/ /srv/downloads/
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# `nobody` (uid 65534) existe en Alpine; copiar el directorio ya preparado
# evita ejecutar herramientas de la arquitectura destino durante buildx.
COPY --from=build --chown=65534:0 --chmod=0770 /out/data/ /data/

# Datos de SQLite (usuarios, agentes, metricas, alertas) fuera del contenedor.
VOLUME /data
EXPOSE 8080

USER nobody

# Binario estatico (CGO_ENABLED=0): corre sin dependencias libc.
ENTRYPOINT ["/srv/server"]

LABEL org.opencontainers.image.title="rls-captain-backend" \
      org.opencontainers.image.description="Backend Go + Chi + SQLite del dashboard RLS Legacy (heartbeat, offline, tokens, thresholds, alertas, SSE)." \
      org.opencontainers.image.version="1.0.1" \
      org.opencontainers.image.licenses="MIT"
