/*
 * Store SSE (REQ-023 / DSG-004): GET /api/v1/stream con eventos
 * heartbeat | agent_offline | agent_online | alert_new | alert_resolved
 * (frames `event: X\ndata: {json}\n\n`, ping cada 15 s del backend).
 *
 * - Estado reactivo: agents (SvelteMap id→Agent), activeAlerts, status.
 * - Reconexión: EventSource reintenta nativo; marcamos 'reconnecting' y si
 *   el error persiste > 30 s levantamos fallback polling (GET /agents cada
 *   15 s) hasta que el stream vuelva a abrirse.
 * - Estrategia heartbeat: el payload trae solo {agent_id,name,status} → merge
 *   de estado + refetch aliasado (2 s) de GET /agents para las métricas.
 */
import { SvelteMap } from 'svelte/reactivity';
import { toast } from 'svelte-sonner';
import { listAgents, listAlerts } from '#lib/api/client.js';
import type {
	Agent,
	SseHeartbeatPayload,
	SseAlertNewPayload,
	SseAlertResolvedPayload
} from '#lib/api/types.js';

const REFETCH_DEBOUNCE_MS = 2000;
const POLL_THRESHOLD_MS = 30_000;
const POLL_INTERVAL_MS = 15_000;

export type SseStatus = 'connecting' | 'live' | 'reconnecting';

const meta = $state<{
	status: SseStatus;
	activeAlerts: number;
	eventTick: number;
	loading: boolean;
}>({
	status: 'connecting',
	activeAlerts: 0,
	eventTick: 0,
	loading: false
});

/** Map id→agent. SvelteMap ES reactivo: las lecturas en $derived/$effect lo rastrean. */
const agents = new SvelteMap<number, Agent>();

let es: EventSource | null = null;
let refetchTimer: ReturnType<typeof setTimeout> | undefined;
let pollTimer: ReturnType<typeof setInterval> | undefined;
let firstErrorAt = 0;
let connecting = false;

/** Contador de eventos manejados — las páginas lo leen para refrescar datos locales. */
function tick(): void {
	meta.eventTick += 1;
}

/** Refetch único aliasado: coalesce de ráfagas de eventos (heartbeat cada ~10 s). */
function scheduleRefetch(delayMs = REFETCH_DEBOUNCE_MS): void {
	if (refetchTimer !== undefined) return;
	refetchTimer = setTimeout(() => {
		refetchTimer = undefined;
		void refreshAgents();
	}, delayMs);
}

/** GET /agents completo: reconstruye el mapa (fuente de verdad del dashboard). */
export async function refreshAgents(): Promise<void> {
	if (meta.loading) return;
	meta.loading = true;
	try {
		const res = await listAgents();
		if (res.ok && res.data) {
			agents.clear();
			let openAlerts = 0;
			for (const a of res.data) {
				agents.set(a.id, a);
				openAlerts += a.alert_count ?? 0;
			}
			meta.activeAlerts = openAlerts;
			tick();
		}
	} catch {
		// backend caído: el status SSE/polling lo comunica, no duplicamos toasts
	} finally {
		meta.loading = false;
	}
}

/** Apply a successful admin-side per-agent tag edit optimistically to the live fleet. */
export function applyContainerTags(agentID: number, tags: string[]): void {
	const existing = agents.get(agentID);
	if (!existing) return;
	agents.set(agentID, { ...existing, container_tags: [...tags] });
	tick();
}

/** Recuento fresco de alertas activas (alert_new/alert_resolved lo actualizan). */
export async function refreshAlerts(): Promise<void> {
	try {
		const res = await listAlerts('active');
		if (res.ok && res.data) {
			const next = res.data.length;
			// solo señalamos tick si cambió de verdad (evita bucles con $effect de páginas)
			if (next !== meta.activeAlerts) {
				meta.activeAlerts = next;
				tick();
			}
		}
	} catch {
		// tolerado
	}
}

/* --------------------------------- eventos -------------------------------- */

function onSseHeartbeat(data: SseHeartbeatPayload): void {
	const existing = agents.get(data.agent_id);
	if (existing) {
		// merge compacto del frame (sin métricas — el refetch las trae)
		agents.set(data.agent_id, { ...existing, name: data.name, status: data.status });
	}
	// agent nuevo (auto-registro) necesita el dato completo + métricas → tick
	scheduleRefetch();
}

function onSseAgentStatus(data: SseHeartbeatPayload, cameOnline: boolean): void {
	const existing = agents.get(data.agent_id);
	if (existing) {
		agents.set(data.agent_id, { ...existing, name: data.name, status: data.status });
	}
	if (!cameOnline) {
		// toast sutil de caída (REQ-023): warning corto, sin agresividad
		toast.warning(`${data.name || 'Agent'} sin señal`, {
			description: 'OFFLINE — sin heartbeat reciente.'
		});
	}
	scheduleRefetch();
	tick();
}

/** Token revocation soft-dismisses agents; remove the card immediately from the live fleet. */
function onSseAgentDismissed(data: { agent_id: number }): void {
	agents.delete(data.agent_id);
	void refreshAlerts();
	tick();
}

function onSseAlertNew(data: SseAlertNewPayload): void {
	meta.activeAlerts += 1;
	tick();
	const agentName = agents.get(data.agent_id)?.name ?? `agent #${data.agent_id}`;
	// toast DESTACADO (AC-004 UI): mensaje del backend si viene
	toast.error(data.message ?? `Alerta ${data.type}`, {
		description: `Agent: ${agentName}`,
		duration: 8000
	});
	// alert_count del agent cambió → refetch completo coalesced
	scheduleRefetch();
	// /alerts panel se refresca leyendo eventTick
}

function onSseAlertResolved(data: SseAlertResolvedPayload): void {
	const n = data.count ?? 1;
	meta.activeAlerts = Math.max(0, meta.activeAlerts - n);
	tick();
	scheduleRefetch();
}

function handleEvent<T>(
	event: MessageEvent,
	handler: (data: T) => void
): void {
	try {
		const parsed = JSON.parse(event.data) as T;
		handler(parsed);
	} catch {
		// frame malformado: ignorado en silencio (el stream sigue vivo)
	}
}

/* ------------------------------ fallback polling ------------------------------ */

function startPolling(): void {
	if (pollTimer !== undefined) return;
	pollTimer = setInterval(() => {
		void refreshAgents();
		void refreshAlerts();
	}, POLL_INTERVAL_MS);
	void refreshAgents(); // primera pasada inmediata al activar el fallback
}

function stopPolling(): void {
	if (pollTimer !== undefined) {
		clearInterval(pollTimer);
		pollTimer = undefined;
	}
}

/* ----------------------------- ciclo de vida stream ----------------------------- */

/** Abre el stream solo en navegador; idempotente. */
export function connect(): void {
	if (typeof window === 'undefined' || es || connecting) return;
	connecting = true;
	if (meta.status !== 'reconnecting') meta.status = 'connecting';

	try {
		es = new EventSource('/api/v1/stream');
	} catch {
		// sin EventSource: fallback inmediato por HTTP
		es = null;
		connecting = false;
		startPolling();
		meta.status = 'reconnecting';
		return;
	}
	connecting = false;

	es.onopen = () => {
		meta.status = 'live';
		firstErrorAt = 0;
		stopPolling();
		// estado inicial al abrir: todo el tablero en una pasada
		void refreshAgents();
	};

	es.onerror = () => {
		meta.status = 'reconnecting';
		if (firstErrorAt === 0) firstErrorAt = Date.now();
		// EventSource reintenta nativamente; tras >30 s de error → polling
		if (Date.now() - firstErrorAt > POLL_THRESHOLD_MS) startPolling();
	};

	es.addEventListener('heartbeat', (e) => handleEvent(e as MessageEvent, onSseHeartbeat));
	es.addEventListener('agent_offline', (e) =>
		handleEvent<SseHeartbeatPayload>(e as MessageEvent, (d) => onSseAgentStatus(d, false))
	);
	es.addEventListener('agent_online', (e) =>
		handleEvent<SseHeartbeatPayload>(e as MessageEvent, (d) => onSseAgentStatus(d, true))
	);
	es.addEventListener('agent_dismissed', (e) =>
		handleEvent<{ agent_id: number }>(e as MessageEvent, onSseAgentDismissed)
	);
	es.addEventListener('agent_monitoring_changed', () => {
		void refreshAgents();
		void refreshAlerts();
	});
	es.addEventListener('alert_new', (e) => handleEvent(e as MessageEvent, onSseAlertNew));
	es.addEventListener('alert_resolved', (e) => handleEvent(e as MessageEvent, onSseAlertResolved));
}

/** Sale limpio: cierra el stream y borra timers (beforeunload / pagehide). */
export function disconnect(): void {
	if (es) {
		es.close();
		es = null;
	}
	if (refetchTimer !== undefined) {
		clearTimeout(refetchTimer);
		refetchTimer = undefined;
	}
	stopPolling();
	firstErrorAt = 0;
	meta.status = 'connecting';
}

/* ------------------------------- JSON API pública ------------------------------- */

export const sse = {
	/** Mapa reactivo id→Agent (folder del dashboard). */
	agents,
	/** Estado de la conexión ('connecting' | 'live' | 'reconnecting'). */
	get status(): SseStatus {
		return meta.status;
	},
	/** Alertas abiertas (contador del badge global). */
	get activeAlerts(): number {
		return meta.activeAlerts;
	},
	/** Se incrementa con cada evento relevante — las páginas dejan $effect leerlo. */
	get eventTick(): number {
		return meta.eventTick;
	},
	/** True mientras hay refetch en vuelo (para deshabilitar botón de refresh manual). */
	get loading(): boolean {
		return meta.loading;
	},
	/** Lista ordenada por nombre para template (mantiene reactividad del Map). */
	list(): Agent[] {
		return [...agents.values()].sort((a, b) => a.name.localeCompare(b.name));
	},
	/** Conecta el stream (idempotente). */
	connect,
	/** Sale limpio: cierra stream y timers (unload/logout). */
	disconnect
};
