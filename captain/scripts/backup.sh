#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# backup.sh — Backup ONLINE consistente de la DB de captain (REQ-060, TASK-014).
#
# Snapshot sqlite ".backup" (WAL-safe, consistente aunque el backend esté
# corriendo) + retención keep-last-N (RLS_BACKUPS_KEEP, default 7).
#
# TODO el trabajo pesado corre EN DOCKER (CON-007): este script solo orquesta
# `docker run` alpine:3.20 (sqlite incluido via apk). No requiere sqlite3,
# curl ni nada más en el host — solo bash + docker.
#
# Uso:
#   bash captain/scripts/backup.sh                 # → captain/backups/
#   bash captain/scripts/backup.sh --dir /ruta     # destino custom de backups
#
# Salida: <backups>/rls-YYYYmmdd-HHMMSS.db (snapshot standalone de rls.db)
# -----------------------------------------------------------------------------
set -euo pipefail

# Git Bash / MSYS convierte argumentos tipo /c/... en rutas Windows y rompe los
# -v del docker run. Desactivar la conversión (harmless en Linux/WSL).
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DATA_DIR="$SCRIPT_DIR/../data"
BACKUP_DIR="$SCRIPT_DIR/../backups"                 # default: captain/backups/

# --- args: [--dir path] ------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --dir)
      [ $# -ge 2 ] || { echo "ERROR: --dir necesita un path" >&2; exit 2; }
      BACKUP_DIR="$2"; shift 2 ;;
    --dir=*) BACKUP_DIR="${1#--dir=}"; shift ;;
    *) echo "ERROR: argumento desconocido: $1 (uso: $0 [--dir path])" >&2; exit 2 ;;
  esac
done

# --- docker necesita paths en formato Windows bajo Git Bash -------------------
to_win() {
  case "$1" in
    /c/*) echo "C:${1#/c}" ;;
    *)    echo "$1" ;;
  esac
}
DOCKER_DATA_DIR="$(to_win "$DATA_DIR")"
DOCKER_BACKUP_DIR="$(to_win "$BACKUP_DIR")"

# --- validaciones -------------------------------------------------------------
[ -f "$DATA_DIR/rls.db" ] || {
  echo "ERROR: no existe $DATA_DIR/rls.db (¿el stack subió alguna vez? ¿captain/ es el project dir?)" >&2
  exit 1
}
mkdir -p "$BACKUP_DIR" || { echo "ERROR: no pude crear $BACKUP_DIR" >&2; exit 1; }
docker image inspect alpine:3.20 >/dev/null 2>&1 || docker pull -q alpine:3.20 >/dev/null

STAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP_NAME="rls-$STAMP.db"

# --- snapshot consistente ONLINE (sqlite .backup es WAL-safe) -----------------
echo "→ Backup de $DATA_DIR/rls.db → $BACKUP_DIR/$BACKUP_NAME"
docker run --rm \
  -v "$DOCKER_DATA_DIR:/data" \
  -v "$DOCKER_BACKUP_DIR:/backup" \
  -e BACKUP_NAME="$BACKUP_NAME" \
  alpine:3.20 sh -c '
    apk add -q sqlite >/dev/null 2>&1
    sqlite3 /data/rls.db ".backup /backup/$BACKUP_NAME"
  '

# --- verificación: existe y size > 0 ------------------------------------------
SIZE="$(ls -l "$BACKUP_DIR/$BACKUP_NAME" | awk '{print $5}')"
if [ -z "$SIZE" ] || [ "$SIZE" -le 0 ]; then
  echo "ERROR: el backup no quedó escrito o está vacío: $BACKUP_NAME (size=$SIZE)" >&2
  exit 1
fi
echo "Backup OK: $BACKUP_DIR/$BACKUP_NAME ($SIZE bytes)"

# --- retención keep-last-N (RLS_BACKUPS_KEEP, default 7) -----------------------
KEEP="${RLS_BACKUPS_KEEP:-7}"
if [ "$KEEP" -gt 0 ] 2>/dev/null; then
  ls -1t "$BACKUP_DIR"/rls-*.db 2>/dev/null | tail -n +"$((KEEP + 1))" | while read -r old; do
    echo "Retention: borrando backup viejo $old"
    rm -f "$old"
  done
fi
