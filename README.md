<p align="center">
  <img src="assets/rls-legacy-logo.svg" width="420" alt="RLS Legacy" />
</p>

<h1 align="center">RLS Legacy</h1>
<p align="center"><strong>Un dashboard para ver los contenedores Docker de tus VPS desde un solo lugar.</strong></p>
<p align="center"><a href="./LICENSE"><img alt="Licencia MIT" src="https://img.shields.io/badge/license-MIT-gold.svg"></a></p>

Captain es el dashboard que instalas en tu servidor de control. En cada VPS externa que quieras monitorear instalas RLS Stowaway, un servicio `systemd` que consulta el Docker Engine de esa VPS y envía sus datos a Captain.

No hace falta instalar un agente dentro de cada contenedor, modificar los Compose de tus aplicaciones ni desplegar Prometheus en cada VPS.

## Qué necesitas

- Un servidor Linux para Captain con Docker Engine y Docker Compose v2.
- Un dominio con HTTPS si vas a conectar VPS externas.
- Cada VPS monitoreada debe usar Linux, `systemd` y Docker Engine. Se admiten `amd64` y `arm64`.

## Instalar Captain con Docker Compose

### 1. Preparar la configuración

En el servidor que será tu centro de control, instala Docker, clona el repositorio y crea el archivo de configuración:

```bash
git clone https://github.com/SrDinoTito/RLS-Legacy.git
cd RLS-Legacy/captain
cp compose.env.example .env
```

Genera una clave para las sesiones:

```bash
openssl rand -hex 32
```

Edita `.env` (`nano .env`) y completa como mínimo:

```env
RLS_JWT_SECRET=PEGA_AQUI_LA_CLAVE_GENERADA
RLS_PUBLIC_URL=https://rls.tudominio.com
```

`RLS_PUBLIC_URL` debe ser la dirección del dashboard que puedan alcanzar las VPS. Para una prueba en la misma computadora puedes usar `http://localhost:3008`; **no uses localhost para VPS externas**.

### 2. Iniciar el dashboard

```bash
docker compose pull
docker compose up -d
docker compose ps
```

Abre `https://rls.tudominio.com` o, si estás probando localmente, `http://localhost:3008`. No hay credenciales predeterminadas: en el primer acceso, crea la cuenta administradora.

Captain escucha en el puerto `3008`. La base SQLite queda en `captain/data/rls.db` en instalaciones Docker Compose normales y sobrevive a las actualizaciones de los contenedores.

### Instalación en Dokploy

Usa el archivo [`captain/compose.dokploy.yaml`](captain/compose.dokploy.yaml). En las variables del servicio configura `RLS_JWT_SECRET` y `RLS_PUBLIC_URL` con tu clave y dominio HTTPS. Agrega el dominio al servicio **frontend** con puerto interno **3008**; no publiques el backend en Internet.

El Compose de Dokploy usa el tag `latest` y solicita la imagen al hacer deploy. Un release nuevo no reinicia por sí solo un contenedor ya activo: después de publicar una versión, vuelve a desplegar el proyecto para actualizarlo.

## Conectar una VPS

Repite estos pasos en cada VPS externa:

1. En Captain, abre **Tokens** y crea un token para esa VPS.
2. Copia el comando de instalación que muestra el dashboard.
3. Ejecútalo en una terminal del **host Linux** que ejecuta Docker, no dentro de una aplicación.
4. El instalador pide el token de forma oculta, instala `rls-stowaway` como servicio `systemd` y lo inicia.

El token se crea y administra desde el dashboard. Usa uno por VPS. Stowaway consulta el Engine local y reporta los contenedores que encuentra; no requiere cambios en sus archivos Compose.

**Seguridad:** el acceso al socket Docker del host equivale a acceso root. Instala Stowaway solo en servidores que administras y protege Captain con HTTPS cuando sea accesible desde Internet.

## Uso

- **Tablero:** revisa el estado y las métricas reportadas por las VPS.
- **VPS:** consulta los contenedores de un servidor y sus preferencias de avisos.
- **Etiquetas:** clasifica contenedores y filtra el tablero por etiqueta.
- **Ignorar:** un contenedor ignorado sigue visible, pero deja de generar métricas y alertas nuevas. Puedes volver a monitorearlo desde su fila.
- **Historial y alertas:** abre un contenedor para revisar sus eventos, alertas y configuración.
- **Notificaciones:** configura Discord o Telegram con las variables opcionales de `.env`; los avisos de consumo se pueden habilitar por separado.

Para revisar o controlar Stowaway en una VPS:

```bash
sudo systemctl status rls-stowaway
sudo journalctl -u rls-stowaway -f
sudo systemctl stop rls-stowaway
sudo systemctl enable --now rls-stowaway
sudo systemctl disable --now rls-stowaway
```

## Actualizar o volver a una versión anterior

Para instalar el release más reciente publicado:

```bash
./update.sh
```

El Compose usa `latest` por defecto; el script descarga las imágenes y actualiza los servicios sin borrar la base de datos ni `.env`. Para fijar o recuperar una versión concreta, cambia `latest` por el mismo tag en las dos referencias `image:` (por ejemplo, `v1.0.1`) y ejecuta `docker compose pull && docker compose up -d`.

## Notificaciones

Los avisos externos son opcionales. Para usar Discord completa `RLS_NOTIF_DISCORD_WEBHOOK_URL` en `.env`; para Telegram configura `RLS_NOTIF_TELEGRAM_BOT_TOKEN` y `RLS_NOTIF_TELEGRAM_CHAT_ID`. Después aplica los cambios:

```bash
docker compose up -d
```

Las alertas siguen visibles en el panel aunque los avisos externos estén silenciados.

## Ayuda y diagnóstico

Si el dashboard no muestra una VPS, comprueba primero el servicio y los logs del host remoto; después revisa los logs de Captain:

```bash
# En la VPS monitoreada
sudo systemctl status rls-stowaway
sudo journalctl -u rls-stowaway -n 100 --no-pager

# En el servidor de Captain
docker compose ps
docker compose logs --tail=100 backend frontend
```

Confirma también que `RLS_PUBLIC_URL` sea correcto y accesible desde la VPS. Para informar un problema, abre un issue e incluye los pasos para reproducirlo y los logs relevantes, quitando antes tokens, webhooks y otros datos privados.

## Contribuir

Los cambios son bienvenidos. Antes de enviar un pull request, ejecuta las comprobaciones correspondientes:

```bash
# Backend
cd captain/backend
go test ./...

# Frontend (Bun)
cd ../frontend
bun run check
bun run build
```

No incluyas archivos `.env`, tokens, webhooks, bases SQLite ni backups.

## Licencia

RLS Legacy se distribuye bajo la licencia [MIT](./LICENSE).
