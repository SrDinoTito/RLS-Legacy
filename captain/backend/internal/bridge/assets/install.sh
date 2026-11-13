#!/bin/sh
# RLS Stowaway installer. Served by Captain at /script.sh.
set -eu

SERVICE_NAME=rls-stowaway
BIN_PATH=/usr/local/bin/rls-stowaway
CONFIG_DIR=/etc/rls-stowaway
CONFIG_FILE=$CONFIG_DIR/bridge.env
UNIT_FILE=/etc/systemd/system/rls-stowaway.service
DEFAULT_DASHBOARD_URL='@@DASHBOARD_URL@@'
DEFAULT_TOKEN='@@TOKEN@@'

fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }

if [ "$(uname -s 2>/dev/null || true)" != Linux ]; then
	fail 'Stowaway requiere un servidor Linux con systemd.'
fi
if [ "$(id -u)" -ne 0 ]; then
	fail 'Ejecuta el instalador como root: curl -fsSL https://tu-dominio/script.sh | sudo sh'
fi
command -v systemctl >/dev/null 2>&1 || fail 'No se encontró systemctl; este host no parece usar systemd.'
[ -S /var/run/docker.sock ] || fail 'No se encontró /var/run/docker.sock. Revisa que Docker Engine esté instalado y activo.'
[ -r /var/run/docker.sock ] || fail 'El socket Docker no es accesible para root.'

case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	aarch64|arm64) ARCH=arm64 ;;
	*) fail "Arquitectura no soportada: $(uname -m). Se requiere amd64 o arm64." ;;
esac

[ -r /dev/tty ] || fail 'Se necesita una terminal interactiva. Descarga script.sh y ejecútalo desde una consola.'

say 'RLS Stowaway — instalador del bridge Docker'
say 'El bridge lee el Docker Engine local para descubrir y medir contenedores.'
say 'ADVERTENCIA: acceso al Docker socket equivale a privilegios root sobre el servidor.'
printf '¿Confirmas instalar este servicio privilegiado? [s/N] ' > /dev/tty
IFS= read -r CONFIRM < /dev/tty || fail 'No se recibió confirmación.'
case "$CONFIRM" in s|S|si|SI|sí|Sí|y|Y|yes|YES) ;; *) fail 'Instalación cancelada.' ;; esac

DASHBOARD_URL=${RLS_DASHBOARD_URL:-$DEFAULT_DASHBOARD_URL}
if [ -z "$DASHBOARD_URL" ]; then
	printf 'URL pública del Dashboard (https://...): ' > /dev/tty
	IFS= read -r DASHBOARD_URL < /dev/tty || fail 'No se recibió la URL.'
fi
DASHBOARD_URL=${DASHBOARD_URL%/}
case "$DASHBOARD_URL" in
	https://*) ;;
	http://localhost*|http://127.0.0.1*|http://\[::1\]*) ;;
	*) fail 'Usa HTTPS para el Dashboard. HTTP solo se permite en localhost.' ;;
esac
case "$DASHBOARD_URL" in *[!A-Za-z0-9._~:/%-]*) fail 'La URL contiene caracteres no permitidos.' ;; esac

TOKEN=${RLS_TOKEN:-$DEFAULT_TOKEN}
if [ -z "$TOKEN" ]; then
	printf 'Token de enrolamiento (la entrada no se muestra): ' > /dev/tty
	if command -v stty >/dev/null 2>&1; then stty -echo < /dev/tty; fi
	IFS= read -r TOKEN < /dev/tty || TOKEN=
	if command -v stty >/dev/null 2>&1; then stty echo < /dev/tty; fi
	printf '\n' > /dev/tty
fi
case "$TOKEN" in rlst_*) ;; *) fail 'Se requiere un token de enrolamiento RLS válido (prefijo rlst_).' ;; esac
case "$TOKEN" in *[!A-Za-z0-9_-]*) fail 'El token contiene caracteres no permitidos.' ;; esac

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM
BASE_URL=$DASHBOARD_URL
ARTIFACT_URL=$BASE_URL/downloads/rls-stowaway-linux/$ARCH
say "Descargando Stowaway para Linux/$ARCH desde el Dashboard…"
command -v curl >/dev/null 2>&1 || fail 'Instala curl para descargar el binario.'
AUTH_CONFIG=$TMP_DIR/curl-auth.conf
printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" > "$AUTH_CONFIG"
chmod 0600 "$AUTH_CONFIG"
fetch() { curl --fail --location --silent --show-error --config "$AUTH_CONFIG" "$1"; }
fetch "$ARTIFACT_URL" > "$TMP_DIR/rls-stowaway" || fail 'No se pudo descargar el binario.'
EXPECTED=$(fetch "$ARTIFACT_URL.sha256" | awk 'NR == 1 {print $1}') || fail 'No se pudo descargar el checksum.'
case "$EXPECTED" in *[!0-9a-fA-F]*|'') fail 'El checksum recibido no es válido.' ;; esac
[ "${#EXPECTED}" -eq 64 ] || fail 'El checksum recibido tiene una longitud inválida.'
command -v sha256sum >/dev/null 2>&1 || fail 'No se encontró sha256sum para verificar el binario.'
ACTUAL=$(sha256sum "$TMP_DIR/rls-stowaway" | awk '{print $1}')
[ "$EXPECTED" = "$ACTUAL" ] || fail 'El checksum del binario no coincide; no se instalará.'

install -d -o root -g root -m 0700 "$CONFIG_DIR"
install -o root -g root -m 0755 "$TMP_DIR/rls-stowaway" "$BIN_PATH"
umask 077
printf 'RLS_DASHBOARD_URL="%s"\nRLS_TOKEN="%s"\nRLS_INTERVAL="10"\n' \
	"$DASHBOARD_URL" "$TOKEN" > "$CONFIG_FILE"
chown root:root "$CONFIG_FILE"
chmod 0600 "$CONFIG_FILE"

cat > "$UNIT_FILE" <<'UNIT'
[Unit]
Description=RLS Stowaway Docker fleet bridge
Documentation=https://docs.docker.com/engine/security/
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
EnvironmentFile=/etc/rls-stowaway/bridge.env
Environment=RLS_DOCKER_SOCKET=/var/run/docker.sock
ExecStart=/usr/local/bin/rls-stowaway
Restart=always
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectHome=true
ProtectSystem=strict
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
CapabilityBoundingSet=
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
MemoryDenyWriteExecute=true

[Install]
WantedBy=multi-user.target
UNIT
chown root:root "$UNIT_FILE"
chmod 0644 "$UNIT_FILE"

systemctl daemon-reload
systemctl enable --now "$SERVICE_NAME.service"
say ''
say 'Stowaway quedó instalado y habilitado al iniciar el servidor.'
say "Estado:   systemctl status $SERVICE_NAME"
say "Logs:     journalctl -u $SERVICE_NAME -f"
say "Detener:  systemctl stop $SERVICE_NAME"
say "Deshabilitar y detener: systemctl disable --now $SERVICE_NAME"
say "Reactivar: systemctl enable --now $SERVICE_NAME"
say 'Al deshabilitarlo, los contenedores dejan de enviar señales y RLS los marcará OFFLINE.'
