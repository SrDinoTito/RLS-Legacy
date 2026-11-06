/*
 * Unit tests (bun:test) — normalizeAlert/normalizeAlerts: hardening null-safety
 * de las filas de alertas (REQ-022). Correr con:
 *   docker run --rm -v <repo>/captain/frontend:/app -w /app oven/bun:1.4-alpine bun test src/lib/alerts.test.ts
 * Sin red, sin backend: la normalización es pura.
 */
import { describe, it, expect } from 'bun:test';
import { normalizeAlert, normalizeAlerts } from './alerts';

/* ------------------------------- fila ideal v2 ------------------------------- */
const WIRED = {
	id: 3,
	agent_id: 1,
	agent_name: 'demo-app',
	type: 'high_cpu',
	value: 94.6,
	message: 'CPU alta: valor 94.6 supera umbral 90',
	fired_at: 1_793_416_163,
	resolved_at: null
};

describe('normalizeAlert — datos reales v2', () => {
	it('filas reales pasan intactas (demo-app high_cpu abierto)', () => {
		const out = normalizeAlert(WIRED);
		expect(out).toEqual(WIRED);
		expect(out.resolved_at).toBeNull();
	});

	it('alerta resuelta con value y resolved_at (demo-cache offline)', () => {
		const out = normalizeAlert({
			...WIRED,
			id: 4,
			agent_id: 5,
			agent_name: 'demo-cache',
			type: 'offline',
			value: 25,
			resolved_at: 1_793_417_050
		});
		expect(out.resolved_at).toBe(1_793_417_050);
		expect(out.value).toBe(25);
	});
});

describe('normalizeAlert — shapes NULL y degradados', () => {
	it('value null NO crashea y queda null (fmtMb/fmtBytesMb pintan "—")', () => {
		const out = normalizeAlert({ ...WIRED, value: null });
		expect(out.value).toBeNull();
	});

	it('fired_at null/NaN/Infinity → null (fmtTs pinta "—")', () => {
		expect(normalizeAlert({ ...WIRED, fired_at: null }).fired_at).toBeNull();
		expect(normalizeAlert({ ...WIRED, fired_at: Number.NaN }).fired_at).toBeNull();
		expect(normalizeAlert({ ...WIRED, fired_at: Number.POSITIVE_INFINITY }).fired_at).toBeNull();
	});

	it('agent_name null post-dismiss → null (render pinta "#<agent_id>")', () => {
		const out = normalizeAlert({ ...WIRED, agent_name: null, agent_id: 7 });
		expect(out.agent_name).toBeNull();
		expect(out.agent_id).toBe(7);
	});

	it('resolved_at null en activa / number en resuelta (sess bits de UI)', () => {
		expect(normalizeAlert({ ...WIRED, resolved_at: null }).resolved_at).toBeNull();
		expect(normalizeAlert({ ...WIRED, resolved_at: 99 }).resolved_at).toBe(99);
		expect(normalizeAlert({ ...WIRED, resolved_at: '99' }).resolved_at).toBeNull();
	});

	it('type ausente/no-string → "" (alertTypeLabel no crashea)', () => {
		expect(normalizeAlert({ ...WIRED, type: undefined }).type).toBe('');
		expect(normalizeAlert({ ...WIRED, type: 42 }).type).toBe('');
	});

	it('message numérico/ausente → null (render usa `?? ""`)', () => {
		expect(normalizeAlert({ ...WIRED, message: 123 }).message).toBeNull();
		expect(normalizeAlert({ ...WIRED, message: undefined }).message).toBeNull();
	});

	it('id/agent_id corruptos → 0 (key de {#each} nunca undefined)', () => {
		const out = normalizeAlert({ ...WIRED, id: 'x', agent_id: Number.NaN });
		expect(out.id).toBe(0);
		expect(out.agent_id).toBe(0);
	});

	it('basura absoluta: null, string, array, {} — nunca lanza', () => {
		for (const shape of [null, undefined, 'boom', 5, [], {}, ['a']]) {
			const out = normalizeAlert(shape);
			expect(out.id).toBeTypeOf('number');
			expect(out.type).toBeTypeOf('string');
			expect(out.value).toEqual(null);
			expect(out.fired_at).toEqual(null);
		}
	});
});

describe('normalizeAlerts — scope active|all / respuesta completa', () => {
	it('array completo (status 200 body COMPLETA v2) se normaliza fila a fila', () => {
		const rows = [
			WIRED,
			{
				id: 1,
				agent_id: 1,
				agent_name: null, // agent dado de baja
				type: 'offline',
				value: null, // offline sin valor
				message: null,
				fired_at: 1_793_407_820,
				resolved_at: 1_793_411_778
			}
		];
		const out = normalizeAlerts(rows);
		expect(out).toHaveLength(2);
		expect(out[1].agent_name).toBeNull();
		expect(out[1].value).toBeNull();
		expect(out[1].message).toBeNull();
	});

	it('data inesperada (null/objeto/number) → [] (page entra al empty-state sin crashear)', () => {
		expect(normalizeAlerts(null)).toEqual([]);
		expect(normalizeAlerts({})).toEqual([]);
		expect(normalizeAlerts(0)).toEqual([]);
	});
});
