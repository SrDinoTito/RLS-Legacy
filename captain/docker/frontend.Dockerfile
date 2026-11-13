# rls-captain-frontend — imagen del dashboard web (TASK-008, REQ-030, DEC-006)
#
# Multi-stage: Bun construye los assets y también sirve el frontend en runtime.
# El mismo servidor Bun resuelve el fallback SPA y proxya API/SSE a Captain.
#
# NOTA toolchain (DECISION-008 analogue): el brief original pinneaba
# oven/bun:1.2-alpine, pero con @sveltejs/kit ^3.0.0 el build se corta tras
# "Analysing routes" y no escribe build/; con bun 1.4 RC=0. Se pinnea 1.4.
# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM oven/bun:1.4-alpine AS build
WORKDIR /app

COPY frontend/package.json frontend/bun.lock ./
COPY frontend/ .
# REQ-004 del proyecto: SIEMPRE Bun (prohibido npm/yarn/pnpm).
RUN bun install --frozen-lockfile && bun run prepare && bun run build

# ---------------------------------------------------------------- runtime ---
FROM oven/bun:1.4-alpine
WORKDIR /app
ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=3008 \
    BACKEND_URL=http://backend:8080
COPY frontend/server.ts ./server.ts
COPY --from=build /app/build/ ./build/
EXPOSE 3008
CMD ["bun", "run", "server.ts"]

LABEL org.opencontainers.image.title="rls-legacy-frontend" \
      org.opencontainers.image.description="Frontend Bun del dashboard RLS Legacy." \
      org.opencontainers.image.version="1.0.1" \
      org.opencontainers.image.licenses="MIT"
