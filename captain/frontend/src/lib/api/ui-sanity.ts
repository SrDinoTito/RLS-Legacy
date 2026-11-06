/*
 * Sanity script de contratos UI (TASK-006): simula con el MISMO cliente
 * src/lib/api/client.js los flujos que la SPA usa — bootstrap/login, agents
 * vacíos, tokens, alerts, thresholds y cabeceras de /stream (SSE).
 * Correrlo: docker run ... oven/bun:1.2-alpine bun src/lib/api/ui-sanity.ts
 */
import {
	setApiBase,
	authBootstrap,
	authLogin,
	authMe,
	listAgents,
	listAlerts,
	createToken,
	listTokens,
	listUsers,
	getThresholds
} from './client.js';

const BASE = process.env.RLS_SANITY_BASE ?? 'http://host.docker.internal:8080';
setApiBase(BASE);

// jar de cookies simple vía interceptor (igual que el test e2e)
const realFetch = globalThis.fetch;
type WrappedFetch = typeof fetch & { __sc?: string | null };
let sessionCookie = '';
const jarFetch: WrappedFetch = (async (input, init) => {
	const headers = new Headers(init?.headers);
	if (sessionCookie) headers.set('Cookie', sessionCookie);
	const res = await realFetch(input as string, { credentials: 'include', ...init, headers });
	jarFetch.__sc = res.headers.get('set-cookie');
	return res;
}) as WrappedFetch;
globalThis.fetch = jarFetch as typeof fetch;
function eat(): void {
	const sc = jarFetch.__sc;
	jarFetch.__sc = null;
	if (sc) sessionCookie = sc.split(';')[0].trim();
}

const fail = (msg: string): never => {
	console.error('FAIL::' + msg);
	process.exit(1);
};

// 1) bootstrap (403 tolerada si hay usuarios) + login admin
const boot = await authBootstrap('ui-check', 'U1-passw0rd');
if (![201, 403].includes(boot.status)) fail(`bootstrap ${boot.status}`);
const login = await authLogin('ui-check', 'U1-passw0rd');
if (!login.ok) fail(`login ${login.status}`);
eat();
const me = await authMe();
if (me.data?.role !== 'admin') fail('me.role!=admin');

// 2) sin agents → lista 200 vacía (estado de onboarding del dashboard)
const agents = await listAgents();
if (!agents.ok || (agents.data?.length ?? -1) !== 0) fail('listAgents shape');

// 3) tokens: crear 201 con rlst_ plano; listar 200; sin agent_id
const tok = await createToken('sanity-nginx');
if (!tok.ok || tok.status !== 201 || !tok.data?.token.startsWith('rlst_')) fail('createToken');
const toks = await listTokens();
if (!toks.ok || toks.data?.[0]?.agent_id !== null) fail('listTokens shape');

// 4) thresholds de agent inexistente → 404 (400 no tolerado)
const thr = await getThresholds(999999);
if (thr.status !== 404) fail(`thresholds ${thr.status}`);

// 5) alerts → 200 [] scope=all
const alerts = await listAlerts('all');
if (!alerts.ok || !Array.isArray(alerts.data)) fail('listAlerts');

// 6) users admin-only OK
const users = await listUsers();
if (!users.ok || users.data?.[0]?.role !== 'admin') fail('listUsers');

// 7) /stream: content-type text/event-stream + primer frame/ping
sessionCookie = '';
const login2 = await authLogin('ui-check', 'U1-passw0rd');
if (!login2.ok) fail('relogin');
eat();
const controller = new AbortController();
const req = fetch(`${BASE}/api/v1/stream`, {
	headers: { Cookie: sessionCookie },
	signal: controller.signal
});
const res = await req;
const ct = res.headers.get('content-type') ?? '';
if (!ct.includes('text/event-stream')) fail(`stream ct=${ct}`);
setTimeout(() => controller.abort(), 1500);
try {
	const chunk = await res.text();
	console.log('SSE_FIRST_BYTES=' + JSON.stringify(chunk.slice(0, 40)));
} catch {
	// abort esperado
}

console.log('SANITY_OK');
process.exit(0);
