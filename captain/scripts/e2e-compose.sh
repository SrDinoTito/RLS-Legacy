#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# e2e-compose.sh — TEST E2E TOTAL de rls-monitor via `docker compose` (TASK-008,
# AC-001..006) contra el stack REAL desplegado como lo haría una persona:
#   captain (backend + frontend como servicios compose, límites y logs DEC-006)
#   + un contenedor "app de ejemplo" con el explorer insertado (REQ-032).
#
# TODO el trabajo pesado corre EN DOCKER (CON-007): este script solo orquesta
# comandos docker/docker compose; las llamadas HTTP salen de un contenedor
# curlimages/curl contra la red interna del compose. Nada se ejecuta en el host
# Windows más allá de `docker` (curl/grep/etc viven en contenedores alpine).
#
# Uso:   bash captain/scripts/e2e-compose.sh      (solo requiere docker D+).
# Idempotente: limpia stack + volumen al inicio y al final con trap EXIT.
# El cleanup PRESERVA deliberadamente: volumen rls-gomodcache / rls-bun-cache
# (cache de toolchains Go/Bun, DEC-008) y la imagen rls-explorer (agente de
# prueba). No borrarlos: reconstruirlos sería desperdicio.
# -----------------------------------------------------------------------------
set -u

# Git Bash / MSYS convierte argumentos tipo /c/... rutas Windows y rompe los
# -o /c/... del docker run. Desactivar la conversion (harmless en Linux/bash).
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

COMPOSE_DIR="$(cd "$(dirname "$0")/.." && pwd)"
# MSYS_NO_PATHCONV está ON, así que docker CLI recibe los paths tal cual:
# convertimos el cwd MSYS (/c/Users/...) a formato Windows (C:/Users/...).
case "$COMPOSE_DIR" in
  /c/*) COMPOSE_WIN="C:${COMPOSE_DIR#/c}" ;;
  *)    COMPOSE_WIN="$COMPOSE_DIR" ;;
esac
PROJ=rls-e2e
PORT=8123                      # puerto host del frontend (evita choques)
VOL_COOKIES=rls-e2e-tokens
APP=rls-e2e-app

ADMIN_USER=e2e-admin ;  ADMIN_PASS='e2e-pass-123'
VIEWER_USER=e2e-viewer ; VIEWER_PASS='e2e-view-456'

say()  { printf '%s\n' "[e2e] $*"; }
fail() { printf '%s\n' "[e2e] FATAL: $*" >&2; exit 1; }
FAILED=0
track() { # track <PASS|FAIL> <sello> <descripción...>
  local st="$1" ; local tag="$2" ; shift 2
  printf '%s\n' "[e2e] $tag: $st — $*"
  [ "$st" = "PASS" ] || FAILED=$((FAILED + 1))
}
now() { date +%s; }
size_mb() { # "30.4MB" → 30 | "1.2GB" → 1228
  local s="$1"
  case "$s" in
    *GB) awk "BEGIN{printf \"%d\", $s * 1024}" ;;
    *KB) awk "BEGIN{printf \"%d\", $s / 1024}" ;;
    *)   awk "BEGIN{printf \"%d\", $s}" ;;
  esac
}
# Compose del e2e: base + override con DB PROPIA (volumen named rls-e2e-data,
# target /data reemplaza al bind ./data del compose.yaml) para NO tocar la DB
# real del usuario (regresion bootstrap 403). Ambos -f van junto a -p rls-e2e
# en TODAS las invocaciones (cleanup incluido, para que down -v borre el
# volumen del override).
dcompose() {
  docker compose -p "$PROJ" \
    -f "$COMPOSE_WIN/compose.yaml" \
    -f "$COMPOSE_WIN/docker/compose.e2e.yaml" \
    "$@"
}

cleanup() {
  say "cleanup: down -v --remove-orphans + contenedor app + volumen cookies"
  docker rm -f "$APP"                     >/dev/null 2>&1 || :
  dcompose down -v --remove-orphans       >/dev/null 2>&1 || :
  docker network rm "${PROJ}_default"     >/dev/null 2>&1 || :
  docker volume rm "$VOL_COOKIES"         >/dev/null 2>&1 || :
}
trap cleanup EXIT INT TERM

docker info         >/dev/null 2>&1 || fail "docker no disponible"
docker compose version >/dev/null 2>&1 || fail "docker compose v2 no disponible"

# --- paso 0: limpieza de corridas previas -------------------------------------
say "paso 0: limpieza de corridas previas"
docker rm -f "$APP"             >/dev/null 2>&1 || :
dcompose down -v --remove-orphans >/dev/null 2>&1 || :
docker network rm "${PROJ}_default" >/dev/null 2>&1 || :
docker volume rm "$VOL_COOKIES" >/dev/null 2>&1 || :
docker volume create "$VOL_COOKIES" >/dev/null || fail "volume cookies"

# Env del stack de prueba (shell env, sin .env en el repo — cero rastros).
export RLS_JWT_SECRET='e2e-compose-secret-0123456789abcdef'
export RLS_RETENTION_DAYS=7
export RLS_PORT="$PORT"
# tick offline 5s: AC-003 (3×5s=15s + sweep ≤5s ⇒ ~20s) sin esperar 30s+.
export RLS_OFFLINE_TICK_SEC=5

# --- paso 1: compose up + healthchecks -----------------------------------------
say "paso 1: docker compose up -d --build (CON-007: builds dentro de Docker)"
dcompose up -d --build >/dev/null || fail "compose up fallo"

B_CONT="$PROJ-backend-1"; F_CONT="$PROJ-frontend-1"
HEALTH=""
for _ in $(seq 1 40); do
  HEALTH="$(docker inspect -f '{{.State.Health.Status}}' "$B_CONT" 2>/dev/null || :)"
  [ "$HEALTH" = "healthy" ] && break
  sleep 2
done
[ "$HEALTH" = "healthy" ] || fail "backend healthcheck no llegó a healthy (${HEALTH:-ninguno})"
docker inspect -f '{{.State.Status}}' "$F_CONT" 2>/dev/null | grep -q running \
  || fail "frontend container no está running"
say "  backend healthy + frontend running"

# Red interna del compose para curl y la app de ejemplo.
NET="$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$B_CONT")"
[ -n "$NET" ] || fail "no se obtuvo la red del compose"
say "  red del compose: $NET"

runc() { # HTTP contra el stack, DENTRO de la red del compose
  docker run --rm --network "$NET" --user 0:0 -v "$VOL_COOKIES":/c \
    curlimages/curl:latest "$@"
}

# --- paso 2: tamaños de imagen (objetivo: backend ≤35MB, frontend ≤40MB) -------
say "paso 2: tamaños de imagen"
BE_IMG="$(docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' rls-captain-backend | head -1)"
FE_IMG="$(docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' rls-captain-frontend | head -1)"
[ -n "$BE_IMG" ] && [ -n "$FE_IMG" ] || fail "imágenes rls-captain-* no encontradas"
BE_MB="$(size_mb "${BE_IMG#* }")"
FE_MB="$(size_mb "${FE_IMG#* }")"
say "  $BE_IMG"
say "  $FE_IMG"
[ "${BE_MB:-9999}" -le 35 ] && track PASS SIZES "backend ${BE_MB}MB ≤ 35MB" \
                            || track FAIL SIZES "backend ${BE_MB}MB > 35MB"
[ "${FE_MB:-9999}" -le 40 ] && track PASS SIZES "frontend ${FE_MB}MB ≤ 40MB" \
                            || track FAIL SIZES "frontend ${FE_MB}MB > 40MB"

# --- paso 3: docker stats (límites runtime DEC-006: 128M / 64M) -----------------
say "paso 3: docker stats --no-stream"
STATS="$(docker stats --no-stream --format '{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}' \
         "$B_CONT" "$F_CONT" 2>/dev/null || :)"
say "  $(echo "$STATS" | tr '\n' ' ')"
BE_MEM="$(echo "$STATS" | grep "^$B_CONT|" | cut -d'|' -f2 | cut -d'/' -f1)"
FE_MEM="$(echo "$STATS" | grep "^$F_CONT|" | cut -d'|' -f2 | cut -d'/' -f1)"
BE_MEM="$(size_mb "${BE_MEM:-0MiB}")"
FE_MEM="$(size_mb "${FE_MEM:-0MiB}")"
[ "${BE_MEM:-99999}" -le 128 ] && track PASS STATS "backend ${BE_MEM}MiB ≤ 128M" \
                              || track FAIL STATS "backend ${BE_MEM}MiB > 128M"
[ "${FE_MEM:-99999}" -le 64 ] && track PASS STATS "frontend ${FE_MEM}MiB ≤ 64M" \
                             || track FAIL STATS "frontend ${FE_MEM}MiB > 64M"

# --- paso 4 (AC-001): bootstrap admin + login ----------------------------------
say "paso 4: AC-001 bootstrap + login del primer usuario"
B1="$(runc -sS -X POST -H 'Content-Type: application/json' \
      --data "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" \
      -o /c/boot.json -w '%{http_code}' \
      http://backend:8080/api/v1/auth/bootstrap || :)"
B2="$(runc -sS -X POST -H 'Content-Type: application/json' \
      --data "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" \
      -c /c/cookies -o /dev/null -w '%{http_code}' \
      http://backend:8080/api/v1/auth/login || :)"
if [ "$B1" = "201" ] && [ "$B2" = "200" ]; then
  track PASS AC-001 "compose up + bootstrap (201) + login (200)"
else
  track FAIL AC-001 "bootstrap→$B1 login→$B2"
fi
docker run --rm -v "$VOL_COOKIES":/c alpine:3.20 \
  sh -c 'grep -q rls_session /c/cookies' || fail "cookie de sesión no quedó en el jar"

# --- paso 5: token de agente via API (REQ-013) ----------------------------------
say "paso 5: crear token de agente (plain value una sola vez)"
TOK_CODE="$(runc -sS -X POST -b /c/cookies -H 'Content-Type: application/json' \
      --data '{"name":"e2e-agent"}' -o /c/token.json -w '%{http_code}' \
      http://backend:8080/api/v1/tokens || :)"
[ "$TOK_CODE" = "201" ] || fail "creación de token → HTTP $TOK_CODE"
TOKEN="$(docker run --rm -v "$VOL_COOKIES":/c alpine:3.20 \
  sed -n 's/.*"token":"\(rlst_[a-f0-9]\{1,\}\)".*/\1/p' /c/token.json)"
[ -n "${TOKEN:-}" ] || fail "token plano no extraído"
say "  token: ${TOKEN:0:12}…"

# --- paso 6 (AC-002): app ejemplo con explorer → ONLINE en ≤ 15s ----------------
say "paso 6: AC-002 app de ejemplo con explorer insertado (REQ-032, imagen ya buildada)"
docker run -d --name "$APP" --network "$NET" \
  -e RLS_DASHBOARD_URL=http://backend:8080 \
  -e RLS_TOKEN="$TOKEN" \
  -e RLS_INTERVAL=5 \
  -e RLS_NAME=e2e-app \
  rls-explorer >/dev/null || fail "app de ejemplo no arrancó"

AC2=""
T0="$(now)"
for _ in $(seq 1 12); do
  AC2="$(runc -sS -b /c/cookies http://backend:8080/api/v1/agents 2>/dev/null \
        | grep -c '"status":"online"' || :)"
  [ "${AC2:-0}" -ge 1 ] && break
  sleep 2
done
DT=$(( $(now) - T0 ))
[ "${AC2:-0}" -ge 1 ] && track PASS AC-002 "agente ONLINE con métricas en ${DT}s (≤ 15s)" \
                     || track FAIL AC-002 "agente no ONLINE en ${DT}s"

# --- paso 7 (AC-003): docker kill → OFFLINE + alerta ≤ 3× interval --------------
say "paso 7: AC-003 kill de la app → silencio → OFFLINE + alerta offline"
docker kill "$APP" >/dev/null || fail "docker kill de la app"
AC3=""
T0="$(now)"
for _ in $(seq 1 15); do
  AC3="$(runc -sS -b /c/cookies http://backend:8080/api/v1/alerts 2>/dev/null \
        | grep -o '"type":"offline"' | head -1 || :)"
  [ -n "$AC3" ] && break
  sleep 2
done
DT=$(( $(now) - T0 ))
[ -n "$AC3" ] && track PASS AC-003 "alerta offline en ${DT}s (bound 3×5s=15s+tick)" \
             || track FAIL AC-003 "sin alerta offline en ${DT}s"
runc -sS -b /c/cookies http://backend:8080/api/v1/agents 2>/dev/null \
  | grep -q '"status":"offline"' \
  && track PASS AC-003 "agente quedó OFFLINE tras el kill" \
  || track FAIL AC-003 "agente no quedó OFFLINE"

say "  reintento: docker start de la app → ONLINE + alerta resuelta"
docker start "$APP" >/dev/null || fail "reinicio de la app"
AC3R=""
for _ in $(seq 1 10); do
  OPEN="$(runc -sS -b /c/cookies http://backend:8080/api/v1/alerts 2>/dev/null \
        | grep -o '"type":"offline"' | head -1 || :)"
  ONL="$(runc -sS -b /c/cookies http://backend:8080/api/v1/agents 2>/dev/null \
        | grep -o '"status":"online"' | head -1 || :)"
  if [ -z "$OPEN" ] && [ -n "$ONL" ]; then AC3R=ok; break; fi
  sleep 2
done
[ -n "$AC3R" ] && track PASS AC-003-rev "reinicio: ONLINE + alerta offline resuelta" \
              || track FAIL AC-003-rev "alerta offline sigue abierta o agente no volvió"

# --- paso 8 (AC-004): consumo excesivo → alerta configurable ---------------------
say "paso 8: AC-004 alerta por consumo (thresholds CPU vía API + busy-loop en la app)"
AID="$(runc -sS -b /c/cookies http://backend:8080/api/v1/agents 2>/dev/null \
      | sed -n 's/.*"id":\([0-9]\{1,\}\),"name":"e2e-app".*/\1/p')"
[ -n "${AID:-}" ] || AID=1
TH_CODE="$(runc -sS -X PUT -b /c/cookies -H 'Content-Type: application/json' \
      --data '{"thresholds_cpu":10,"thresholds_ram":50}' -o /dev/null -w '%{http_code}' \
      "http://backend:8080/api/v1/agents/$AID/thresholds" || :)"
[ "$TH_CODE" = "200" ] || track FAIL AC-004 "PUT thresholds → HTTP $TH_CODE"
# Ruta elegida (documentada): el explorer mide REAL; para un consumo BREACH
# determinístico se usa un busy-loop CPU (dd no aporta % ~estable); con
# thresholds_cpu=10 el próximo beat >10% dispara high_cpu (eval por ingest).
docker exec -d "$APP" sh -c 'timeout 90 sh -c "while :; do :; done"' 2>/dev/null \
  || docker exec -d "$APP" sh -c 'while :; do :; done'
AC4=""
T0="$(now)"
for _ in $(seq 1 15); do
  AC4="$(runc -sS -b /c/cookies http://backend:8080/api/v1/alerts 2>/dev/null \
        | grep -o '"type":"high_cpu"' | head -1 || :)"
  [ -n "$AC4" ] && break
  sleep 2
done
DT=$(( $(now) - T0 ))
[ -n "$AC4" ] && track PASS AC-004 "high_cpu disparada en ${DT}s con thresholds_cpu=10" \
             || track FAIL AC-004 "sin high_cpu en ${DT}s (host puede estar en idle alto)"
# resolve manual via /resolve — PERO primero cesa el consumo: docker restart
# reinicia el contenedor (muere el busy-loop, el explorer revive con su shell
# normal y el CPU vuelve a ~0; el backend dedupe no re-dispara por inanición).
# OJO: puede haber MÁS de una high_cpu activa — un beat con CPU alto previo al
# restart disparó un DUPLICADO (eval por ingest, dedupe con cooldown). Se
# resuelven TODAS en loop acotado hasta que el scope active quede sin high_cpu.
docker restart "$APP" >/dev/null
sleep 5
RESOLVED_N=0
for _ in $(seq 1 5); do
  PEND="$(runc -sS -b /c/cookies http://backend:8080/api/v1/alerts 2>/dev/null \
        | grep -o '"type":"high_cpu"' | head -1 || :)"
  [ -n "$PEND" ] || break
  ALID="$(runc -sS -b /c/cookies http://backend:8080/api/v1/alerts 2>/dev/null \
        | sed -n 's/.*"id":\([0-9]\{1,\}\),"agent_id".*/\1/p')"
  [ -n "${ALID:-}" ] || break
  RES_CODE="$(runc -sS -X POST -b /c/cookies -o /dev/null -w '%{http_code}' \
        "http://backend:8080/api/v1/alerts/$ALID/resolve" || :)"
  say "  resolve de la alerta $ALID → HTTP $RES_CODE"
  RESOLVED_N=$((RESOLVED_N + 1))
done
RES="$(runc -sS -b /c/cookies http://backend:8080/api/v1/alerts 2>/dev/null \
      | grep -o '"type":"high_cpu"' | head -1 || :)"
[ -z "$RES" ] && track PASS AC-004-resolved "resolve manual cierra la(s) high_cpu ($RESOLVED_N alerta(s); scope=active vacío)" \
              || track FAIL AC-004-resolved "alerta high_cpu sigue activa tras resolve"

# --- paso 9 (AC-005): admin crea viewer; viewer NO administra --------------------
say "paso 9: AC-005 roles: admin crea viewer; viewer no puede crear tokens"
U_CODE="$(runc -sS -X POST -b /c/cookies -H 'Content-Type: application/json' \
      --data "{\"username\":\"$VIEWER_USER\",\"password\":\"$VIEWER_PASS\",\"role\":\"viewer\"}" \
      -w '%{http_code}' -o /dev/null http://backend:8080/api/v1/users || :)"
[ "$U_CODE" = "201" ] && track PASS AC-005 "admin creó usuario viewer (201)" \
                     || track FAIL AC-005 "create viewer → HTTP $U_CODE"
runc -sS -X POST -H 'Content-Type: application/json' \
    --data "{\"username\":\"$VIEWER_USER\",\"password\":\"$VIEWER_PASS\"}" \
    -c /c/cookies2 -o /dev/null http://backend:8080/api/v1/auth/login || :
VW_CODE="$(runc -sS -X POST -b /c/cookies2 -H 'Content-Type: application/json' \
    --data '{"name":"should-fail"}' -w '%{http_code}' -o /dev/null \
    http://backend:8080/api/v1/tokens || :)"
[ "$VW_CODE" = "403" ] && track PASS AC-005 "viewer NO puede POST /tokens (403)" \
                      || track FAIL AC-005 "viewer POST /tokens → HTTP $VW_CODE"
VU_CODE="$(runc -sS -b /c/cookies2 -w '%{http_code}' -o /dev/null \
    http://backend:8080/api/v1/users || :)"
[ "$VU_CODE" = "403" ] && track PASS AC-005 "viewer NO puede GET /users (403)" \
                      || track FAIL AC-005 "viewer GET /users → HTTP $VU_CODE"

# --- paso 10 (AC-006): tema Treasure Planet + SSE vía el proxy nginx -------------
say "paso 10: AC-006 frontend prerenderizado + SSE (headers) vía nginx"
FE="$(runc -sS -o /c/index.html -w '%{http_code}|%{content_type}' http://frontend:80/ || :)"
[ "$FE" = "200|text/html" ] && track PASS AC-006 "GET / → 200 text/html (SPA prerenderizada)" \
                            || track FAIL AC-006 "GET / → $FE"
docker run --rm rls-captain-frontend sh -c 'grep -rq "070708" /usr/share/nginx/html' \
  && track PASS AC-006 "assets contienen fondo #070708 (paleta Treasure Planet)" \
  || track FAIL AC-006 "no se encontró #070708 en los assets servidos"
SSE="$(runc -sS --max-time 5 -N -b /c/cookies -D - -o /dev/null \
      http://frontend:80/api/v1/stream 2>/dev/null || :)"
say "  headers SSE (cookie con domain=frontend, jar dedicado):"
runc -sS -X POST -H 'Content-Type: application/json' \
    --data "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" \
    -c /c/cookiesFE -o /dev/null http://frontend:80/api/v1/auth/login || :
SSE="$(runc -sS --max-time 5 -N -b /c/cookiesFE -D - -o /dev/null \
      http://frontend:80/api/v1/stream 2>/dev/null || :)"
printf '%s\n' "$SSE" | head -8 | sed 's/^/    /'
echo "$SSE" | grep -i '^content-type: text/event-stream' >/dev/null 2>&1 \
  && track PASS AC-006 "SSE /api/v1/stream vía nginx → text/event-stream" \
  || track FAIL AC-006 "SSE headers: $(printf '%s' "$SSE" | head -3 | tr '\n' ' ')"

# --- resumen -------------------------------------------------------------------
# Cleanup EXPLÍCITO (y chequeo de host limpio DESPUÉS, no antes del down):
cleanup
sleep 1
# Chequeo de host limpio scoped al e2e: SOLO sus recursos (rls-e2e-* + app).
# NO cuenta stacks ajenos del usuario (rls-demo-*, rls-legacy-*,
# rls-example-demo-app) — no son cosa del e2e ni de este script borrarlos.
LEFT="$(docker ps -a --format '{{.Names}}' | grep -E "^${PROJ}-|^${APP}$" || :)"
if [ -n "$LEFT" ]; then
  say "contenedores e2e restantes: $LEFT"
fi
NETLEFT="$(docker network ls --format '{{.Name}}' | grep '^rls-e2e' || :)"
say "verificación post-cleanup: $([ -z "$LEFT" ] && echo 'sin contenedores del e2e' || echo 'QUEDARON contenedores del e2e') $([ "$NETLEFT" = "" ] && echo '(sin red rls-e2e))' || echo '(QUEDÓ red rls-e2e)')"

if [ "$FAILED" -eq 0 ] && [ -z "$LEFT" ]; then
  say "RESULTADO FINAL: TODOS LOS AC PASS + host limpio"
  exit 0
fi
say "RESULTADO FINAL: $FAILED check(s) en FALLA (ver líneas AC arriba)"
exit 1
