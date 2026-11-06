#!/bin/sh
# -----------------------------------------------------------------------------
# e2e-smoke.sh — prueba de humo end-to-end del explorer contra el backend REAL
# (captain/backend) de RLS Legacy. TASK-007.
#
# TODO el trabajo pesado corre EN DOCKER (CON-007 del spec: nada se compila ni
# ejecuta en el host); este script solo orquesta comandos `docker`.
#
# Pasos:
#   1. Compila el binario del backend con golang EN Docker (GOTOOLCHAIN auto,
#      modulos cacheados en el volumen rls-gomodcache — DECISION-008);
#      binario estatico (CGO_ENABLED=0; modernc.org/sqlite es pure-Go).
#   2. Levanta el backend en alpine:3.20 en la red rls-e2e + volumen rls-e2e-data.
#   3. Bootstrap del primer admin + login con curl DENTRO de docker
#      (contenedor curlimages/curl; cookie en el volumen efimero rls-e2e-tokens).
#   4. Crea el token de agente: la API HTTP /api/v1/tokens aun no existe
#      (queda para TASK-004, el router del backend no la tiene todavia), asi
#      que se inserta su SHA-256 hex DIRECTO en SQLite desde un contenedor
#      alpine con sqlite3 — el mismo hashing que el backend usa
#      (api.HashAgentToken = hex(sha256(plaintext))).
#   5. Build de la imagen rls-explorer y validacion:
#        --self-test (exit 0, 64m) -> --once (HTTP 200) -> loop 25s sin errores
#        -> docker stats (<20MB) -> logs del backend "agent registered".
#
# Uso:  sh explorer/scripts/e2e-smoke.sh   (desde la raiz del repo; solo
#        requiere docker). Seguro de re-ejecutar: limpia todo al iniciar.
# Deja rastros inspeccionables: red rls-e2e y volumen rls-e2e-data:
#   docker run --rm -v rls-e2e-data:/d alpine:3.20 sqlite3 /d/rls.db \
#     'select * from agents; select * from metrics;'
# -----------------------------------------------------------------------------
set -u

[ -f explorer/explorer.sh ] || {
    echo "FATAL: ejecuta este script desde la raiz del repo (se requiere explorer/)" >&2
    exit 1
}
BACKEND_SRC="$(pwd)/captain/backend"
[ -f "$BACKEND_SRC/cmd/server/main.go" ] || {
    echo "FATAL: no se encontro captain/backend" >&2
    exit 1
}

EXP_IMAGE=rls-explorer
NET=rls-e2e
VOL_DATA=rls-e2e-data
VOL_TMP=rls-e2e-tokens
BACKEND_CONTAINER=rls-backend
EXPLORER_LOOP=rls-explorer-loop

say()  { printf '%s\n' "[e2e] $*"; }
fail() { printf '%s\n' "[e2e] FATAL: $*" >&2; exit 1; }
cleanup() {
    docker rm -f "$BACKEND_CONTAINER" "$EXPLORER_LOOP" >/dev/null 2>&1 || :
}
trap cleanup EXIT INT TERM

docker info >/dev/null 2>&1 || fail "docker no disponible"

# --- paso 0: limpieza de corridas previas -------------------------------------
say "paso 0: limpieza"
cleanup
docker network rm -f "$NET"     >/dev/null 2>&1 || :
docker volume rm -f "$VOL_DATA" >/dev/null 2>&1 || :
docker volume rm -f "$VOL_TMP"  >/dev/null 2>&1 || :

# --- paso 1: build del backend en docker --------------------------------------
say "paso 1 (CON-007): build del binario del backend EN docker (512m/1cpu)"
docker run --rm \
    -v "$BACKEND_SRC":/src \
    -v rls-gomodcache:/go/pkg/mod \
    -w /src \
    --memory 512m --cpus 1 \
    golang:1.22-alpine sh -c \
    'export PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=auto CGO_ENABLED=0; \
     go build -trimpath -o /src/bin/server ./cmd/server && echo BUILD_OK' \
    || fail "build del backend fallo (salida arriba)"
[ -f "$BACKEND_SRC/bin/server" ] || fail "no se genero captain/backend/bin/server"
say "  binario listo: captain/backend/bin/server"

# --- paso 2: red + backend ------------------------------------------------------
say "paso 2: red '$NET' + backend"
docker network create "$NET" >/dev/null   || fail "docker network create"
docker volume create "$VOL_DATA" >/dev/null || fail "docker volume create"
docker volume create "$VOL_TMP"  >/dev/null || fail "docker volume tmp create"
docker run -d --name "$BACKEND_CONTAINER" \
    --network "$NET" \
    -e RLS_DB_PATH=/data/rls.db \
    -e RLS_JWT_SECRET=e2e-smoke-secret \
    -v "$VOL_DATA":/data \
    -v "$BACKEND_SRC/bin":/srv:ro \
    --memory 256m --cpus 0.5 \
    alpine:3.20 /srv/server >/dev/null || fail "no se pudo arrancar el backend"
i=0
while [ "$i" -lt 20 ]; do
    if docker run --rm --network "$NET" curlimages/curl:latest -fsS \
        http://rls-backend:8080/healthz >/dev/null 2>&1; then
        break
    fi
    i=$((i + 1)); sleep 1
done
[ "$i" -lt 20 ] || fail "healthz no respondio en 20s"
say "  backend vivo en http://rls-backend:8080"

# --- paso 3: bootstrap + login con curl dentro de docker ------------------------
say "paso 3: bootstrap admin + login (curl en la red de docker)"
docker run --rm --network "$NET" --user 0:0 \
    -v "$VOL_TMP":/tmp/c \
    curlimages/curl:latest -sS \
    -X POST -H 'Content-Type: application/json' \
    --data '{"username":"e2e-admin","password":"e2e-pass-123"}' \
    -o /tmp/c/bootstrap.json \
    http://rls-backend:8080/api/v1/auth/bootstrap
docker run --rm --network "$NET" --user 0:0 \
    -v "$VOL_TMP":/tmp/c \
    curlimages/curl:latest -sS \
    -X POST -H 'Content-Type: application/json' \
    --data '{"username":"e2e-admin","password":"e2e-pass-123"}' \
    -c /tmp/c/cookies.txt -o /tmp/c/login.json \
    http://rls-backend:8080/api/v1/auth/login \
    || fail "login fallo"
docker run --rm -v "$VOL_TMP":/c:ro alpine:3.20 \
    grep -q rls_session /c/cookies.txt \
    || fail "login no dejo cookie de sesion"
say "  cookie de sesion OK (en volumen efimero, nunca en el host)"

# --- paso 4: token de agente ------------------------------------------------------
say "paso 4: token de agente (generacion+sha256+INSERT en sqlite, todo en docker)"
GEN=$(docker run --rm alpine:3.20 \
    sh -c 'head -c 18 /dev/urandom | busybox base64 | tr -d "/+="')
[ -n "$GEN" ] || fail "no se pudo generar el token"
AGENT_TOKEN="rlst_e2e_${GEN}"
TOK_HASH=$(printf '%s' "$AGENT_TOKEN" | docker run --rm -i alpine:3.20 sha256sum | awk '{print $1}')
[ ${#TOK_HASH} -eq 64 ] || fail "sha256 invalido: '$TOK_HASH'"
SQL="INSERT INTO tokens (token_hash, name, created_at) VALUES ('$TOK_HASH', 'e2e-smoke', strftime('%s','now'));"
docker run --rm -i \
    -e SQL="$SQL" \
    -v "$VOL_DATA":/data \
    alpine:3.20 sh -c 'apk add --no-cache sqlite >/dev/null 2>&1; echo "$SQL" | sqlite3 /data/rls.db' \
    || fail "insert sqlite fallo"
say "  token persistido (solo el hash) en rls.db; el plaintext no sale del ámbito docker"

say "paso 5: build imagen rls-explorer"
docker build --rm -t "$EXP_IMAGE" explorer/ || fail "docker build explorer"

say "paso 6: --self-test (exit 0, 64m)"
docker run --rm --memory 64m "$EXP_IMAGE" --self-test || fail "self-test fallo"

say "paso 7: --once contra el backend real (debe dar 200)"
ONCE_OUT=$(docker run --rm --network "$NET" \
    -e RLS_DASHBOARD_URL=http://rls-backend:8080 \
    -e RLS_TOKEN="$AGENT_TOKEN" \
    -e RLS_INTERVAL=10 \
    "$EXP_IMAGE" --once 2>&1) || fail "once fallo: $ONCE_OUT"
printf '%s\n' "$ONCE_OUT" | sed 's/^/    /'
printf '%s\n' "$ONCE_OUT" | grep -q 'heartbeat ok (200)' \
    || fail "no hubo 200 en --once"

say "paso 8: loop 25s (sin errores de backoff) + docker stats (<20MB)"
docker rm -f "$EXPLORER_LOOP" >/dev/null 2>&1 || :
docker run -d --name "$EXPLORER_LOOP" --network "$NET" \
    -e RLS_DASHBOARD_URL=http://rls-backend:8080 \
    -e RLS_TOKEN="$AGENT_TOKEN" \
    -e RLS_INTERVAL=5 \
    --memory 64m "$EXP_IMAGE" >/dev/null || fail "loop no arranco"
sleep 25
say "  logs del loop:"
docker logs "$EXPLORER_LOOP" 2>&1 | sed 's/^/    /'
if docker logs "$EXPLORER_LOOP" 2>&1 | grep -q 'dropeado\|FATAL'; then
    fail "hubo errores de backoff en el loop (log arriba)"
fi
VMEM=$(docker stats --no-stream --format '{{.MemUsage}}' "$EXPLORER_LOOP" | head -n 1)
VMEM_NUM=$(printf '%s' "$VMEM" | awk '{print $1}' | tr -d '[:alpha:]')
VMEM_UNIT=$(printf '%s' "$VMEM" | awk '{print $1}' | tr -d '0-9.')
case $VMEM_UNIT in
    KiB|KB) MB=$(awk -v v="$VMEM_NUM" 'BEGIN{printf "%.1f", v/1024}') ;;
    GiB|GB) MB=$(awk -v v="$VMEM_NUM" 'BEGIN{printf "%.1f", v*1024}') ;;
    *)      MB=$(awk -v v="$VMEM_NUM" 'BEGIN{printf "%.1f", v}') ;;
esac
say "  RAM del loop: $MB MB ($VMEM)"
if ! awk -v m="$MB" 'BEGIN{exit !(m < 20)}'; then
    fail "RAM del explorer $MB MB >= 20MB (REQ-005)"
fi
docker rm -f "$EXPLORER_LOOP" >/dev/null

say "paso 9: 'agent registered' en los logs del backend"
docker logs "$BACKEND_CONTAINER" 2>&1 | head -n 60 | grep -q 'agent registered' \
    || fail "no aparecio 'agent registered' en logs del backend"
docker logs "$BACKEND_CONTAINER" 2>&1 | grep 'agent registered' | head -n 1 | sed 's/^/    /'
docker rm -f "$BACKEND_CONTAINER" >/dev/null

say "E2E OK: explorer valido contra el backend real"
say "  dejo red '$NET' y volumen '$VOL_DATA' para inspeccion:"
say "    docker run --rm -v $VOL_DATA:/d alpine:3.20 sqlite3 /d/rls.db 'select * from agents;'"
