import { goto } from '$app/navigation';
import { authLogin, authBootstrap, authMe, authLogout } from '#lib/api/client.js';

export interface AuthUser {
	id: number;
	username: string;
	role: string;
}

export interface AuthResult {
	ok: boolean;
	status: number;
}

/*
 * Store de auth (DSG-001): JWT en cookie httpOnly → todo fetch con
 * credentials: 'include' (centralizado en #lib/api/client); el estado local
 * es solo el perfil visible. REQ-024: admin (todo) / viewer (solo lectura).
 */
const state = $state<{ user: AuthUser | null }>({ user: null });

async function login(username: string, password: string): Promise<AuthResult> {
	const res = await authLogin(username, password);
	if (res.ok && res.data) {
		state.user = {
			id: res.data.id,
			username: res.data.username,
			role: res.data.role ?? 'viewer'
		};
	}
	return { ok: res.ok, status: res.status };
}

export async function bootstrapUser(username: string, password: string): Promise<AuthResult> {
	const res = await authBootstrap(username, password);
	return { ok: res.ok, status: res.status };
}

export async function me(): Promise<AuthUser | null> {
	const res = await authMe();
	if (res.ok && res.data) {
		state.user = {
			id: res.data.id,
			username: res.data.username,
			role: res.data.role ?? 'viewer'
		};
		return state.user;
	}
	state.user = null;
	return null;
}

/** Logout tolerant (REQ-024): cualquier error igualmente limpia la sesión local. */
export async function logout(): Promise<void> {
	try {
		await authLogout();
	} catch {
		// tolerado (backend caído / 401 ya no autenticado)
	} finally {
		state.user = null;
		goto('/login');
	}
}

export const auth = {
	get user(): AuthUser | null {
		return state.user;
	},
	login,
	bootstrapUser,
	me,
	logout
};
