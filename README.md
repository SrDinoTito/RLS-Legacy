<p align="center">
  <img src="assets/rls-legacy-logo.svg" width="420" alt="RLS Legacy" />
</p>

<h1 align="center">RLS Legacy</h1>
<p align="center"><strong>Consola central de observabilidad para tus VPS y contenedores Docker.</strong></p>
<p align="center">
  <a href="./LICENSE"><img alt="Licencia MIT" src="https://img.shields.io/badge/license-MIT-gold.svg"></a>
  <img alt="Backend Go" src="https://img.shields.io/badge/backend-Go-00ADD8?logo=go&logoColor=white">
  <img alt="Frontend SvelteKit + Bun" src="https://img.shields.io/badge/frontend-SvelteKit%20%2B%20Bun-fbf0df?logo=bun&logoColor=black">
  <img alt="Docker Compose" src="https://img.shields.io/badge/deploy-Docker%20Compose-2496ED?logo=docker&logoColor=white">
</p>

RLS Legacy centraliza en un solo dashboard los contenedores Docker de varias VPS. **Captain** se instala una vez en tu VPS de control y aloja el panel, la API, las alertas y el historial. En cada VPS externa que quieras observar, **RLS Stowaway** se instala como servicio del host: consulta el Docker Engine local y envía a Captain el estado y las métricas disponibles de sus contenedores por HTTPS.

No hace falta instalar Prometheus, exporters ni agentes dentro de los contenedores, y tampoco hay que editar los Compose de las aplicaciones. Captain no necesita Stowaway en la VPS de control; instálalo allí solo si también quieres monitorear sus contenedores.

> Stowaway observa; no reinicia, modifica ni administra contenedores. El acceso al socket Docker del host equivale a privilegios de root.

### Enlaces rápidos

- [Primeros pasos](#primeros-pasos)
- [Conectar una VPS](#conectar-una-vps-docker)
- [Alertas y notificaciones](#notificaciones-discord-y-telegram)
- [Contribuir](#contribuir)
- [Licencia](#licencia)

---

## Componentes

- **`captain/`** — Consola central que se despliega una vez en la VPS de control:
  dashboard, backend Go, API, alertas e historial. Frontend Bun/SvelteKit con
  tema **Treasure Planet**; Compose en `captain/compose.yaml`.
- **`RLS Stowaway`** — Servicio Linux/systemd instalado en cada VPS externa que
  quieras supervisar. Usa un token exclusivo por VPS, consulta el Docker Engine
  local y reporta sus contenedores en ejecución sin modificar sus Compose.
- **`explorer/`** — Agente POSIX sh por contenedor, conservado únicamente para
  instalaciones antiguas y migración. No es un segundo método para nuevas VPS.

## Arquitectura

```text
┌ VPS externa A ─────────────────┐       HTTPS saliente
│ Stowaway → Docker Engine       │────────────────────────┐
│              └→ contenedores   │                        │
└────────────────────────────────┘                        ▼
┌ VPS externa B ─────────────────┐              ┌ VPS de control ──────────┐
│ Stowaway → Docker Engine       │─────────────▶│ Captain: dashboard, API, │
│              └→ contenedores   │ HTTPS        │ alertas, historial/DB   │
└────────────────────────────────┘              └───────────┬─────────────┘
                                                             │ HTTPS / SSE
                                                         navegador
```

Las VPS externas inician conexiones HTTPS salientes hacia Captain; no se
expone el Docker Engine por la red ni se necesita acceso remoto al socket.

El bridge descubre los contenedores **en ejecución** del host; no necesitas
instalar nada dentro de cada contenedor ni editar Compose. Cada token queda
fijado al ID estable del Docker Engine de una VPS en su primer heartbeat. Los
tokens anteriores sin ese alcance se mantienen solo para compatibilidad/migración.

Por cada contenedor en ejecución, Stowaway reporta estado, CPU, RAM, tráfico
de red (RX/TX), uptime e identidad/etiquetas de Docker. Si uno deja de reportar,
Captain conserva el registro y puede generar una alerta offline. Los
contenedores detenidos que ya habían reportado se muestran como offline; los
que nunca llegaron a reportar no aparecen en el inventario. La versión actual
no envía métricas de disco por contenedor ni CPU/RAM del host; esos datos solo
pueden aparecer para instalaciones legacy que ya los reportaban.

- Backend: auth por cookie JWT (`/api/v1/auth/bootstrap` + `/login`), tokens de
  agente Bearer (hash SHA-256 para validar y plaintext persistente según
  DEC-011), heartbeat con ingest,
  detección `offline` por silencio (ticker), thresholds `high_cpu`/`high_ram`
  por agente, alertas con resolve manual/auto, SSE en vivo y job de retención.
- Frontend: Login / Bootstrap / Dashboard (tarjetas + sparkline) / Detalle con
  gráficas / Alertas / Admin (usuarios, tokens, umbrales). SSE con fallback a
  polling.
- El frontend usa rutas **relativas** (`/api/...`): funciona en CUALQUIER
  dominio detrás de tu reverse proxy TLS sin recompilar (REQ-031).

## Notificaciones: Discord y Telegram

Las alertas (y solo las alertas) pueden avisarte fuera del dashboard: se
notifica `alert_new` cuando se dispara **offline**, **high_cpu** o
**high_ram**, y su `alert_resolved` correspondiente cuando el agente vuelve
a reportar (o un admin la resuelve). Los heartbeats **nunca** se notifican.

Configuración por variables de entorno (sin UI): edita `captain/.env` y
rellena uno o ambos canales — si no configuras ninguno, el notifier queda
inerte. Después prueba el canal sin esperar una alerta real.

Los mensajes vienen **enriquecidos** con el contexto de la alerta: cada
notificación incluye el nombre del agente y la causa (offline / CPU / RAM
sobre umbral), sus **tags de ecosistema**, **hostname + container_id**,
la **última muestra** conocida (CPU, RAM usada/límite, uptime del proceso
formateado — `2h 21m`, `23d 4h`), el **intervalo** de reporte y la
**hora relativa** del evento. Si un dato no está disponible (agente dado
de baja, sin muestras), esos campos simplemente se omiten.

Opcionalmente, con `RLS_PUBLIC_URL` seteada (ej. tu dominio TLS o
`http://localhost:3008`), el embed de Discord y el mensaje de Telegram
añaden un **link directo al panel** (`/agents/{id}`) para ver el detalle
del agente que disparó la alerta sin pasar por el resumen.

### Discord (webhook)

1. En tu servidor de Discord: **Ajustes del servidor → Integraciones →
   Webhooks → Nuevo webhook** → elige el canal destino → **Copiar URL del
   webhook**.
2. Pega la URL completa en `RLS_NOTIF_DISCORD_WEBHOOK_URL` del `.env`.

Ejemplo del `.env`:

```bash
RLS_NOTIF_DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/123456/abcdef
```

### Telegram (bot + chat)

1. Habla con **@BotFather** en Telegram → `/newbot` → sigue el asistente →
   copia el **token** (`123456789:AA...`).
2. **Invita el bot a tu chat/grupo o mándale un mensaje directo**: los bots
   no pueden escribirte sin ese primer contacto.
3. Obtén el `chat_id`: envía un mensaje al chat del bot y abre
   `https://api.telegram.org/bot<TOKEN>/getUpdates` → copia
   `result[0].message.chat.id`.
4. Rellena **ambas** variables (si falta una, Telegram queda deshabilitado).

Ejemplo del `.env`:

```bash
RLS_NOTIF_TELEGRAM_BOT_TOKEN=123456789:AAH-un-token-de-ejemplo
RLS_NOTIF_TELEGRAM_CHAT_ID=-100200300

# publica la URL del panel para que las alertas lleven link directo:
RLS_PUBLIC_URL=https://TU-DOMINIO        # o http://localhost:3008
```

### Probar el canal sin una alerta real

Endpoint admin con tu sesión de cookie (login de la UI o curl):

```bash
# 1) login y guarda la cookie
curl -c cookies.txt -X POST https://TU-DOMINIO/api/v1/auth/login \
     -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"..."}'

# 2) ping al canal configurado
curl -b cookies.txt -X POST https://TU-DOMINIO/api/v1/notify/test \
     -H 'Content-Type: application/json' -d '{"channel":"discord"}'
# → 201 {"status":"ok","channel":"discord"}   (envía "RLS test ok")
# → 503 {"error":"channel discord is not configured ..."}
# → 502 {"error":"notify test failed: HTTP 401: ..."}  (webhook/mal token)
```

(cambia `"channel":"telegram"` para el otro canal). Los reintentos ante
errores de red / 5xx son automáticos (x3, backoff 1s/2s/4s); un 4xx se
loguea una vez como configuración errónea y no se reintenta.

## Primeros pasos

### Requisitos

- Docker Engine con el plugin Docker Compose para alojar Captain.
- En un despliegue público, DNS apuntando al servidor y HTTPS configurado en el reverse proxy.
- Para cada VPS observada: Linux con systemd, Docker Engine local y arquitectura `amd64` o `arm64`.

El dominio debe llegar al frontend de Captain (puerto interno `80`; el Compose
local publica `3008`). El reverse proxy debe conservar el `Host` original y
`X-Forwarded-Proto`, además de permitir `/script.sh` y `/downloads/`.

### Desplegar Captain

Ejecuta estos pasos **una sola vez en la VPS de control**. Esa VPS aloja el
dashboard central al que se conectarán las VPS externas.

```bash
# 1) variables (copia el ejemplo y rellena)
cd captain
cp compose.env.example .env
# genera el secreto OBLIGATORIO en producción (si falta, las sesiones se
# invalidan en cada reinicio y el backend lo loguea como warning):
openssl rand -hex 32        # (o: docker run --rm alpine sh -c 'head -c32 /dev/urandom | od -An -tx1 | tr -d " \n"')
$EDITOR .env                # pegalo en RLS_JWT_SECRET

# 2) levanta el stack (imágenes multi-stage construidas en Docker)
docker compose up -d --build

# 3) abre tu dashboard
open http://localhost:3008      # o tu dominio tras el proxy TLS
```

En producción configura `RLS_PUBLIC_URL=https://TU-DOMINIO` en `captain/.env`
si quieres que los avisos de Discord/Telegram enlacen directamente al panel.

Primer arranque en la UI:

1. La app detecta que **no hay usuarios** → página `/bootstrap`: crea el primer
   admin (queda logueado automáticamente).
2. En **Tokens**, crea un token exclusivo para esa VPS. Según DEC-011, el valor
   `rlst_…` queda copiable desde la UI; también se guarda en texto plano en
   SQLite, además de su hash. Protege la base de datos y todos sus backups como
   credenciales: restringe el acceso al host, usa HTTPS en el proxy y revoca
   el token inmediatamente si sospechas una exposición.
3. Crea más usuarios con rol `admin` o `viewer` si quieres (los `viewer` solo
   leen). Configura por agente los umbrales (CPU 10–100 %, RAM 50–100 %).

### Conectar una VPS Docker

Cada VPS externa necesita **un token propio**. Ese mismo token representa la VPS y
autoriza a Stowaway a reportar todos los contenedores de su Docker Engine; no
crees un token por contenedor y no reutilices el token en otra VPS.

Instala Stowaway en el **host Linux de cada VPS externa**, no en sus contenedores.
La VPS de control que ejecuta Captain no necesita Stowaway para recibir los
datos de las demás; instalarlo allí es opcional si también quieres observar ese
servidor.

1. En **Tokens**, crea un token con un nombre que identifique la VPS (por
   ejemplo `dokploy-produccion`) y, si quieres, tags comunes para sus
   contenedores.
2. Abre el token, copia el comando de instalación y ejecútalo en una terminal
   interactiva del host Linux que corre Docker:

   ```bash
   curl -fsSL https://TU-DOMINIO/script.sh | sudo sh
   ```

El instalador solicita el token sin mostrarlo, confirma el riesgo del Docker
socket, verifica SHA-256, instala el binario adecuado (amd64/arm64) y habilita
`rls-stowaway.service` en systemd. No ingreses el token en la URL ni como
argumento del comando.

El servicio consulta Docker localmente y solo envía métricas por HTTPS a
Captain. **El acceso al Docker socket equivale a privilegios root del host**;
instálalo solo en servidores confiables. No recolecta logs, variables de
entorno ni secretos, y no ejecuta acciones Docker. Para detenerlo temporalmente:

```bash
sudo systemctl stop rls-stowaway
```

Para deshabilitarlo y detenerlo incluso en el siguiente arranque:

```bash
sudo systemctl disable --now rls-stowaway
```

Para volver a activarlo y arrancarlo:

```bash
sudo systemctl enable --now rls-stowaway
```

Ver estado y logs:

```bash
sudo systemctl status rls-stowaway
sudo journalctl -u rls-stowaway -f
```

Al detener el bridge, los contenedores dejan de emitir heartbeats y Captain los
marca OFFLINE. Una VPS por token impide mezclar inventarios o recibir reportes
de otro Docker Engine bajo la misma identidad.

Variables mínimas: `RLS_DASHBOARD_URL` (tu dominio), `RLS_TOKEN` (el
`rlst_…` creado en la UI), opcional `RLS_INTERVAL` (5–300 s) y
`RLS_HOSTNAME` (prefijo para identificar el host; por defecto usa el hostname
del sistema). Estos valores se guardan en
`/etc/rls-stowaway/bridge.env`; protégelo y no copies el token en tickets o
logs.

### Identidad, exclusión de contenedores y compatibilidad

Stowaway obtiene el ID estable del Docker Engine directamente del servicio
Docker local. El primer heartbeat fija el token a ese ID; el mismo Engine puede
reportar muchos contenedores con nombres e historiales independientes. Un
token no puede enlazarse a otro Engine y un Engine no puede tener dos tokens
activos de VPS. Para migrar, crea un token nuevo por VPS e instala Stowaway;
los tokens antiguos Explorer/enrolamiento se aceptan como legado hasta que se
revoquen, pero no son el flujo de alta nuevo.

En la página de una VPS puedes **ignorar** un contenedor individual. Esto
detiene sus alertas/contabilización operativa y resuelve alertas abiertas, sin
desconectar la VPS ni afectar sus otros contenedores; se puede volver a incluir
desde la misma pantalla. Ignorar no detiene ni elimina el contenedor en Docker.

## VPS y etiquetas por contenedor

La sección **VPS** agrupa por el ID estable del Docker Engine que reporta
Stowaway (usa hostname como nombre legible). Al abrir un servidor ves su inventario, el estado y
las alertas; desde ahí un admin puede asignar hasta cinco etiquetas manuales
por contenedor. Estas se guardan aparte de los tags de token: editar una no
modifica las otras ni requiere cambiar el Compose o volver a enrolar agentes.
Ambos tipos aparecen en los filtros del tablero y en los perfiles `/tags/{tag}`.

Las etiquetas manuales respetan el formato existente (`a-z`, `0-9`, `-`, `_`,
hasta 24 caracteres). Incluso si dos VPS tienen el mismo hostname, sus IDs de
Engine distintos las mantienen separadas.

## Por qué es ligero (DEC-006)

| Pieza | Recursos |
|---|---|
| Imagen `rls-captain-backend` | alpine + binario estático: **~30 MB** (objetivo <35) |
| Imagen `rls-captain-frontend` | alpine + nginx + assets: **~18 MB** |
| Límites compose | backend `mem_limit: 128m`/`cpus: 0.5`, frontend `64m`/`0.25` |
| Logs | `json-file` con `max-size: 5m`, `max-file: 2` en **todos** los servicios |
| Retención (REQ-040) | `RLS_RETENTION_DAYS=7` default; pruner borra métricas y alertas resueltas viejas cada hora → `captain/data/rls.db` no crece de más |
| SQLite | WAL, pure-Go, sin cgo |

## e2e automatizado (AC-001..006)

```bash
bash captain/scripts/e2e-compose.sh
```

Levanta el compose real (en proyecto aislado `rls-e2e`), mide tamaños de
imagen y memoria con `docker stats`, corre todos los criterios de aceptación
(bootstrap/login, explorer ONLINE, kill→OFFLINE+alerta, thresholds con
busy-loop, roles viewer, tema+SSE) y limpia TODO (`down -v --remove-orphans`)
al terminar con un trap `EXIT`. Es seguro re-ejecutar. Deja intacto el volumen
`rls-gomodcache`/`rls-bun-cache` y la imagen `rls-explorer`.

**Resultados de la corrida de validación (v1, e2e real):** AC-001..006 PASS — caída detectada en 19s (3×interval + 1 tick de sweep con RLS_INTERVAL=5), high_cpu en 2–5s, imágenes 30.4MB backend / 17.8MB frontend, RAM en runtime 2.3MiB / 11.7MiB, logs 5MB × 2 rotados.

## Troubleshooting

- **Puerto 3008 en uso** → cambia `RLS_PORT` en `.env` (o `export` antes de
  `compose up`) y repite `docker compose up -d`.
- **Warning `RLS_JWT_SECRET` vacío** → sin secreto el backend genera uno
  efímero: cualquier reinicio invalida TODAS las sesiones. Rellena en `.env`.
- **`401 token inválido o revocado` en Stowaway** → el bridge llegó al
  backend pero el token está mal/revocado. Revisa el estado del token en la UI;
  si lo reemplazas, actualiza la configuración local y reinicia el servicio.
- **Stowaway indica que el token pertenece a otra VPS** → cada token nuevo se
  fija al ID del Docker Engine en el primer heartbeat. Usa un token exclusivo
  para esta VPS; no copies tokens entre Engines.
- **No aparece un contenedor** — comprueba que esté running y consulta los logs
  con `journalctl -u rls-stowaway`. Verifica que la VPS asociada sea la esperada
  en `/api/v1/agents`.
- **Base de datos** — vive en la carpeta del host `captain/data/rls.db`
  (bind mount `/data` del backend, TASK-014); borra `captain/data/` si quieres
  un dashboard en blanco. Ver la sección **Datos y backups** de abajo.

## Datos y backups (REQ-060, TASK-014)

La base SQLite **vive en una carpeta visible del host**:
`captain/data/rls.db` (montada como `/data` en el backend, modo WAL). Ya no
hay volumen Docker nombrado — esto hace trivial hacer backups con `sqlite3`.

**Backup** (snapshot consistente ONLINE vía sqlite `.backup`, WAL-safe; el
stack puede seguir corriendo):

```bash
bash captain/scripts/backup.sh            # → captain/backups/rls-YYYYmmdd-HHMMSS.db
bash captain/scripts/backup.sh --dir /otra/ruta
```

Cada snapshot es una DB standalone (no necesita el `-wal`). Retención
keep-last-N con `RLS_BACKUPS_KEEP` (default **7**): cada corrida borra los
snapshots que se salen de ese top-N. La retención de DATOS (métricas y alertas
viejas) es aparte: la maneja el pruner interno del backend con
`RLS_RETENTION_DAYS` (REQ-040) — que tu snapshot tenga 7 días de métricas
topadas ya está garantizado por el backend, no por el backup.

**Restore** (reemplaza `captain/data/rls.db` — pide confirmación explícita
escribiendo `YES`):

```bash
bash captain/scripts/restore.sh captain/backups/rls-20260101-120000.db
```

Pasos que ejecuta: valida el header sqlite del backup → `docker compose stop
backend` → copia el backup sobre `captain/data/rls.db` (removiendo los
`rls.db-wal`/`rls.db-shm` del estado viejo) → `docker compose up -d backend`
→ espera el healthcheck. Ensaya lo que haría SIN tocar nada:

```bash
bash captain/scripts/restore.sh <backup.db> --dry-run
```

**Volumen histórico (pre-migración):** el volumen nombrado
`rls-legacy_rls-data` quedó como snapshot de la DB ANTES de migrar a
`captain/data/`. El compose ya no lo consume. Cuando confirmes que tus backups
en `captain/backups/` funcionan, puedes borrarlo con
`docker volume rm rls-legacy_rls-data`.

## Fuera de alcance v1

Multi-cluster / acciones remotas / móvil quedan fuera de alcance. El bridge sí consulta el socket Docker **local del host**;
no lo expongas por red. TLS del dominio lo resuelve el reverse proxy (Caddy,
Traefik, nginx externo): documentado como integración, no como componente.

## Contribuir

¡Las contribuciones son bienvenidas! Para proponer un cambio:

1. Crea un fork y una rama enfocada (`feat/...`, `fix/...` o `docs/...`).
2. Mantén Stowaway como el flujo recomendado para nuevas VPS; Explorer queda
   solo para compatibilidad legacy. No montes `docker.sock` en los contenedores
   de aplicaciones.
3. Ejecuta las verificaciones apropiadas antes de abrir el pull request:

   ```bash
   # Backend
   cd captain/backend && go test ./... && go vet ./...

   # Frontend (Bun es el único gestor admitido)
   cd ../frontend
   bun install --frozen-lockfile
   bun run check
   bun test
   bun run build
   ```

4. Describe el problema, el cambio y las pruebas realizadas; incluye capturas
   si modifica la UI. Para cambios de arquitectura, documenta la decisión en el
   pull request.

No subas `.env`, tokens, webhooks, bases SQLite ni backups. Usa los archivos
`*.env.example` para documentar configuración sin credenciales.

## Licencia

Este proyecto se distribuye bajo la licencia [MIT](./LICENSE).
