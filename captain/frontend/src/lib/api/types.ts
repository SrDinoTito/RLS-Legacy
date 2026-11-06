/*
 * Tipos de contratos API (TASK-004 backend ya implementado).
 * Los punteros Go ("*float64") llegan como `null` en JSON; se modelan con `| null`.
 * Fuente de verdad: captain/backend/internal/api/*.go (agents_api.go, alerts.go, tokens.go, users.go).
 */

/** Última muestra disponible de un agent (GET /agents → metrics, NULL si nunca reportó). */
export interface MetricPoint {
	ts: number | null;
	cpu_pct: number | null;
	ram_used_mb: number | null;
	ram_limit_mb: number | null;
	rx_mb: number | null;
	tx_mb: number | null;
	uptime_sec: number | null;
}

/** GET /api/v1/agents — un agent con su muestra más reciente y alertas abiertas. */
export interface Agent {
	id: number;
	name: string;
	hostname: string;
	/** JSON crudo del backend: string o null. */
	container_id: string | null;
	status: 'online' | 'offline' | string;
	/** Unix del server time (DSG-003), null si nunca reportó. */
	last_seen: number | null;
	interval: number;
	/** Labels como objeto plano o null. */
	labels: Record<string, string> | null;
	/** Tags (1-5, a-z0-9-_, ≤24 chars) sincronizados con el token ligado, o null. */
	tags: string[] | null;
	/** Etiquetas de clasificación editables por contenedor, separadas de tags del token. */
	container_tags: string[] | null;
	thresholds_cpu: number | null;
	thresholds_ram: number | null;
	metrics: MetricPoint | null;
	alert_count: number;
	/** True when an operator has excluded this container from monitoring. */
	monitoring_ignored: boolean;
	/** explorer v4 disk: snapshot df del FS del container (MiB). null = nunca reportado. */
	disk_free_mb: number | null;
	disk_total_mb: number | null;
}

/** Bucket del history (AVG cpu / MAX resto por ts de bucket). Buckets vacíos NO existen. */
export interface HistoryPoint {
	ts: number;
	cpu_pct: number | null;
	ram_used_mb: number | null;
	ram_limit_mb: number | null;
	rx_mb: number | null;
	tx_mb: number | null;
	uptime_sec: number | null;
}

/** GET /api/v1/agents/{id}/history — la serie no reticulada: huecos ok. */
export interface HistoryResponse {
	agent_id: number;
	step: number;
	points: HistoryPoint[];
}

/** GET/PUT /api/v1/agents/{id}/thresholds. cpu∈[10..100], ram∈[50..100]. */
export interface Thresholds {
	thresholds_cpu: number;
	thresholds_ram: number;
}

/** Effective outbound policy for one VPS or agent. Agents inherit from VPS until customized. */
export interface NotificationPolicy {
	scope: 'vps' | 'agent';
	scope_id: string;
	notifications_enabled: boolean;
	resource_alerts_enabled: boolean;
	customized: boolean;
	inherited: boolean;
	vps_notifications_enabled?: boolean;
	parent_scope_id?: string;
}

export interface NotificationPolicyValues {
	notifications_enabled: boolean;
	resource_alerts_enabled: boolean;
}

/** GET /api/v1/alerts?scope=active|all (resuelta → resolved_at != null). */
export interface Alert {
	id: number;
	agent_id: number;
	agent_name: string | null;
	type: 'offline' | 'high_cpu' | 'high_ram' | string;
	value: number | null;
	message: string | null;
	fired_at: number;
	resolved_at: number | null;
}

/**
 * GET /api/v1/tokens (contrato v2: token plano COMPLETO siempre, copiable
 * desde la UI; tags editables vía PUT /tokens/{id}/tags con sync al agent).
 * New tokens are VPS-scoped credentials for Stowaway. `vps_scoped=false`
 * identifies a grandfathered token kept for migration compatibility.
 * `adopted_count` counts all discovered containers linked to the token.
 */
export interface AgentToken {
	id: number;
	name: string;
	token: string;
	tags: string[] | null;
	agent_id: number | null;
	created_at: number;
	revoked_at: number | null;
	status: 'active' | 'revoked';
	/** True for tokens that can report multiple containers from one VPS. */
	enrollment: boolean;
	/** Stowaway Docker Engine ID pinned on first report; null while installing. */
	vps_id: string | null;
	/** True for newly-created single-VPS credentials; false is grandfathered migration compatibility. */
	vps_scoped: boolean;
	/** Number of linked containers, including retired/historical records. */
	adopted_count: number;
}

/** POST /api/v1/tokens — creates the only new credential mode: one VPS token. */
export interface TokenCreated {
	id: number;
	name: string;
	token: string;
	tags: string[] | null;
	enrollment: boolean;
	vps_id: string | null;
	vps_scoped: boolean;
}

export interface AgentMonitoringUpdated {
	id: number;
	ignored: boolean;
	alerts_resolved: number;
}

/** PUT /api/v1/tokens/{id}/tags — 200 con la lista actualizada (sync al agent ligado en vivo). */
export interface TokenTagsUpdated {
	id: number;
	name: string;
	tags: string[] | null;
}

/** GET /api/v1/users (admin-only, viewer → 403). */
export interface User {
	id: number;
	username: string;
	role: 'admin' | 'viewer' | string;
	created_at: number;
}

/** Payloads de eventos SSE (GET /api/v1/stream, DSG-004): `event: X\ndata: {json}\n\n`. */
export interface SseHeartbeatPayload {
	agent_id: number;
	name: string;
	status: 'online' | 'offline' | string;
}

/** agent_offline / agent_online comparten forma {agent_id,name,status}. */
export type SseAgentStatusPayload = SseHeartbeatPayload;

/** alert_new: nueva alerta abierta. */
export interface SseAlertNewPayload {
	id: number;
	agent_id: number;
	type: string;
	value: number | null;
	message: string | null;
	fired_at: number;
}

/** alert_resolved: por resolve manual ({id}) o por resurrección ({agent_id,type,count}). */
export interface SseAlertResolvedPayload {
	id?: number;
	agent_id?: number;
	type?: string;
	count?: number;
	resolved_at?: number;
}

/** GET/PUT /api/v1/notify/settings — SIEMPRE enmascarado (last8 webhook / last4 token); jamás plaintext (REQ-050). */
export interface NotifySettings {
	/** Últimos 8 chars del webhook URL, o null (sin configurar). */
	discord_webhook_url: string | null;
	/** Últimos 4 chars del bot token, o null. */
	telegram_bot_token: string | null;
	/** Chat id numérico completo (no es secreto), o null. */
	telegram_chat_id: string | null;
	discord_enabled: boolean;
	telegram_enabled: boolean;
}

/** PUT parcial: clave ausente = no tocar; null|'' = deshabilitar ese canal. */
export interface NotifySettingsPatch {
	discord_webhook_url?: string | null;
	telegram_bot_token?: string | null;
	telegram_chat_id?: string | null;
}
