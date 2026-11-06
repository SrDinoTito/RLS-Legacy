# captain/frontend — Dashboard RLS Legacy (TASK-005/006)

SPA SvelteKit 5 + Tailwind v4 + shadcn-svelte (a mano, `#lib/*` alias STRICT),
tema Treasure Planet (DSG paleta: negro espacio, oro `hsl(42 85% 52%)`,
cian holograma `hsl(187 80% 45%)`), adapter-static + fallback `index.html`
(DSG-002), todo fetch por `src/lib/api/client.js` con `credentials:'include'`.

## Desarrollo (CON-007: SIEMPRE bun en Docker)

Volúmenes compartidos: cache de bun (`rls-bun-cache`) para no reinstalar.

```powershell
# dev server (proxy /api → backend en 8080, definido en vite.config.ts)
docker run --rm -p 5173:5173 -v C:\Users\srDino\Proyects\rls-legacy\captain\frontend:/app -v rls-bun-cache:/root/.bun/install/cache -w /app --memory 1g --cpus 1 oven/bun:1.2-alpine bun run dev --host

# type check (0 errores exigidos)
docker run --rm -v C:\Users\srDino\Proyects\rls-legacy\captain\frontend:/app -v rls-bun-cache:/root/.bun/install/cache -w /app --memory 1g --cpus 1 oven/bun:1.2-alpine bunx svelte-check

# build estático → build/ (prerender rutas estáticas; dinámicas por fallback)
docker run --rm -v C:\Users\srDino\Proyects\rls-legacy\captain\frontend:/app -v rls-bun-cache:/root/.bun/install/cache -w /app --memory 1g --cpus 1 oven/bun:1.2-alpine bun run build
```

> Nota: NO correr bun/npm en el host (regla CON-007); `node_modules` es linux.

## Estructura TASK-006

- `src/lib/api/` — `client.js|ts` (fetch único + base override para tests),
  `types.js` (contratos backend TASK-004), `client.test.ts` (bun test e2e).
- `src/lib/stores/` — `auth.svelte.ts` (sesión), `sse.svelte.ts` (stream SSE +
  refetch con debounce + fallback polling >30 s, REQ-023).
- `src/lib/components/` — `AppShell.svelte` (sidebar/topbar, view-transition
  off), `AgentCard.svelte` (cards online/offline + sparkline SVG inline),
  `Sparkline.svelte`, `UplotChart.svelte` (uPlot ~45 KB, huecos nativos) y
  `ui/{badge,dialog,tabs,table}` siguiendo la convención shadcn del repo.
- `src/routes/` — `/` (dashboard), `/agents/[id]` (detalle con uPlot+
  thresholds inline+timeline de alertas), `/alerts` (activas/todas+resolver),
  `/tags/[tag]` (perfil de flota), `/vps` + `/vps/[hostname]` (inventario
  agrupado por host con edición admin de etiquetas individuales) y `/admin`
  (usuarios/tokens; viewer → redirect con toast).
- `src/lib/vps.ts` y `ContainerTagsEditor.svelte` — agrupan por hostname y
  guardan etiquetas independientes del token por contenedor.

## Smoke e2e bun test contra el backend real

El suite `src/lib/api/client.test.ts` ejercita login/me/logout + roles +
token lifecycle con la MISMA capa de cliente de la UI (cookies interceptadas
en fetch global). Se salta solo si el backend no responde en `RLS_TEST_BASE`.

```powershell
# 1) backend (binario linux precompilado en captain/backend/bin/server)
docker rm -f rlsui-backend 2>$null
docker run -d --name rlsui-backend -p 8080:8080 -e RLS_JWT_SECRET=testsecret `
  -v C:\Users\srDino\Proyects\rls-legacy\captain\backend\bin\server:/srv/server `
  -v rls-frontend-test-data:/data --memory 256m --cpus 0.5 alpine:3.20 `
  /bin/sh -c "RLS_DB_PATH=/data/rls.db /srv/server"

# 2) suite (host.docker.internal porque el test corre en otro contenedor)
docker run --rm --add-host=host.docker.internal:host-gateway `
  -e RLS_TEST_BASE=http://host.docker.internal:8080 -e HOME=/tmp `
  -v C:\Users\srDino\Proyects\rls-legacy\captain\frontend:/app `
  -v rls-bun-cache:/root/.bun/install/cache `
  -w /app --memory 1g --cpus 1 oven/bun:1.2-alpine bun test src/lib/api

# 3) tearDown
docker rm -f rlsui-backend
```

Sanity positivo declarativo (`bun src/lib/api/ui-sanity.ts` con
`RLS_SANITY_BASE`): bootstrap/login, agents `[]`, token plano `rlst_` una vez,
thresholds 404, alerts scope, users y cabecera SSE `text/event-stream`.

## Cómo reproducir el binario del backend si queda desfasado (DEC-008)

```powershell
docker run --rm -v rls-gomodcache:/go/pkg/mod -v rls-gobuildcache:/root/.cache/go-build `
  -v C:\Users\srDino\Proyects\rls-legacy\captain\backend:/src -w /src `
  --memory 512m --cpus 1 golang:1.22-alpine `
  sh -c "export PATH=/usr/local/go/bin:`$PATH; export GOTOOLCHAIN=auto; go build -o bin/server ./cmd/server"
```
