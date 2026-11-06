# rls-explorer — inserción en contenedores (RLS Legacy v4 disk)

> **LEGACY / MIGRACIÓN:** este agente sigue disponible para instalaciones
> existentes, pero no debe usarse para dar de alta nuevas VPS. El flujo actual
> es **un `rls-stowaway` y un token único por VPS**, que descubre todos los
> contenedores desde Docker Engine sin editar cada Compose. Los tokens nuevos
> del dashboard son exclusivos de Stowaway y requieren la identidad del Engine;
> los tokens Explorer existentes se mantienen compatibles hasta revocarlos.

El **explorer** es un script POSIX sh (`explorer.sh`) que corre **dentro** de un
contenedor de tu infraestructura y reporta sus **propias** métricas (CPU, RAM,
red, uptime y almacenamiento) por HTTP a tu dashboard RLS Legacy (`captain/`).
No usa `docker.sock` y no puede ver otros contenedores: vigilancia endogámica
por diseño (DECISION-001). La detección de caída es por **silencio**: si deja de
mandar heartbeats, el dashboard lo marca OFFLINE a los `3×interval`.

| Métrica          | Fuente                    | Contracto del payload                       |
|------------------|---------------------------|---------------------------------------------|
| `cpu_pct`        | `/proc/stat` (delta 1s)   | % 0–100, 1 decimal                          |
| `ram_used_mb`    | cgroups v2 (fallback v1)  | MiB, 2 decimales                            |
| `ram_limit_mb`   | idem (`memory.max`)       | MiB; "max" (unlimited) → se omite           |
| `rx_mb`/`tx_mb`  | `/proc/net/dev`           | deltas por beat, 3 decimales                |
| `uptime_sec`     | `/proc/1/stat` + btime    | segundos, uptime del PID 1 del container    |
| `disk_free_mb`   | `df -k /` (v4)            | MiB libres del FS del container, 1 decimal  |
| `disk_total_mb`  | `df -k /` (v4)            | MiB totales del FS del container, 1 decimal |

`disk_*` es un **snapshot** del filesystem `/` del container (sin flags extras
de df); ante cualquier fallo de `df` los campos se omiten del beat y el
dashboard conserva el último valor (null en el Agent = nunca reportado).

## Variables de entorno

| Variable             | Requerida | Default | Descripción |
|----------------------|-----------|---------|-------------|
| `RLS_DASHBOARD_URL`  | sí        | —       | URL base del dashboard (p. ej. `https://mi-dominio.com`) |
| `RLS_TOKEN`          | sí        | —       | Token de agente creado en el dashboard (se muestra una vez) |
| `RLS_INTERVAL`       | no        | `10`    | Segundos entre beats; se ajusta (clamp) a `5..300` |
| `RLS_NAME`           | no        | —       | Nombre legible del agente en la UI |
| `RLS_LABELS_JSON`    | no        | —       | Labels opcionales como objeto JSON: `'{"app":"nginx","stack":"prod"}'` |

Requiere **sh** y **curl o wget** en la imagen destino (las recetas B y C
garantizan curl). Si falta `RLS_DASHBOARD_URL` o `RLS_TOKEN`, el script termina
en el arranque con un error claro. Si no existen curl **ni** wget, también
termina avisando (única condición de salida temprana permitida).

Granularidad de métricas: `rx_mb`/`tx_mb` son **deltas por beat con 3
decimales** (0.001 MB = 1 KB de precisión; los campos del payload siguen
llamándose `*_mb`). La UI del dashboard formatea en KB cuando el valor es
menor a 1 MB, así los beats chicos (~KB/s) no se aplastan a `0.00`.

Modos:

```sh
explorer.sh              # daemon: beat cada RLS_INTERVAL, nunca muere ante fallos
explorer.sh --once       # un solo snapshot + POST (payload visible en stderr)
explorer.sh --self-test  # colección sin HTTP; JSON a stdout; exit 0 si OK
```

Ante fallo de red/HTTP el daemon aplica **backoff exponencial** (1,2,4,…,60s),
se resetea al primer 200; un **401** (token inválido o revocado) usa backoff
fijo de 60s y lo loguea explícitamente. Los beats fallidos se **dropean** con
contador logueado (buffer lógico máximo de 10 beats detallados; son métricas,
no alertas — REQ-004), y el script sigue vivo indefinidamente.

---

## Forma A — Montar el script en el contenedor de tu app (entrypoint wrapper)

Ideal para vigilar un servicio que **ya corre** (nginx, node, postgres…): se
monta el script y se antepone al comando original. **Verbatim**:

```yaml
services:
  app:
    image: nginx:alpine          # tu app, sin cambios de imagen
    volumes:
      - ./explorer/explorer.sh:/usr/local/bin/explorer.sh:ro
    entrypoint: ["/bin/sh","-c"]
    command: ["/usr/local/bin/explorer.sh & exec \"$$0\" \"$@\"","nginx","-g","daemon off;"]
    environment:
      RLS_DASHBOARD_URL: https://mi-dominio.com
      RLS_TOKEN: rlst_xxxxxxxx
      RLS_INTERVAL: "10"
      RLS_NAME: nginx-prod
      RLS_LABELS_JSON: '{"app":"nginx","stack":"prod"}'
    mem_limit: 150m
```

> Compose nota: `$$` escapa el `$` para que el shell del contenedor reciba
> `"$0" "$@"`; en `docker run` directo usarías un solo `$`.

Equivalente con `docker run`:

```sh
docker run -d --name mi-app \
  -v "$PWD/explorer/explorer.sh:/usr/local/bin/explorer.sh:ro" \
  -e RLS_DASHBOARD_URL=https://mi-dominio.com \
  -e RLS_TOKEN=rlst_xxxxxxxx \
  -e RLS_INTERVAL=10 \
  -e RLS_NAME=nginx-prod \
  --entrypoint /bin/sh nginx:alpine \
  -c '/usr/local/bin/explorer.sh & exec "$0" "$@"' nginx -g 'daemon off;'
```

La imagen destino necesita `sh` + `curl` o `wget` (nginx:alpine trae wget de
busybox; funciona, aunque curl es preferible). Si tu imagen no tiene
herramientas HTTP, usa la forma B o C.

## Forma B — Sidecar con esta imagen (no tocas tu app)

Primero construye la imagen del explorer (~21MB sin comprimir, ~7MB en registry):

```sh
docker build -t rls-explorer explorer/
```

Después agrégalo como servicio de tu compose, en la **misma red** que tu app
(si el dashboard es externo, solo necesita salida a internet):

```yaml
services:
  app:
    image: nginx:alpine
    # ... tu app sin ningún cambio ...

  rls-explorer:
    image: rls-explorer          # construida arriba (o tu registry: rls-explorer:1)
    environment:
      RLS_DASHBOARD_URL: https://mi-dominio.com
      RLS_TOKEN: rlst_xxxxxxxx
      RLS_INTERVAL: "10"
      RLS_NAME: sidecar-demo
    mem_limit: 32m
    logging:
      driver: json-file
      options: { max-size: "5m", max-file: "2" }
    restart: unless-stopped
```

Para vigilar la app del compose entero puedes lanzar un sidecar por servicio,
todos apuntando al mismo dashboard con tokens distintos (1 token = 1 agente).

## Forma C — Dockerfile wrapper (hereda tu imagen y añade el script)

Cuando controlas el build de la imagen de la app:

```dockerfile
FROM node:20-alpine
# ... tus capas habituales de la app ...
RUN apk add --no-cache curl
COPY explorer/explorer.sh /usr/local/bin/explorer.sh
RUN chmod +x /usr/local/bin/explorer.sh

# Envolver el CMD original: explorer en background + exec del proceso real
ENV RLS_INTERVAL=10
CMD ["/bin/sh","-c","/usr/local/bin/explorer.sh & exec node server.js"]
```

(el `RLS_DASHBOARD_URL` y `RLS_TOKEN` se inyectan en runtime por env/compose,
nunca dentro de la imagen.)

## ⚠️ Imágenes distroless / sin `sh`

El script es `#!/bin/sh`: **no funciona** en imágenes distroless
(`gcr.io/distroless/*`, `scratch` sin shell, imágenes Go estáticas sin busybox).
Para esos casos usa la **Forma B (sidecar)**: el explorer corre en su propio
contenedor y tu app no cambia nada.

## ❌ docker.sock — explícito fuera de alcance

El explorer **nunca monta `docker.sock`** ni usa la Docker API: solo reporta lo
que ve de sí mismo vía `/proc` + cgroups (v2, fallback v1). La vigilancia de
otros contenedores del host queda fuera del alcance v1 (spec `rls-monitor`,
DECISION-001). No intentes montar el socket: el script no lo lee.

---

## 🧪 Validación realizada (e2e contra backend real, TASK-007)

Validada con `explorer/scripts/e2e-smoke.sh` (solo Docker; el script orquesta
todo solo, ver header del mismo). Resultados reales:

- `checkbashisms` + `sh -n` + `dash -n` → **limpio** (POSIX sin bashismos).
- `--self-test` (límite 64MB) → JSON válido, exit 0.
- `--once` contra el backend Go real → HTTP 200
  `{"next_interval":10,"status":"ok"}`, backend loguea
  `agent registered agent_id=… name=…`; quedan filas en `agents`/`metrics`.
- Loop daemon 25s → sin errores; RAM medida con `docker stats`: **~0.9–1.1 MiB**
  (límite < 20MB, REQ-005).
- Fallback wget (alpine busybox sin curl) → 200 OK vía `--post-data` +
  `--header` de busybox 1.36.
- 401 con token revocado → mensaje explícito "token inválido o revocado" y
  exit 1.
- Resiliencia: con el backend DETENIDO el loop sigue vivo, loguea
  `beat dropeado #N` con backoff exponencial; al revivirlo reintenta con éxito
  y el backend resuelve la alerta offline (`agent back online …
  alerts_resolved=1`).

## FAQ / Troubleshooting

**¿Cómo pruebo la inserción sin esperar el dashboard?**
Desde cualquier contenedor con el script montado:

```sh
/usr/local/bin/explorer.sh --self-test   # JSON de métricas a stdout; no toca la red
/usr/local/bin/explorer.sh --once        # snapshot + POST real; payload en stderr
```

**`401 token inválido o revocado` vs error de red — ¿cómo los distingo?**

- `401: token inválido o revocado (revisar RLS_TOKEN)` → el script **llegó al
  backend** y el token está mal, expiró o fue revocado en la UI. Crea un token
  nuevo y reinicia el contenedor. Backoff fijo de 60s mientras tanto.
- `beat dropeado … (HTTP 000)` → **no llegó**: DNS, red, firewall o URL mal
  escrita (falta `https://`), o el dashboard está caído. Prueba
  `curl -v $RLS_DASHBOARD_URL/healthz` desde dentro del contenedor.
- Los logs van a stderr del contenedor: `docker logs <container>`.

**El dashboard no registra mi agente pero el POST responde 200**
El agente se auto-registra con el primer heartbeat del token (1 token = 1
agente). Verifica en la UI o `GET /api/v1/agents` del dashboard.

**Mi `RLS_INTERVAL` no se respeta**
Se hace clamp a `5..300`; un valor no numérico cae al default 10. El backend
también puede devolver `next_interval`, que el explorer adopta cuando es válido.

**¿Cuánta RAM consume?**
Footprint objetivo < 20MB (REQ-005). El script es stateless: no buferiza beats
fallidos en disco, se dropean con contador logueado.

**host vs container uptime**
`uptime_sec` es el uptime del **PID 1 del contenedor** (tiempo efectivamente
vivo de ese container; sensible a pausas del scheduler del contenedor): se
calcula como `now − (btime + starttime/HZ)` usando `btime` de `/proc/stat` y
el campo 22 de `/proc/1/stat` (`HZ` vía `getconf CLK_TCK`, default 100). Si
`/proc/1/stat` o `btime` no son accesibles, cae como último recurso a
`/proc/uptime` (comportamiento viejo), que en kernels sin lxcfs puede reflejar
el uptime del **host** — en ese caso el script loguea un AVISO una sola vez.
El dashboard lo trata como informativo.

**Clock desfasado**
El payload lleva `ts` (unix ms) pero el dashboard usa `last_seen` de **su** reloj
del server: un desfase del agente no rompe el registro.
