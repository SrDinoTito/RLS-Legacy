<p align="center">
  <img src="assets/rls-legacy-logo.svg" width="420" alt="RLS Legacy" />
</p>

<h1 align="center">RLS Legacy</h1>
<p align="center"><strong>Un panel para consultar el estado de tus VPS y contenedores Docker desde un solo lugar.</strong></p>
<p align="center"><a href="./LICENSE"><img alt="Licencia MIT" src="https://img.shields.io/badge/license-MIT-gold.svg"></a></p>

RLS Legacy tiene dos partes: **Captain**, el dashboard central que instalas en tu VPS de control, y **RLS Stowaway**, un servicio liviano que instalas en cada VPS que quieras monitorear. Stowaway consulta Docker en su propio servidor y envía los datos al dashboard.

No necesitas agregar un agente a cada contenedor ni modificar los archivos Compose de tus aplicaciones.

## Qué puedes hacer

- Ver desde un solo panel las VPS conectadas y sus contenedores.
- Consultar el estado, las métricas disponibles, el historial y las alertas.
- Clasificar contenedores con etiquetas y filtrar el tablero.
- Ignorar un contenedor sin eliminarlo: seguirá visible como ignorado y podrás volver a incluirlo cuando quieras.
- Recibir avisos de alertas en Discord o Telegram.

## Instalación

### 1. Inicia el dashboard

En la VPS que usarás como centro de control, instala Docker y Docker Compose. Clona el repositorio y ejecuta Captain:

```bash
git clone https://github.com/SrDinoTito/RLS-Legacy.git
cd RLS-Legacy/captain
cp compose.env.example .env
```

Configura `RLS_JWT_SECRET` en `captain/.env` con una clave aleatoria. Por ejemplo:

```bash
openssl rand -hex 32
```

Luego inicia el dashboard:

```bash
docker compose up -d --build
```

Abre `http://IP-DE-TU-VPS:3008` o el dominio que hayas configurado. En la primera visita, crea la cuenta administradora.

### 2. Conecta una VPS

Repite estos pasos por cada VPS externa que quieras monitorear:

1. En el dashboard, abre **Tokens** y crea un token con un nombre que te ayude a reconocer esa VPS.
2. Copia desde el propio dashboard el comando de instalación que aparece para ese token. El token se crea y administra desde la interfaz web; no hace falta escribirlo a mano en un Compose.
3. Ejecuta el comando en una terminal del **host Linux** de esa VPS. La instalación configura RLS Stowaway como servicio del sistema y lo inicia.

Instálalo en el servidor que ejecuta Docker, no dentro de las aplicaciones. Un token corresponde a una VPS y permite que aparezcan en el dashboard los contenedores en ejecución de ese servidor.

El acceso de Stowaway al socket Docker del host tiene privilegios equivalentes a root. Instálalo solo en VPS que administras y mantén el dashboard detrás de HTTPS si es accesible desde Internet.

## Uso del dashboard

- **Tablero:** consulta el estado de los contenedores y abre un contenedor para ver sus datos, configuración e historial.
- **VPS:** entra a una VPS para ver sus contenedores, agregar etiquetas o ajustar los avisos de ese servidor.
- **Ignorar un contenedor:** abre su detalle y elige **Ignorar monitoreo**. Seguirá apareciendo en la lista con la marca **Ignorado**, pero dejará de generar nuevas métricas y alertas. Usa **Volver a monitorear** para reactivarlo.
- **Tokens:** crea, copia o revoca el token de cada VPS desde la interfaz web.
- **Notificaciones:** configura Discord o Telegram en el archivo `.env` del dashboard. Las preferencias del dashboard permiten silenciar avisos generales o de consumo por VPS o contenedor.

Para administrar el servicio en una VPS:

```bash
sudo systemctl status rls-stowaway
sudo systemctl stop rls-stowaway
sudo systemctl enable --now rls-stowaway
sudo systemctl disable --now rls-stowaway
```

## Requisitos

- **Captain:** un servidor con Docker Engine y Docker Compose.
- **Stowaway:** VPS Linux con systemd y Docker Engine; arquitectura `amd64` o `arm64`.
- Las VPS externas deben poder conectarse por HTTPS al dominio del dashboard.

## Contribuir

Si encuentras un problema o tienes una mejora, abre un issue o envía un pull request. Para cambios en la aplicación, valida el backend con `go test ./...` y el frontend con `bun test` y `bun run build` desde sus respectivas carpetas.

No incluyas tokens, webhooks, archivos `.env`, bases de datos ni copias de seguridad en tus cambios.

## Licencia

RLS Legacy se distribuye bajo la licencia [MIT](./LICENSE).
