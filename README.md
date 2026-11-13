<p align="center">
  <img src="assets/rls-legacy-logo.svg" width="420" alt="RLS Legacy" />
</p>

<h1 align="center">RLS Legacy</h1>
<p align="center"><strong>Observabilidad centralizada para VPS y contenedores Docker.</strong></p>
<p align="center"><a href="./LICENSE"><img alt="Licencia MIT" src="https://img.shields.io/badge/license-MIT-gold.svg"></a></p>

RLS Legacy reúne en Captain el estado y las métricas disponibles de contenedores en varias VPS. En cada host remoto, RLS Stowaway consulta el Docker Engine local y reporta los datos a Captain. No requiere agentes dentro de cada contenedor ni cambios en los Compose de las aplicaciones.

## Componentes

- **Captain**: dashboard, API, alertas e historial. Se despliega en el servidor de control.
- **RLS Stowaway**: servicio `systemd` instalado en cada VPS que se quiera supervisar; usa un token exclusivo por VPS.
- **Explorer**: agente legacy para instalaciones existentes. No forma parte del flujo recomendado para nuevas VPS.

El acceso de Stowaway al socket Docker del host equivale a privilegios de root. Instálalo solo en hosts administrados por ti y no expongas el socket por red.

## Despliegue en Dokploy

Requisitos: un proyecto/environment en Dokploy, acceso a GHCR y un dominio para Captain. Para VPS externas, el dominio debe ser accesible desde ellas y servir HTTPS.

1. Crea un servicio **Compose** y usa [`captain/compose.dokploy.yaml`](captain/compose.dokploy.yaml) como definición. Puedes pegar el archivo completo en el editor de Compose o incorporarlo al repositorio conectado a Dokploy.
2. Define estas dos variables requeridas en el environment del proyecto:

   | Variable | Valor |
   | --- | --- |
   | `RLS_JWT_SECRET` | Secreto aleatorio persistente para firmar sesiones. Por ejemplo, `openssl rand -hex 32`. |
   | `RLS_PUBLIC_URL` | URL pública base de Captain, por ejemplo `https://rls.example.com`. |

3. Despliega el Compose. Configura el dominio de Dokploy sobre **frontend**, puerto interno **3008**. El backend escucha en `8070` dentro de la red privada del proyecto y no necesita un dominio público.
4. Abre el dominio y completa el bootstrap inicial para crear la cuenta administradora. No hay credenciales predeterminadas.

El Compose usa las imágenes públicas de GHCR con tag `latest`, persiste SQLite en el volumen `rls-data` e incluye healthchecks, límites de recursos y rotación de logs. Para actualizar, vuelve a desplegar el proyecto en Dokploy para que descargue las imágenes actuales.

### Ejemplo Compose

El archivo [`captain/compose.dokploy.yaml`](captain/compose.dokploy.yaml) contiene la configuración completa de producción. Este fragmento muestra las imágenes, puertos internos y variables relevantes:

```yaml
services:
  backend:
    image: ghcr.io/srdinotito/rls-legacy-backend:latest
    environment:
      RLS_DB_PATH: /data/rls.db
      RLS_PORT: "8070"
      RLS_JWT_SECRET: ${RLS_JWT_SECRET:?required}
      RLS_PUBLIC_URL: ${RLS_PUBLIC_URL:?required}
      RLS_NOTIF_DISCORD_WEBHOOK_URL: ${RLS_NOTIF_DISCORD_WEBHOOK_URL:-}
      RLS_NOTIF_TELEGRAM_BOT_TOKEN: ${RLS_NOTIF_TELEGRAM_BOT_TOKEN:-}
      RLS_NOTIF_TELEGRAM_CHAT_ID: ${RLS_NOTIF_TELEGRAM_CHAT_ID:-}
    volumes:
      - rls-data:/data
    expose:
      - "8070"
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:8070/healthz || exit 1"]
      interval: 30s
      timeout: 5s
      retries: 3

  frontend:
    image: ghcr.io/srdinotito/rls-legacy-frontend:latest
    environment:
      PORT: "3008"
      BACKEND_URL: http://backend:8070
    expose:
      - "3008"
    depends_on:
      backend:
        condition: service_healthy

volumes:
  rls-data:
```

`expose` mantiene los puertos dentro de la red de Compose; el routing público de `frontend:3008` lo configura Dokploy.

### Notificaciones

Discord y Telegram no son requisitos de despliegue. Puedes omitir `RLS_NOTIF_DISCORD_WEBHOOK_URL`, `RLS_NOTIF_TELEGRAM_BOT_TOKEN` y `RLS_NOTIF_TELEGRAM_CHAT_ID` del environment y configurar los canales después desde **Notificaciones** en Captain. Los valores definidos en el environment funcionan como fallback para los campos sin configurar en la aplicación.

## Instalar Captain con Docker Compose

El repositorio incluye [`captain/compose.yaml`](captain/compose.yaml) para desplegar fuera de Dokploy:

```bash
git clone https://github.com/SrDinoTito/RLS-Legacy.git
cd RLS-Legacy/captain
cp compose.env.example .env
```

Configura `RLS_JWT_SECRET` y `RLS_PUBLIC_URL` en `.env`. Para una instancia local, `RLS_PUBLIC_URL=http://localhost:3008`; para conectar hosts remotos, configura una URL pública accesible desde ellos, preferiblemente HTTPS. Luego:

```bash
docker compose pull
docker compose up -d
```

El dashboard se publica en el puerto `3008`. SQLite queda en `captain/data/rls.db` y persiste al actualizar los contenedores.

## Conectar una VPS

Desde Captain, crea un token para la VPS y copia el comando de instalación mostrado en la interfaz. Ejecútalo en el host Linux que ejecuta Docker —no dentro de un contenedor—. El instalador configura y activa `rls-stowaway` como servicio `systemd`; no hace falta modificar los Compose de las aplicaciones.

El token identifica una VPS/Docker Engine. Usa uno distinto por host. Para administrar el servicio:

```bash
sudo systemctl status rls-stowaway
sudo journalctl -u rls-stowaway -f
sudo systemctl restart rls-stowaway
```

## Operación

- El tablero, las vistas por VPS y etiqueta muestran el estado y las métricas reportadas.
- Los contenedores ignorados permanecen visibles, pero no generan métricas ni alertas nuevas.
- El historial del contenedor agrupa eventos y alertas; las alertas se consultan también desde la vista global.
- Las notificaciones externas y las preferencias de consumo se administran desde la aplicación.

En despliegues Docker Compose, `captain/update.sh` descarga y actualiza las imágenes sin eliminar `.env` ni los datos. Para fijar una versión, usa el mismo tag en las dos referencias `image:` y vuelve a desplegar.

## Diagnóstico

Si el pull falla con `lookup ghcr.io: i/o timeout`, el host de Docker no pudo resolver el DNS de GHCR. No es un error del Compose ni de credenciales: la conexión no llegó al registry y la imagen que aparece como `No such image` no se descargó. Comprueba desde la VPS:

```bash
getent ahosts ghcr.io
curl -I --connect-timeout 5 https://ghcr.io/v2/
docker pull ghcr.io/srdinotito/rls-legacy-backend:latest
```

La consulta DNS debe devolver direcciones y `curl` debe obtener una respuesta HTTP (el endpoint `/v2/` puede responder `401`). Si expira, revisa el resolver configurado por el proveedor y el acceso saliente UDP/TCP 53; para el pull también debe estar permitido TCP 443. Reintenta el despliegue cuando el host resuelva `ghcr.io`. Si el pull devuelve `denied` o `unauthorized`, es otro problema: verifica que los paquetes de GHCR sean públicos o autentica Docker con un token que tenga `read:packages`.

`Detected: 0 mounts` no equivale a un error de persistencia: el Compose usa el volumen con nombre `rls-data`, no un bind mount de una ruta del host. Docker lo conserva al recrear los servicios. El `--build` de Dokploy tampoco construye estas imágenes, ya que los servicios declaran `image:` y no `build:`; se descargan del registry.

## Desarrollo

```bash
# Backend
cd captain/backend
go test ./...

# Frontend
cd ../frontend
bun install --frozen-lockfile
bun run check
bun test
bun run build
```

No incluyas `.env`, tokens, webhooks, bases SQLite ni backups en commits o reportes.

## Licencia

RLS Legacy se distribuye bajo la licencia [MIT](./LICENSE).
