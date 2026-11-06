# rls-legacy — ejemplo de agente (explorer)

Demo plug&play: un contenedor mínimo Alpine que corre `explorer/` y reporta
sus métricas al dashboard (captain) cada `RLS_INTERVAL` segundos — CPU, RAM,
red, uptime y **almacenamiento** (snapshot `df` del FS del container: `disk_free_mb`
/ `disk_total_mb`; la card y el modal del agent lo muestran como
"Almacenamiento", p. ej. "52.3 GB / 74.5 GB").

## Requisitos

- El dashboard ya está corriendo (`captain/docker compose up -d`).
- Docker accesible (Docker Desktop).

## Pasos (plug & play)

1. **Creá un token** en la UI del dashboard (`Admin -> Tokens`); ponele un tag
   si querés etiquetar el agente en el ecosistema (ej. `lab`). Copiá el token
   (`rlst_...`).
2. **Configurá el .env**:

   ```sh
   cp .env.example .env
   # editá .env y pegá el token en RLS_TOKEN
   ```

   > Docker Compose lee el `.env` del directorio del compose (esta carpeta
   > cuando el command usa `-f example/docker-compose.yaml`). También podés
   > pasar el valor por shell: `RLS_TOKEN=rlst_xxx docker compose up -d`.
3. **Levantá el agente**:

   ```sh
   docker compose -f example/docker-compose.yaml -p rls-example up -d
   ```

4. En ~10-12s el agente aparece **ONLINE** en el dashboard con su tag.

> **¿Muchos contenedores? Modo enrolamiento** — token de enrolamiento
> (TASK-013): el compose de este ejemplo usa el modelo clásico (1 token =
> 1 agente). Si vas a monitorear muchos contenedores del mismo stack, creá
> un token de **enrolamiento** en el dashboard (Admin → Tokens → "Tipo de
> token → Enrolamiento (flota)") y reutilizá EL MISMO token en cada
> servicio del compose cambiando SOLO `RLS_NAME` (único por contenedor).
> Cada heartbeat adopta su propio agente con los tags del token heredados
> en el momento de adoptarse; revocar el token da de baja TODA la flota.
> Detalle: editar los tags del token después NO re-sincroniza los agentes
> ya adoptados (los tags quedan fijados al adoptar).

## Ver la alerta de caída

Querés ver cómo se detecta la caída de un contenedor? Matá el agente:

```sh
docker kill rls-example-demo-app-1
```

En ≤30s pasa a **OFFLINE** y salta la alerta (y notificación a Discord o Telegram si las configuraste en el dashboard).

Revivilo:

```sh
docker compose -f example/docker-compose.yaml -p rls-example up -d
```

El agente vuelve a **ONLINE** y conserva el mismo identity (mismo token)
junto con su historial de métricas.

## Cleanup

```sh
docker compose -f example/docker-compose.yaml -p rls-example down
```
