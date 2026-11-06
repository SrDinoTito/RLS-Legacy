#!/bin/sh
# -----------------------------------------------------------------------------
# rls-explorer — agente de observabilidad RLS Legacy (v4 disk, TASK-007)
#
# Corre DENTRO de un contenedor destino (entrypoint wrapper o sidecar) y hace
# POST de un heartbeat con sus PROPIAS métricas (sin docker.sock, DECISION-001):
#
#   POST {RLS_DASHBOARD_URL}/api/v1/agents/heartbeat
#   Authorization: Bearer <RLS_TOKEN>
#   Content-Type:  application/json
#   body: {"hostname":"..","ts":unix_ms,"interval":N,"name"?:..,
#          "container_id"?:..,"labels"?:{...},
#          "metrics":{"cpu_pct":..,"ram_used_mb":..,"ram_limit_mb"?:..,
#                     "rx_mb":..,"tx_mb":..,"uptime_sec":..,
#                     "disk_free_mb"?:..,"disk_total_mb"?..}}   # v4: snapshot df
#
# Resp: 200 {"status":"ok","next_interval":N} | 401 token inválido | 400 body.
# Fuentes: /proc, /proc/net/dev, /sys/fs/cgroup y df (v4: almacenamiento).
#
# v4 disk (almacenamiento): df -k / del filesystem del CONTAINER (contrato:
# filesystem del container, sin flags extras). free/total en MiB con 1
# decimal. Si df falla el bloque simplemente se omite — nunca tumbar el
# contenedor ("falla suave" igual que las demás métricas).
#
# Modos:
#   (default)     loop daemon: un beat cada RLS_INTERVAL s. Ante fallos de
#                 colección/HTTP NUNCA muere: backoff exponencial 1,2,4..60s
#                 (se resetea con un 200; con 401 backoff fijo de 60s). Los
#                 beats fallidos se DROPEAN con contador logueado (buffer
#                 limitado, REQ-004: son métricas, no alertas; sin localStorage).
#   --once        un solo beat; imprime el payload JSON en stderr; exit 0
#                 sólo si el backend respondió 200.
#   --self-test   valida la colección SIN HTTP; JSON a stdout; exit 0 si la
#                 recolección de métricas base funcionó.
#
# POSIX sh portable (dash / busybox ash; sin bashismos). Footprint mínimo.
# -----------------------------------------------------------------------------
set -u
export LC_ALL=C

PROG=rls-explorer

# --- constantes --------------------------------------------------------------
CPU_SAMPLE_SEC=1          # ventana entre snapshots de /proc/stat (0.5..1s)
HTTP_TIMEOUT=10           # timeout por intento HTTP (segundos)
BACKOFF_MAX=60            # tope del backoff exponencial (segundos)
BACKOFF_AUTH=60           # backoff fijo ante 401 (token inválido/revocado)
DROP_DETAIL_LIMIT=10      # REQ-004: buffer lógico de 10 beats perdidos

# --- utilidades --------------------------------------------------------------
log() {
    printf '%s [%s] %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z' 2>/dev/null)" "$PROG" "$*" >&2
}

# 'die' está reservado a fatales ANTES del loop (única salida temprana permitida).
die() {
    log "FATAL: $*"
    exit 1
}

# Escape de una cadena para incrustarla en JSON (sin control chars).
json_escape() {
    printf '%s' "$1" | tr -d '\000-\037' | sed -e 's/\\/\\\\/g' -e 's/"/\\\\"/g'
}

# unix_ms: date +%s%3N si el date lo soporta; si no, epoch*1000.
DATE_MS_OK=0
_ts_probe=$(date +%s%3N 2>/dev/null)
case ${_ts_probe:-} in
    ''|*[!0-9]*) DATE_MS_OK=0 ;;
    ?????????????*) DATE_MS_OK=1 ;;     # >= 13 dígitos => con ms
    *) DATE_MS_OK=0 ;;
esac
unset _ts_probe

now_ts_ms() {
    if [ "$DATE_MS_OK" = 1 ]; then
        date +%s%3N
    else
        printf '%s000' "$(date +%s)"
    fi
}

# --- argumentos ----------------------------------------------------------------
do_once=0
self_test=0
for _arg in "$@"; do
    case $_arg in
        --once)      do_once=1 ;;
        --self-test) self_test=1 ;;
        -h|--help)
            printf 'usage: %s [--once|--self-test]\n' "$PROG" >&2
            exit 0 ;;
        *)
            log "argumento no reconocido: $_arg"
            printf 'usage: %s [--once|--self-test]\n' "$PROG" >&2
            exit 2 ;;
    esac
done

# --- variables de entorno (REQ-001) ----------------------------------------------
# --self-test no hace HTTP: tolera URL/token ausentes para poder validar la
# recolección en cualquier contenedor. En los demás modos son obligatorios.
URL=${RLS_DASHBOARD_URL:-}
TOKEN=${RLS_TOKEN:-}

if [ "$self_test" = 0 ]; then
    [ -n "$URL" ]   || die "RLS_DASHBOARD_URL es obligatorio (no hay a dónde reportar)"
    [ -n "$TOKEN" ] || die "RLS_TOKEN es obligatorio"
    # normaliza: sin trailing slashes
    while :; do
        case $URL in */) URL=${URL%/} ;; *) break ;; esac
    done
    case $URL in
        http://*|https://*) ;;
        *) log "AVISO: RLS_DASHBOARD_URL no usa http(s)://; se usará tal cual ($URL)" ;;
    esac

    # cliente HTTP: se detecta UNA vez al arrancar (curl preferido, fallback wget).
    if command -v curl >/dev/null 2>&1; then
        CLIENT=curl
    elif command -v wget >/dev/null 2>&1; then
        CLIENT=wget
        log "AVISO: curl no disponible; usando wget (fallback busybox)"
    else
        die "ni curl ni wget disponibles: sin forma de hacer POST"   # única salida ok
    fi
fi

# RLS_INTERVAL: default 10; sólo dígitos; clamp 5..300.
INTERVAL=10
if [ -n "${RLS_INTERVAL:-}" ]; then
    case $RLS_INTERVAL in
        *[!0-9]*|'') log "AVISO: RLS_INTERVAL='$RLS_INTERVAL' no numérico; uso default 10" ;;
        *) INTERVAL=$RLS_INTERVAL ;;
    esac
fi
if [ "$INTERVAL" -lt 5 ];   then log "AVISO: RLS_INTERVAL <5  => clamp a 5";    INTERVAL=5;   fi
if [ "$INTERVAL" -gt 300 ]; then log "AVISO: RLS_INTERVAL >300 => clamp a 300"; INTERVAL=300; fi

NAME=
if [ -n "${RLS_NAME:-}" ]; then
    NAME=$(json_escape "$RLS_NAME")
fi

# labels (opcional): objeto JSON crudo vía RLS_LABELS_JSON, p.ej.
#   RLS_LABELS_JSON='{"app":"nginx","stack":"prod"}'
LABELS=
if [ -n "${RLS_LABELS_JSON:-}" ]; then
    trimmed=$(printf '%s' "$RLS_LABELS_JSON" | tr -d '\000-\037\015\012' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
    case $trimmed in
        '{'*'}'*) LABELS=$trimmed ;;
        *) log "AVISO: RLS_LABELS_JSON ignorado (no parece objeto JSON)" ;;
    esac
fi

# ============================ COLECCIÓN =========================================

# --- hostname ------------------------------------------------------------------
read_hostname() {
    HOSTNAME_VAL=
    HN=$(hostname 2>/dev/null)
    [ -n "${HN:-}" ] || HN=$(tr -d '\000-\037' < /etc/hostname 2>/dev/null)
    HN=$(printf '%s' "${HN:-}" | tr -d '\000-\037' | awk 'NR==1{print $1; exit}')
    [ -n "$HN" ] || return 1
    HOSTNAME_VAL=$(json_escape "$HN")
    return 0
}

# --- CPU vía /proc/stat ----------------------------------------------------------
# snapshot del agregador "cpu" => "<total> <idle+iowait>".
_cpu_snap() {
    awk '/^cpu /{print $2+$3+$4+$5+$6+$7+$8, $5+$6; exit}' /proc/stat 2>/dev/null
}

collect_cpu() {
    CPU_VAL=
    snapA=$(_cpu_snap)
    [ -n "$snapA" ] || return 1
    sleep "$CPU_SAMPLE_SEC"
    snapB=$(_cpu_snap)
    [ -n "$snapB" ] || return 1
    CPU_VAL=$(awk -v a="$snapA" -v b="$snapB" 'BEGIN{
        split(a, x); split(b, y);
        dt = y[1]-x[1]; di = y[2]-x[2];
        if (dt <= 0) exit 3;
        v = 100 * (1 - di/dt);
        if (v < 0) v = 0; if (v > 100) v = 100;
        printf "%.1f", v
    }')
    case ${CPU_VAL:-} in
        ''|*[!0-9.]*) return 1 ;;
    esac
    return 0
}

# --- RAM por cgroup v2 (fallback v1) ---------------------------------------------
collect_ram() {
    RAM_USED_VAL=
    RAM_LIMIT_VAL=
    # cgroups v2 (unificado)
    if [ -r /sys/fs/cgroup/memory.current ]; then
        R_cur=$(awk '{print $1}' /sys/fs/cgroup/memory.current 2>/dev/null)
        R_max=$(awk '{print $1}' /sys/fs/cgroup/memory.max 2>/dev/null)
        case ${R_cur:-} in
            ''|*[!0-9]*) : ;;
            *)
                RAM_USED_VAL=$(awk -v b="$R_cur" 'BEGIN{printf "%.2f", b/1048576}')
                case ${R_max:-} in
                    ''|*[!0-9]*|max|MAX) : ;;    # "max" (unlimited) => omitir límite
                    *) [ "$R_max" -gt 0 ] && RAM_LIMIT_VAL=$(awk -v b="$R_max" 'BEGIN{printf "%.2f", b/1048576}') ;;
                esac
                return 0
                ;;
        esac
    fi
    # cgroups v1 (jerarquía única)
    if [ -r /sys/fs/cgroup/memory/memory.usage_in_bytes ]; then
        R_cur=$(awk '{print $1}' /sys/fs/cgroup/memory/memory.usage_in_bytes 2>/dev/null)
        R_max=$(awk '{print $1}' /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null)
        case ${R_cur:-} in
            ''|*[!0-9]*) return 1 ;;
            *)
                RAM_USED_VAL=$(awk -v b="$R_cur" 'BEGIN{printf "%.2f", b/1048576}')
                # v1 reporta ~2^63 cuando no hay límite: > 2^40 => omitir.
                case ${R_max:-} in
                    ''|*[!0-9]*) : ;;
                    *) if [ "$R_max" -gt 0 ] && [ "$R_max" -lt 1099511627776 ]; then
                           RAM_LIMIT_VAL=$(awk -v b="$R_max" 'BEGIN{printf "%.2f", b/1048576}')
                       fi ;;
                esac
                return 0
                ;;
        esac
    fi
    return 1
}

# --- red vía /proc/net/dev ---------------------------------------------------------
# Suma de interfaces NO loopback; "rx_mb"/"tx_mb" son acumulados desde el
# arranque del script (delta por beat sobre una base inicial).
_net_totals() {
    awk 'NR>2 {
        sub(/^[ \t]+/, "");
        p = index($0, ":"); if (p == 0) next;
        dev = substr($0, 1, p-1);
        if (dev == "lo" || dev == "") next;
        rest = substr($0, p+1); sub(/^[ \t]+/, "", rest);
        n = split(rest, f, "[ \t]+");
        if (n < 9) next;              # bytes_rx packets..(9 campos) bytes_tx
        rx += f[1]; tx += f[9];
    } END { printf "%.0f %.0f\n", rx+0, tx+0 }' /proc/net/dev 2>/dev/null
}

_NET_BASE_RX=
_NET_BASE_TX=

collect_net() {
    NET_RX_VAL=
    NET_TX_VAL=
    totals=$(_net_totals)
    [ -n "$totals" ] || return 1
    set -- $totals
    cur_rx=$1
    cur_tx=$2
    case ${cur_rx:-}${cur_tx:-} in ''|*[!0-9]*) return 1 ;; esac

    if [ -z "${_NET_BASE_RX:-}" ]; then
        # primer snapshot: corre al ARRANCAR el script; los beats posteriores
        # reportan el delta acumulado respecto de esta base.
        _NET_BASE_RX=$cur_rx
        _NET_BASE_TX=$cur_tx
        NET_RX_VAL="0.000"
        NET_TX_VAL="0.000"
        return 0
    fi
    # contadores reseteados (interface recreada) => re-baseline silencioso.
    [ "$cur_rx" -lt "$_NET_BASE_RX" ] && _NET_BASE_RX=$cur_rx
    [ "$cur_tx" -lt "$_NET_BASE_TX" ] && _NET_BASE_TX=$cur_tx
    d_rx=$((cur_rx - _NET_BASE_RX))
    d_tx=$((cur_tx - _NET_BASE_TX))
    _NET_BASE_RX=$cur_rx
    _NET_BASE_TX=$cur_tx
    # 3 decimales: beats chicos (~KB/s) no se aplastan a 0.00 (0.001 MB = 1 KB)
    NET_RX_VAL=$(awk -v b="$d_rx" 'BEGIN{printf "%.3f", b/1048576}')
    NET_TX_VAL=$(awk -v b="$d_tx" 'BEGIN{printf "%.3f", b/1048576}')
    return 0
}

# --- container_id (best effort; se omite si no se puede determinar) --------------
detect_container_id() {
    CONTAINER_ID_VAL=
    ID=
    # cgroups v1: línea con .../docker/<64hex>
    ID=$(grep docker /proc/self/cgroup 2>/dev/null | grep -o '[0-9a-f]\{64\}' | head -n 1)
    if [ -z "$ID" ]; then
        # mountinfo: .../docker/containers/<64hex>/...  (cgroupns privado)
        ID=$(grep 'containers' /proc/self/mountinfo 2>/dev/null | grep -o 'containers/[0-9a-f]\{64\}' | head -n 1 | grep -o '[0-9a-f]\{64\}')
    fi
    case ${ID:-} in
        *[!0-9a-f]*|'') return 1 ;;
        *) CONTAINER_ID_VAL=$ID; return 0 ;;
    esac
}

# --- uptime ------------------------------------------------------------------------
# uptime_sec = uptime del CONTENEDOR (PID 1): now - (btime + starttime/HZ).
#   - btime     = campo "btime" de /proc/stat  (epoch del boot del kernel)
#   - starttime = campo 22 de /proc/1/stat     (ticks desde boot). Se lee
#     saltando el comm "(...)" (puede contener espacios/paréntesis): tras el
#     último ")" restan los campos 3.., donde starttime es el 20º token.
#   - HZ = getconf CLK_TCK (si no hay getconf, default 100).
# Fallback (comportamiento viejo): /proc/uptime — en kernels sin lxcfs puede
# reflejar el uptime del HOST, por eso queda como último recurso con AVISO
# único logueado una sola vez.
_CLK_TCK=100
if command -v getconf >/dev/null 2>&1; then
    _hz=$(getconf CLK_TCK 2>/dev/null)
    case ${_hz:-} in
        ''|*[!0-9]*|0) : ;;    # valor raro => mantener default
        *) _CLK_TCK=$_hz ;;
    esac
    unset _hz
fi
_UPTIME_WARNED=0

collect_uptime() {
    UPTIME_VAL=
    St=$(awk '{sub(/.*\)/, ""); print $20; exit}' /proc/1/stat 2>/dev/null)
    Bt=$(awk '$1=="btime"{print $2; exit}' /proc/stat 2>/dev/null)
    Nw=$(date +%s 2>/dev/null)
    # cada campo se valida aparte (uno vacío no debe "colarse" por el vecino)
    case ${Bt:-} in *[!0-9]*|'') Bt= ;; esac
    case ${Nw:-} in *[!0-9]*|'') Nw= ;; esac
    if [ -n "$Bt" ] && [ -n "$Nw" ]; then
        case ${St:-} in
            *[!0-9]*|'') : ;;
            *)
                U=$((Nw - Bt - St / _CLK_TCK))    # negativo => 0
                [ "$U" -lt 0 ] && U=0
                UPTIME_VAL=$U
                return 0
                ;;
        esac
    fi
    # Fallback: /proc/uptime (posible uptime del HOST sin lxcfs) — AVISO una vez.
    U=$(awk 'NR==1{printf "%.0f", $1+0; exit}' /proc/uptime 2>/dev/null)
    case ${U:-} in
        *[!0-9]*|'') return 1 ;;
        *)
            if [ "$U" -ge 0 ]; then
                if [ "$_UPTIME_WARNED" = 0 ]; then
                    log "AVISO: uptime via /proc/uptime (fallback): /proc/1/stat o btime inaccesibles; puede reflejar el uptime del HOST"
                    _UPTIME_WARNED=1
                fi
                UPTIME_VAL=$U
                return 0
            fi
            return 1
            ;;
    esac
}

# --- disco (v4): snapshot df del filesystem raíz del container -----------------
# df -k /: total (1K-blocks) y disponible (Available) en KiB → MiB con 1
# decimal (base 1024, coherente con ram_*). Una sola fila (la del mount "/"):
# primera fila real debajo del header. Sin flags extras: df simple.
collect_disk() {
    DISK_FREE_VAL=
    DISK_TOTAL_VAL=
    line=$(df -k / 2>/dev/null | awk 'NR>1 {print $2, $4; exit}')
    [ -n "$line" ] || return 1
    set -- $line
    total_kb=$1
    avail_kb=$2
    case ${total_kb:-} in ''|*[!0-9]*|0) return 1 ;; esac
    case ${avail_kb:-} in ''|*[!0-9]*) return 1 ;; esac
    DISK_FREE_VAL=$(awk -v b="$avail_kb" 'BEGIN{printf "%.1f", b/1024}')
    DISK_TOTAL_VAL=$(awk -v b="$total_kb" 'BEGIN{printf "%.1f", b/1024}')
    return 0
}

# --- colección agregada --------------------------------------------------------------
# "Falla suave": cada métrica individual puede fallar (se omite del beat y se
# loguea a stderr); el beat se manda igual mientras haya hostname.
collect_metrics() {
    read_hostname || return 1
    TS_VAL=$(now_ts_ms)
    CPU_VAL=
    RAM_USED_VAL=
    RAM_LIMIT_VAL=
    NET_RX_VAL=
    NET_TX_VAL=
    UPTIME_VAL=
    DISK_FREE_VAL=
    DISK_TOTAL_VAL=
    CONTAINER_ID_VAL=
    collect_cpu     || log "fallo colectando CPU (/proc/stat); beat sin cpu_pct"
    collect_ram     || log "fallo colectando RAM (cgroups); beat sin ram_*"
    collect_net     || log "fallo colectando red (/proc/net/dev); beat sin rx_mb/tx_mb"
    collect_uptime  || log "fallo colectando uptime; beat sin uptime_sec"
    collect_disk    || log "fallo colectando disco (df); beat sin disk_*"
    detect_container_id || :    # best-effort: puede omitirse silenciosamente
    return 0
}

# --- payload JSON -----------------------------------------------------------------------
# Campos numéricos con awk ya son JSON válidos; strings van escapados. Los
# campos indisponibles simplemente se omiten (backend tolera métricas parciales).
build_payload() {
    p="{\"hostname\":\"$HOSTNAME_VAL\",\"ts\":$TS_VAL,\"interval\":$INTERVAL"
    if [ -n "$NAME" ]; then p="$p,\"name\":\"$NAME\""; fi
    if [ -n "${CONTAINER_ID_VAL:-}" ]; then p="$p,\"container_id\":\"$CONTAINER_ID_VAL\""; fi
    if [ -n "$LABELS" ]; then p="$p,\"labels\":$LABELS"; fi
    p="$p,\"metrics\":{"
    sep=
    if [ -n "${CPU_VAL:-}" ];       then p="$p${sep}\"cpu_pct\":$CPU_VAL";            sep=,;  fi
    if [ -n "${RAM_USED_VAL:-}" ];  then p="$p${sep}\"ram_used_mb\":$RAM_USED_VAL";   sep=,;  fi
    if [ -n "${RAM_LIMIT_VAL:-}" ]; then p="$p${sep}\"ram_limit_mb\":$RAM_LIMIT_VAL";  sep=,;  fi
    if [ -n "${NET_RX_VAL:-}" ];    then p="$p${sep}\"rx_mb\":$NET_RX_VAL";           sep=,;  fi
    if [ -n "${NET_TX_VAL:-}" ];    then p="$p${sep}\"tx_mb\":$NET_TX_VAL";           sep=,;  fi
    if [ -n "${UPTIME_VAL:-}" ];    then p="$p${sep}\"uptime_sec\":$UPTIME_VAL";      sep=,;  fi
    # v4 disk: snapshot df del FS del container (opcional; df puede fallar)
    if [ -n "${DISK_FREE_VAL:-}" ];  then p="$p${sep}\"disk_free_mb\":$DISK_FREE_VAL";  sep=,;  fi
    if [ -n "${DISK_TOTAL_VAL:-}" ]; then p="$p${sep}\"disk_total_mb\":$DISK_TOTAL_VAL"; sep=,;  fi
    p="$p}}"
    PAYLOAD=$p
}

# --- POST -----------------------------------------------------------------------------
# Setaea HTTP_CODE (000 = fallo de red/transporte) y RESP_BODY (respuesta 2xx).
ERR_FILE=/tmp/.rls-explorer.$$.err
post_heartbeat() {
    HTTP_CODE=000
    RESP_BODY=
    : > "$ERR_FILE" 2>/dev/null || ERR_FILE=/dev/null
    rc=0
    if [ "$CLIENT" = curl ]; then
        raw=$(curl -sS -m "$HTTP_TIMEOUT" \
              -H "Content-Type: application/json" \
              -H "Authorization: Bearer $TOKEN" \
              --data-binary "$1" \
              -w '\n%{http_code}' \
              "$URL/api/v1/agents/heartbeat" 2>"$ERR_FILE") || rc=$?
        code=$(printf '%s' "${raw:-}" | awk 'END{print $NF}')
        case ${code:-} in
            [1-5][0-9][0-9])
                RESP_BODY=$(printf '%s' "${raw:-}" | sed '$d')
                HTTP_CODE=$code
                ;;
            *) : ;;    # fallo de red/timeout: HTTP_CODE queda 000
        esac
    else
        # wget fallback (busybox): --post-data + --header; -O - vuelca el body.
        body=$(wget -q -T "$HTTP_TIMEOUT" \
               --header="Content-Type: application/json" \
               --header="Authorization: Bearer $TOKEN" \
               --post-data="$1" \
               -O - "$URL/api/v1/agents/heartbeat" 2>"$ERR_FILE") || rc=$?
        if [ "$rc" = 0 ]; then
            HTTP_CODE=200
            RESP_BODY=$body
        else
            code=$(sed -n 's/.*HTTP\/[0-9.]* \([0-9][0-9][0-9]\).*/\1/p' "$ERR_FILE" | head -n 1)
            case ${code:-} in
                [1-5][0-9][0-9]) HTTP_CODE=$code ;;
                *) HTTP_CODE=000 ;;    # error de red/DNS
            esac
        fi
    fi
}

# --- un beat ----------------------------------------------------------------------------
# Devuelve 0 si hubo 200 (seteando SLEEP_AFTER para el siguiente ciclo).
do_beat() {
    if ! collect_metrics; then
        log "no se pudo determinar hostname; beat no enviado"
        return 1
    fi
    build_payload
    post_heartbeat "$PAYLOAD"
    case $HTTP_CODE in
        200)
            SLEEP_AFTER=$INTERVAL
            # adopta next_interval del backend si es válido (5..300)
            ni=$(printf '%s' "$RESP_BODY" | sed -n 's/.*"next_interval"[ \t]*:[ \t]*\([0-9][0-9]*\).*/\1/p' | head -n 1)
            case ${ni:-} in
                *[!0-9]*|'') : ;;
                *) if [ "$ni" -ge 5 ] && [ "$ni" -le 300 ]; then SLEEP_AFTER=$ni; fi ;;
            esac
            return 0
            ;;
        401)
            log "401: token inválido o revocado (revisar RLS_TOKEN); backoff fijo ${BACKOFF_AUTH}s"
            return 1
            ;;
        400)
            log "400: heartbeat rechazado (body malformado/inválido)"
            return 1
            ;;
        *)
            # 000 (red/DNS/timeout) u otro status: el loop aplica backoff.
            return 1
            ;;
    esac
}

# --- modos ------------------------------------------------------------------------------
if [ "$self_test" = 1 ]; then
    # Colección sin HTTP: JSON a stdout; exit 0 sólo si la base (hostname) OK.
    if ! collect_metrics; then
        echo "{\"error\":\"no se pudo iniciar la coleccion (sin hostname)\"}" >&2
        exit 1
    fi
    build_payload
    printf '%s\n' "$PAYLOAD"
    exit 0
fi

if [ "$do_once" = 1 ]; then
    # Un solo snapshot + POST; payload visible en stderr para inspección.
    if ! collect_metrics; then
        log "coleccion fallida (sin hostname)"
        exit 1
    fi
    build_payload
    log "payload: $PAYLOAD"
    post_heartbeat "$PAYLOAD"
    if [ "$HTTP_CODE" = 200 ]; then
        log "heartbeat ok (200): $RESP_BODY"
        exit 0
    fi
    if [ "$HTTP_CODE" = 000 ]; then
        log "fallo de red/transporte: $(head -n 1 "$ERR_FILE" 2>/dev/null)"
    elif [ "$HTTP_CODE" = 401 ]; then
        log "401: token inválido o revocado (revisar RLS_TOKEN)"
    else
        log "HTTP $HTTP_CODE del backend"
    fi
    exit 1
fi

# --- modo daemon --------------------------------------------------------------------------
# Baseline de red ANTES del primer beat: rx/tx de cada beat son deltas desde aquí.
collect_net >/dev/null 2>&1 || :
SLEEP_AFTER=$INTERVAL
dropped_run=0      # beats fallidos consecutivos (racha)
dropped_total=0    # total de beats dropeados desde el arranque
backoff=1
log "daemon iniciado: interval=${INTERVAL}s client=${CLIENT:-none} url=$URL (CON-007 del loop: nunca muere)"

# Vivir SIEMPRE: ninguna rama de fallo hace exit; TERM/INT salen con 0 (stop limpio).
trap 'log "señal recibida; saliendo limpio"; exit 0' TERM INT

while :; do
    sleep_now=$SLEEP_AFTER
    if do_beat; then
        dropped_run=0
        backoff=1
        sleep_now=$SLEEP_AFTER
    else
        dropped_run=$((dropped_run + 1))
        dropped_total=$((dropped_total + 1))
        if [ "$dropped_run" -le "$DROP_DETAIL_LIMIT" ]; then
            log "beat dropeado #$dropped_total (racha $dropped_run, HTTP $HTTP_CODE): $(head -n 1 "$ERR_FILE" 2>/dev/null)"
        elif [ $((dropped_run % 10)) -eq 0 ]; then
            # buffer lógico lleno: resumen cada 10 beats; sigue vivo igual.
            log "buffer lleno: racha de $dropped_run fallidos; $dropped_total beats dropeados en total desde arranque"
        fi
        if [ "$HTTP_CODE" = 401 ]; then
            sleep_now=$BACKOFF_AUTH      # token inválido/revocado: 60s fijo
            backoff=1
        else
            backoff=$((backoff * 2))
            [ "$backoff" -gt "$BACKOFF_MAX" ] && backoff=$BACKOFF_MAX
            sleep_now=$backoff
        fi
    fi
    sleep "$sleep_now" 2>/dev/null || sleep 1
done
