# rls-explorer — Imagen minima del agente RLS Legacy (TASK-007, REQ-032)
#
# Imagen total ~21MB sin comprimir (~7MB comprimida). Sin EXPOSE: solo egress
# POST hacia el dashboard.
# Corre como PID del contenedor (modo daemon) o con --once/--self-test firmando
# como argumento: docker run rls-explorer --self-test
FROM alpine:3.20

RUN apk add --no-cache curl ca-certificates

COPY explorer.sh /usr/local/bin/explorer.sh
RUN chmod 0755 /usr/local/bin/explorer.sh

# (no USER: /proc y cgroups requieren lecturas del root del container)
ENTRYPOINT ["/usr/local/bin/explorer.sh"]

LABEL org.opencontainers.image.title="rls-legacy-explorer" \
      org.opencontainers.image.description="Agente de observabilidad self-hosted RLS Legacy: heartbeat de metricas del propio contenedor (sin docker.sock)." \
      org.opencontainers.image.version="1.0.0" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://example.invalid/rls-legacy/explorer"
