#!/bin/sh
set -eu

cd "$(dirname "$0")"

echo "Descargando las imágenes indicadas en compose.yaml (latest sigue el release más reciente)..."
docker compose pull
echo "Aplicando la actualización sin tocar los datos de SQLite..."
docker compose up -d
echo "Actualización terminada. La base sigue persistida en ./data/rls.db."
