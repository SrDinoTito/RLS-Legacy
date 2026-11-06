/*
 * Normalización runtime de filas de alertas (REQ-022 hardening client-side).
 *
 * El backend serializa `value`/`agent_name`/`message` como NULL-able (columna
 * value TEXT, agent dado de baja post-dismiss, tipos nuevos sin label) y ningún
 * render de la UI debe asumir shapes: normalizeAlert devuelve SIEMPRE un shape
 * inofensivo (null en vez de undefined/NaN, y el resto coercible a string).
 * Pura, sin depender de Svelte ni fetch → testeable con bun test (bun: criollo).
 */

/**
 * Shape runtime tolerante: `fired_at` pasa a number | null (el tipo Wire de
 * `Alert` lo declara number, pero un row corrupto/retention-edge no debe poder
 * tumbar el render; fmtTs() ya trata null → "—").
 */
export interface NormalizedAlert {
	id: number;
	agent_id: number;
	agent_name: string | null;
	type: string;
	value: number | null;
	message: string | null;
	fired_at: number | null;
	resolved_at: number | null;
}

/** number finito o null (NaN/Infinity/strings numéricos NO se aceptan). */
function toFinite(v: unknown): number | null {
	return typeof v === 'number' && Number.isFinite(v) ? v : null;
}

/** string real o null (no acepta números/objetos — el render escribe strings). */
function toStr(v: unknown): string | null {
	return typeof v === 'string' ? v : null;
}

/**
 * Normaliza UNA fila de GET /api/v1/alerts (o cualquier shape degradado:
 * null, array, string, campos ausentes/NaN). Jamás lanza.
 */
export function normalizeAlert(raw: unknown): NormalizedAlert {
	const rec = typeof raw === 'object' && raw !== null ? (raw as Record<string, unknown>) : {};
	return {
		id: toFinite(rec.id) ?? 0,
		agent_id: toFinite(rec.agent_id) ?? 0,
		agent_name: toStr(rec.agent_name),
		type: toStr(rec.type) ?? '',
		value: toFinite(rec.value),
		message: toStr(rec.message),
		fired_at: toFinite(rec.fired_at),
		resolved_at: toFinite(rec.resolved_at)
	};
}

/** Normaliza una página entera de rows (answer de listAlerts con scope active|all). */
export function normalizeAlerts(rows: unknown): NormalizedAlert[] {
	if (!Array.isArray(rows)) return [];
	return rows.map((r) => normalizeAlert(r));
}
