/*
 * Cliente HTTP de la API (contratos TASK-004, CON-007: todo relativo a /api,
 * cookie JWT httpOnly → SIEMPRE credentials: 'include' — DSG-001).
 *
 * Toda ejecución fetch pasa por aquí para que esté centralizado el:
 *  - prefijo base (vacío = relativo; en `bun test` se apunta a http://127.0.0.1:8080
 *    con setApiBase() para el smoke de integración);
 *  - serialización/deserialización JSON;
 *  - mapping de 204/404/* a ApiResult { ok, status, data }.
 *
 * Este archivo NO usa runas ni dependencias de Svelte: es importable desde
 * bun test y desde cualquier datastore.
 */
import type {
	Agent,
	Alert,
	AgentToken,
	AgentMonitoringUpdated,
	Thresholds,
	TokenCreated,
	TokenTagsUpdated,
	User,
	HistoryResponse,
	NotifySettings,
	NotifySettingsPatch,
	NotificationPolicy,
	NotificationPolicyValues
} from './types';

/** Base opcional (absoluta) para el smoke e2e; vacío = relativo al origen. */
let apiBase = '';

export function setApiBase(base: string): void {
	apiBase = base.replace(/\/+$/, '');
}

export function getApiBase(): string {
	return apiBase;
}

/** Resultado uniforme de todo endpoint: data es null en 204/404/error parse fail. */
export interface ApiResult<T> {
	ok: boolean;
	status: number;
	data: T | null;
	/** Mensaje de error del backend ({ "error": "..." }) si existió. */
	error: string | null;
}

const jsonHeaders = { 'Content-Type': 'application/json' };

/** Fetch centralizado con cookie httpOnly (credentials include SIEMPRE). */
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<ApiResult<T>> {
	const res = await fetch(apiBase + path, {
		credentials: 'include',
		...init,
		headers: { ...(init?.headers ?? {}) }
	});
	let data: T | null = null;
	let error: string | null = null;
	if (res.status !== 204) {
		try {
			const parsed = (await res.json()) as unknown;
			if (parsed !== null && typeof parsed === 'object') {
				if (res.ok) data = parsed as T;
				else {
					const maybeErr = parsed as { error?: unknown };
					if (typeof maybeErr.error === 'string') error = maybeErr.error;
				}
			}
		} catch {
			// 204 real o body vacío: tolerado
		}
	}
	return { ok: res.ok, status: res.status, data, error };
}

/* ----------------------------- Auth (DSG-001) ----------------------------- */

export interface AuthMe {
	id: number;
	username: string;
	role: string;
}

export interface AuthBootstrapStatus {
	required: boolean;
}

export interface PublicConfig {
	dashboard_url: string;
}

export function getPublicConfig(): Promise<ApiResult<PublicConfig>> {
	return apiFetch<PublicConfig>('/api/v1/public-config');
}

export function authBootstrapStatus(): Promise<ApiResult<AuthBootstrapStatus>> {
	return apiFetch<AuthBootstrapStatus>('/api/v1/auth/bootstrap/status');
}

export function authLogin(username: string, password: string): Promise<ApiResult<AuthMe>> {
	return apiFetch<AuthMe>('/api/v1/auth/login', {
		method: 'POST',
		headers: jsonHeaders,
		body: JSON.stringify({ username, password })
	});
}

export function authBootstrap(username: string, password: string): Promise<ApiResult<AuthMe>> {
	return apiFetch<AuthMe>('/api/v1/auth/bootstrap', {
		method: 'POST',
		headers: jsonHeaders,
		body: JSON.stringify({ username, password })
	});
}

export function authMe(): Promise<ApiResult<AuthMe>> {
	return apiFetch<AuthMe>('/api/v1/auth/me');
}

export function authLogout(): Promise<ApiResult<null>> {
	return apiFetch<null>('/api/v1/auth/logout', { method: 'POST' });
}

/* --------------------------------- Agents --------------------------------- */

export function listAgents(): Promise<ApiResult<Agent[]>> {
	return apiFetch<Agent[]>('/api/v1/agents');
}

/**
 * GET /api/v1/agents/{id}/history?from&to&step — from/to en unix segundos
 * (ts de payload, DSG-003); step ≥ 30 s y (to-from)/step ≤ 2000 en el backend.
 */
export function agentHistory(
	id: number,
	from: number,
	to: number,
	step: number
): Promise<ApiResult<HistoryResponse>> {
	return apiFetch<HistoryResponse>(`/api/v1/agents/${id}/history?from=${from}&to=${to}&step=${step}`);
}

export function getThresholds(id: number): Promise<ApiResult<Thresholds>> {
	return apiFetch<Thresholds>(`/api/v1/agents/${id}/thresholds`);
}

/** PUT thresholds; validación backend: cpu∈[10..100], ram∈[50..100] → 400 si no. */
export function putThresholds(id: number, thresholds: Thresholds): Promise<ApiResult<Thresholds>> {
	return apiFetch<Thresholds>(`/api/v1/agents/${id}/thresholds`, {
		method: 'PUT',
		headers: jsonHeaders,
		body: JSON.stringify({ thresholds_cpu: thresholds.thresholds_cpu, thresholds_ram: thresholds.thresholds_ram })
	});
}

/** GET effective policy for a VPS or agent scope (admin-only). */
export function getNotificationPolicy(scope: 'vps' | 'agent', scopeID: string | number): Promise<ApiResult<NotificationPolicy>> {
	return apiFetch<NotificationPolicy>(`/api/v1/notification-policies/${scope}/${encodeURIComponent(String(scopeID))}`);
}

/** PUT explicit notification policy for a scope (both settings saved atomically). */
export function putNotificationPolicy(
	scope: 'vps' | 'agent',
	scopeID: string | number,
	values: NotificationPolicyValues
): Promise<ApiResult<NotificationPolicy>> {
	return apiFetch<NotificationPolicy>(`/api/v1/notification-policies/${scope}/${encodeURIComponent(String(scopeID))}`, {
		method: 'PUT', headers: jsonHeaders, body: JSON.stringify(values)
	});
}

/** DELETE explicit policy: VPS resets to defaults; agent inherits from its VPS again. */
export function resetNotificationPolicy(scope: 'vps' | 'agent', scopeID: string | number): Promise<ApiResult<null>> {
	return apiFetch<null>(`/api/v1/notification-policies/${scope}/${encodeURIComponent(String(scopeID))}`, { method: 'DELETE' });
}

/** PUT /api/v1/agents/{id}/container-tags — etiquetas manuales del contenedor. */
export function putContainerTags(id: number, tags: string[]): Promise<ApiResult<{ id: number; container_tags: string[] }>> {
	return apiFetch<{ id: number; container_tags: string[] }>(`/api/v1/agents/${id}/container-tags`, {
		method: 'PUT',
		headers: jsonHeaders,
		body: JSON.stringify({ tags })
	});
}

/** PUT /api/v1/agents/{id}/monitoring — exclude/re-include one container. */
export function putAgentMonitoringIgnored(id: number, ignored: boolean): Promise<ApiResult<AgentMonitoringUpdated>> {
	return apiFetch<AgentMonitoringUpdated>(`/api/v1/agents/${id}/monitoring`, {
		method: 'PUT',
		headers: jsonHeaders,
		body: JSON.stringify({ ignored })
	});
}

/* --------------------------------- Alerts --------------------------------- */

/** scope=active (default backend) | all. */
export function listAlerts(scope: 'active' | 'all'): Promise<ApiResult<Alert[]>> {
	return apiFetch<Alert[]>(`/api/v1/alerts?scope=${scope}`);
}

/** Alertas propias de un contenedor; scope=all conserva el historial cerrado. */
export function listAgentAlerts(id: number, scope: 'active' | 'all'): Promise<ApiResult<Alert[]>> {
	return apiFetch<Alert[]>(`/api/v1/agents/${id}/alerts?scope=${scope}`);
}

/** 200 ok; 409 ya resuelta; 404 no existe. */
export function resolveAlert(id: number): Promise<ApiResult<{ id: number; resolved_at: number }>> {
	return apiFetch<{ id: number; resolved_at: number }>(`/api/v1/alerts/${id}/resolve`, {
		method: 'POST'
	});
}

/* -------------------------------- Tokens/agents admin -------------------------------- */

export function listTokens(): Promise<ApiResult<AgentToken[]>> {
	return apiFetch<AgentToken[]>('/api/v1/tokens');
}

/**
 * POST /api/v1/tokens — 201 {id,name,token,tags,enrollment,vps_scoped};
 * el único flujo nuevo crea una credencial exclusiva de VPS para Stowaway.
 */
export function createToken(
	name: string,
	tags?: string[]
): Promise<ApiResult<TokenCreated>> {
	const body: { name: string; tags?: string[] } = { name };
	if (tags && tags.length > 0) body.tags = tags;
	return apiFetch<TokenCreated>('/api/v1/tokens', {
		method: 'POST',
		headers: jsonHeaders,
		body: JSON.stringify(body)
	});
}

/**
 * PUT /api/v1/tokens/{id}/tags — 200 con la lista actualizada; el backend
 * sincroniza en vivo al agent ligado (RLS_LABELS_JSON → labels.reported.tags
 * vía env RLS_LABELS_JSON del explorer).
 */
export function putTokenTags(id: number, tags: string[]): Promise<ApiResult<TokenTagsUpdated>> {
	return apiFetch<TokenTagsUpdated>(`/api/v1/tokens/${id}/tags`, {
		method: 'PUT',
		headers: jsonHeaders,
		body: JSON.stringify({ tags })
	});
}

/** DELETE → 204 (idempotente para ya-revocado); 404 si no existe. */
export function revokeToken(id: number): Promise<ApiResult<null>> {
	return apiFetch<null>(`/api/v1/tokens/${id}`, { method: 'DELETE' });
}

/* --------------------------------- Users (admin) --------------------------------- */

export function listUsers(): Promise<ApiResult<User[]>> {
	return apiFetch<User[]>('/api/v1/users');
}

export type Role = 'admin' | 'viewer';

export function createUser(body: {
	username: string;
	password: string;
	role: Role;
}): Promise<ApiResult<{ username: string; role: string }>> {
	return apiFetch<{ username: string; role: string }>('/api/v1/users', {
		method: 'POST',
		headers: jsonHeaders,
		body: JSON.stringify(body)
	});
}

/** DELETE → 204; 409 no puedes borrar el último admin (REQ-012). */
export function deleteUser(id: number): Promise<ApiResult<null>> {
	return apiFetch<null>(`/api/v1/users/${id}`, { method: 'DELETE' });
}

/**
 * PUT /api/v1/users/{id}/password {"password"} → 204; un admin puede cambiar
 * la de cualquiera y un usuario la propia (self); otro caso → 403.
 */
export function updateUserPassword(id: number, password: string): Promise<ApiResult<null>> {
	return apiFetch<null>(`/api/v1/users/${id}/password`, {
		method: 'PUT',
		headers: jsonHeaders,
		body: JSON.stringify({ password })
	});
}

/* --------------------------- Notify settings (admin) --------------------------- */

/** GET /api/v1/notify/settings — vista enmascarada (last8/last4), nunca plaintext. */
export function getNotifySettings(): Promise<ApiResult<NotifySettings>> {
	return apiFetch<NotifySettings>('/api/v1/notify/settings');
}

/**
 * PUT /api/v1/notify/settings — patch parcial: clave ausente = no tocar;
 * null|'' = deshabilitar el canal. Validación backend → 400 si el formato no
 * es un webhook de Discord o un bot token/chat id de Telegram. Devuelve la
 * vista enmascarada actualizada.
 */
export function putNotifySettings(
	patch: NotifySettingsPatch
): Promise<ApiResult<NotifySettings>> {
	return apiFetch<NotifySettings>('/api/v1/notify/settings', {
		method: 'PUT',
		headers: jsonHeaders,
		body: JSON.stringify(patch)
	});
}

/** POST /api/v1/notify/test — 201 ok; 400 canal desconocido; 503 sin configurar; 502 fallo de entrega. */
export function testNotify(
	channel: 'discord' | 'telegram'
): Promise<ApiResult<{ status: string; channel: string }>> {
	return apiFetch<{ status: string; channel: string }>('/api/v1/notify/test', {
		method: 'POST',
		headers: jsonHeaders,
		body: JSON.stringify({ channel })
	});
}
