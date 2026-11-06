#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# restore.sh — Restaura el backend desde un backup hecho por backup.sh
# (REQ-060, TASK-014). Con confirmación explícita (escribe YES) y modo In-PLACE:
#   docker compose stop backend
#   reemplazo de captain/data/rls.db por la copia DEL BACKUP (sin perder wal/shm adheridos al db viejo)
#   docker compose up -d backend + healthcheck
#
# TODO corre EN DOCKER (CON-007); solo requiere bash + docker en el host.
#
# Uso:
#   bash captain/scripts/restore.sh <path/to/backup/rls-xxxx.db>             # real
#   bash captain/scripts/restore.sh <path/to/backup/rls-xxxx.db> --dry-run  # solo loguea lo que haría
# -----------------------------------------------------------------------------
set -euo pipefail

# Git Bash / MSYS → ver backup.sh
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DATA_DIR="$SCRIPT_DIR/../data"
COMPOSE_FILE="$SCRIPT_DIR/../compose.yaml"

# --- args ---------------------------------------------------------------------
DRY_RUN=0
BACKUP_FILE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    -*) echo "ERROR: flag desconocido: $1" >&2; exit 2 ;;
    *)
      [ -z "$BACKUP_FILE" ] || { echo "ERROR: solo se permite 1 backup por invocación" >&2; exit 2; }
      BACKUP_FILE="$1"; shift ;;
  esac
done
[ -n "$BACKUP_FILE" ] || {
  echo "ERROR: falta el path del backup." >&2
  echo "Uso: $0 <backups/rls-xxxx.db> [--dry-run]" >&2
  exit 2
}

# resolve a path absoluto (cwd-relative ok)
BACKUP_ABS="$(cd "$(dirname "$BACKUP_FILE")" && pwd)/$(basename "$BACKUP_FILE")"

HEADER_LINE=""
# --- docker quiere paths Windows bajo Git Bash --------------------------------
to_win() {
  case "$1" in
    /c/*) echo "C:${1#/c}" ;;
    *)    echo "$1" ;;
  esac
}
DOCKER_DATA_DIR="$(to_win "$DATA_DIR")"
DOCKER_BACKUP_DIR="$(to_win "$(dirname "$BACKUP_ABS")")"
BACKUP_BASENAME="$(basename "$BACKUP_ABS")"

# --- validaciones ---------------------------------------------------------------
[ -d "$DATA_DIR" ] || { echo "ERROR: no existe el data dir $DATA_DIR" >&2; exit 1; }
[ -f "$BACKUP_ABS" ] || { echo "ERROR: no existe el backup: $BACKUP_ABS" >&2; exit 1; }
[ -f "$COMPOSE_FILE" ] || { echo "ERROR: no existe $COMPOSE_FILE (¿captain/ es el project dir?)" >&2; exit 1; }
[ -f "$DATA_DIR/rls.db" ] || { echo "ERROR: no existe $DATA_DIR/rls.db (¿el stack subió alguna vez?)" >&2; exit 1; }

# --- comando(s) que se van a correr (respetando el patrón dry-run) -------------
header_expected="SQLite format 3"
# -- validar el header del backup (solo lectura, seguro incluso sin confirmar):
HEADER="$(docker run --rm -v "$DOCKER_BACKUP_DIR:/b:ro" alpine:3.20 sh -c "head -c 16 /b/$BACKUP_BASENAME | tr -d '\000'")"
if [ "$HEADER" != "$header_expected" ]; then
  echo "ERROR: este archivo no es una DB sqlite (header '$HEADER' ≠ 'SQLite format 3') — aborto sin tocar nada." >&2
  exit 1
fi

if [ "$DRY_RUN" -eq 1 ]; then
  RESOLVE() { echo "  \$ $*"; }
else
  RESOLVE() { "$@"; }
fi

echo "Esto MODIFICA el estado real del stack:"
echo "  data dir:     $DATA_DIR"
echo "  backup file:  $BACKUP_ABS"
printf '%b' "¿RESTAURAR el backup y PISAR el actual rls.db del backend (stop + replace + up)? Escribi YES: "
if [ "$DRY_RUN" -eq 1 ]; then
  echo "YES (auto por --dry-run)"
  ANSWER="YES"
else
  read -r ANSWER
fi
if [ "$ANSWER" != "YES" ]; then
  echo "Cancelado (no se tocó nada)." >&2
  exit 0
fi

echo "→ 1/4 docker compose stop backend"
RESOLVE docker compose -f "$COMPOSE_FILE" stop backend

echo "→ 2/4 reemplazo rls.db ← backup (y remuevo rls.db-wal / rls.db-shm del estado viejo)"
RESOLVE rm -f "$DATA_DIR/rls.db-wal" "$DATA_DIR/rls.db-shm"
RESOLVE cp "$BACKUP_ABS" "$DATA_DIR/rls.db"

echo "→ 3/4 docker compose up -d backend"
RESOLVE docker compose -f "$COMPOSE_FILE" up -d backend

echo "→ 4/4 healthcheck (hasta healthy o timeout)"
if [ "$DRY_RUN" -eq 1 ]; then
  echo "  \$ (espero healthcheck real — omitido en dry-run)"
else
  CTID="$(docker compose -f "$COMPOSE_FILE" ps -q backend)"
  TIMEOUT=60
  ELAPSED=0
  while :; do
    STATUS="$(docker inspect -f '{{.State.Health.Status}}' "$CTID" 2>/dev/null || echo starting)"
    if [ "$STATUS" = "healthy" ]; then break; fi
    if [ "$ELAPSED" -ge "$TIMEOUT" ]; then
      echo "ERROR: backend no llegó a healthy en ${TIMEOUT}s (status=$STATUS)" >&2
      exit 1
    fi
    sleep 2; ELAPSED=$((ELAPSED + 2))
  done
fi

echo "Restaurado:"
RESOLVE docker compose -f "$COMPOSE_FILE" ps backend | sed -n '1,3p'
