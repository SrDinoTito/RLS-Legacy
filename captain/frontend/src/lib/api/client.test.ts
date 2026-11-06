/*
 * Smoke e2e de contratos contra el backend real en Docker (TASK-006, AC-005).
 *
 * CÓMO CORRER (CON-007: todo bun dentro de Docker; el binario del backend
 * está en captain/backend/bin/server, compilado en el smoke de TASK-004):
 *
 *   1) backend:
 *      docker run -d --name rlsui-backend -p 8080:8080 \
 *        -e RLS_JWT_SECRET=testsecret \
 *        -v <ABS>/captain/backend/bin/server:/srv/server \
 *        -v rls-frontend-test-data:/data \
 *        --memory 256m --cpus 0.5 alpine:3.20 \
 *        /bin/sh -c "RLS_DB_PATH=/data/rls.db /srv/server"
 *
 *   2) suite (desde captain/frontend, monta el repo y usa cache de vol):
 *      docker run --rm --add-host=host.docker.internal:host-gateway \
 *        -e RLS_TEST_BASE=http://host.docker.internal:8080 -e HOME=/tmp \
 *        -v <ABS>/captain/frontend:/app \
 *        -v rls-bun-cache:/root/.bun/install/cache \
 *        -w /app --memory 1g --cpus 1 oven/bun:1.2-alpine bun test src/lib/api
 *
 *   3) tearDown:
 *      docker rm -f rlsui-backend
 *
 * Exercita las MISMAS funciones del cliente que usan los stores UI
 * (auth login/me/logout + users + tokens). El jar de cookies JWT se
 * inyecta interceptando fetch global: así cubrimos credentials:'include'
 * sin depender del jar nativo de Bun.
 */
import { describe, it, expect } from 'bun:test';
import {
	setApiBase,
	authLogin,
	authMe,
	authLogout,
	authBootstrap,
	listUsers,
	createUser,
	createToken,
	revokeToken,
	listAlerts
} from './client';

const BASE = process.env.RLS_TEST_BASE ?? 'http://127.0.0.1:8080';
setApiBase(BASE);

/* ----------------------- fetch wrapper con cookie jar ----------------------- */

const realFetch = globalThis.fetch;
let sessionCookie = '';

type WrappedFetch = typeof fetch & { __lastSetCookie?: string | null };

/** Intercepta fetch: inyecta Cookie del jar y guarda el último set-cookie. */
const jarFetch: WrappedFetch = (async (input, init) => {
	const headers = new Headers(init?.headers);
	if (sessionCookie) headers.set('Cookie', sessionCookie);
	const res = await realFetch(input as string, { credentials: 'include', ...init, headers });
	jarFetch.__lastSetCookie = res.headers.get('set-cookie');
	return res;
}) as WrappedFetch;
globalThis.fetch = jarFetch as typeof fetch;

/** Tras cada llamada al cliente: absorbe el set-cookie (login/logout). */
function ingestSetCookie(): void {
	const sc = jarFetch.__lastSetCookie;
	jarFetch.__lastSetCookie = null;
	if (!sc) return;
	sessionCookie = sc.split(';')[0].trim(); // "rls_session=..." (o el vencido)
}

/* --------------------------------- liveness --------------------------------- */

const alive: boolean = await (async () => {
	try {
		const res = await realFetch(`${BASE}/healthz`, { signal: AbortSignal.timeout(2000) });
		return res.ok;
	} catch {
		return false;
	}
})();

/* ----------------------------------- suite ----------------------------------- */

describe.skipIf(!alive)('contratos auth + roles contra backend real', () => {
	const suffix = crypto.randomUUID().slice(0, 8);
	const adminUser = `test-admin-${suffix}`;
	const adminPass = `S3cret-${suffix}`;
	const viewerUser = `test-viewer-${suffix}`;

	it('login con credenciales inválidas → 401 y sin cookie', async () => {
		const res = await authLogin('no-existe', 'wrong');
		expect(res.ok).toBe(false);
		expect(res.status).toBe(401);
		ingestSetCookie(); // no debería llegarnos set-cookie
		expect(sessionCookie).toBe('');
	});

	it('me en guest → 401', async () => {
		const res = await authMe();
		expect(res.ok).toBe(false);
		expect(res.status).toBe(401);
	});

	it('bootstrap/login admin → 200 con cookie rls_session y perfil', async () => {
		// bootstrap: 201 la primera vez, 403 si ya hay usuarios (estado previo tolerado)
		const boot = await authBootstrap(adminUser, adminPass);
		expect([201, 403]).toContain(boot.status);

		const res = await authLogin(adminUser, adminPass);
		expect(res.ok).toBe(true);
		expect(res.status).toBe(200);
		expect(res.data?.username).toBe(adminUser);
		ingestSetCookie();
		expect(sessionCookie).toStartWith('rls_session=');

		const me = await authMe();
		expect(me.ok).toBe(true);
		expect(me.data?.username).toBe(adminUser);
		expect(me.data?.role).toBe('admin');
	});

	it('admin users CRUD + viewer 403 en endpoints admin-only', async () => {
		const created = await createUser({ username: viewerUser, password: `V3ryS3cret-${suffix}`, role: 'viewer' });
		expect(created.ok).toBe(true);
		expect(created.status).toBe(201);

		const list = await listUsers();
		expect(list.ok).toBe(true);
		const usernames = (list.data ?? []).map((u) => u.username);
		expect(usernames).toContain(adminUser);
		expect(usernames).toContain(viewerUser);

		// login como viewer en el mismo jar (logout primero)
		await authLogout();
		ingestSetCookie();
		const vLogin = await authLogin(viewerUser, `V3ryS3cret-${suffix}`);
		expect(vLogin.ok).toBe(true);
		ingestSetCookie();
		if (sessionCookie) {
			const me = await authMe();
			expect(me.data?.role).toBe('viewer');
			const forbidden = await listUsers();
			expect(forbidden.status).toBe(403);
		}
		await authLogout();
		ingestSetCookie();
		// me tras logout → 401 (sesión invalidada del lado del servidor)
		const meAfter = await authMe();
		expect(meAfter.ok).toBe(false);
		expect(meAfter.status).toBe(401);
	});

	it('token lifecycle: token plano 201 una vez + revoke 204', async () => {
		// restaurar sesión admin
		const res = await authLogin(adminUser, adminPass);
		expect(res.ok).toBe(true);
		ingestSetCookie();

		const created = await createToken(`tk-${suffix}`);
		expect(created.ok).toBe(true);
		expect(created.status).toBe(201);
		expect(created.data?.token).toStartWith('rlst_');
		expect(created.data?.token.length).toBeGreaterThan(32);

		if (created.data?.id != null) {
			const rev = await revokeToken(created.data.id);
			expect(rev.ok).toBe(true);
			expect(rev.status).toBe(204);
		}
		// sin cookie contrastada arriba; alerts como admin sigue 200
		const alerts = await listAlerts('all');
		expect(alerts.ok).toBe(true);
		expect(Array.isArray(alerts.data)).toBe(true);
	});

	it('cleanup jar final: me sin sesión → 401', async () => {
		sessionCookie = '';
		const res = await authMe();
		expect(res.ok).toBe(false);
		expect(res.status).toBe(401);
	});
});

// fin de suite — sin pegotes.
