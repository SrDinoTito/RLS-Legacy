# rls-captain-frontend — imagen del dashboard web (TASK-008, REQ-030, DEC-006)
#
# Multi-stage: el build Bun corre en build (nunca dentro del runtime).
# nginx:1.27-alpine sirve el estatico + proxy /api al backend + SSE sin
# buffering. Imagen final ~25-30MB.
#
# NOTA toolchain (DECISION-008 analogue): el brief original pinneaba
# oven/bun:1.2-alpine, pero con @sveltejs/kit ^3.0.0 el build se corta tras
# "Analysing routes" y no escribe build/; con bun 1.4 RC=0. Se pinnea 1.4.
# syntax=docker/dockerfile:1

FROM oven/bun:1.4-alpine AS build
WORKDIR /app

COPY frontend/package.json frontend/bun.lock ./
COPY frontend/ .
# REQ-004 del proyecto: SIEMPRE Bun (prohibido npm/yarn/pnpm).
RUN bun install --frozen-lockfile && bun run prepare && bun run build

# ---------------------------------------------------------------- runtime ---
# NOTE tamaños (DEC-006): la base nginx:1.27-alpine contabiliza ~75MB via
# `docker images` en este entorno (containerd store) — rompe el objetivo
# (<30MB) y el bound e2e (40MB). alpine:3.20 + nginx del repositorio Alpine
# queda en ~16MB reales. nginx.conf va a http.d/ (include nativo de alpine).
FROM alpine:3.20
RUN apk add --no-cache nginx ca-certificates
COPY docker/nginx.conf /etc/nginx/http.d/default.conf
COPY --from=build /app/build/ /usr/share/nginx/html/
EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
