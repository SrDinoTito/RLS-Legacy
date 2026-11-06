<script lang="ts">
	import { toast } from 'svelte-sonner';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import UserRoundPlus from '@lucide/svelte/icons/user-round-plus';
	import { auth } from '#lib/stores/auth.svelte.js';
	import { createUser, deleteUser, updateUserPassword, listUsers } from '#lib/api/client.js';
	import type { Role } from '#lib/api/client.js';
	import type { User } from '#lib/api/types.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '#lib/components/ui/card/index.js';
	import { Dialog } from '#lib/components/ui/dialog/index.js';
	import {
		Table,
		TableCell,
		TableHead,
		TableRow
	} from '#lib/components/ui/table/index.js';
	import { fmtTs } from '#lib/format.js';

	/*
	 * Modal de gestión de usuarios (admin — REQ-024): lista con rol/creado,
	 * crear usuario (nombre + contraseña + rol), cambio de contraseña
	 * (PUT /users/{id}/password: admin siempre, self permitido por el backend)
	 * y borrar con confirmación. Igual que el backend: no puedes borrarte a
	 * ti mismo (409) y la flota siempre conserva un admin (409 last-admin).
	 */
	let { open = $bindable(false) }: { open?: boolean } = $props();

	let users = $state<User[]>([]);
	let uUsername = $state('');
	let uPassword = $state('');
	let uRole = $state<Role>('viewer');
	let creatingUser = $state(false);

	/** Confirmación de borrado (desactivado para self / último admin en UI). */
	let confirmDelete = $state<User | null>(null);
	let confirmOpen = $state(false);

	/** Cambio de contraseña: admin puede cambiar el de cualquiera. */
	let pwTarget = $state<User | null>(null);
	let pwValue = $state('');
	let pwOpen = $state(false);

	async function loadUsers(): Promise<void> {
		try {
			const res = await listUsers();
			if (res.ok) users = res.data ?? [];
			else toast.error(`No se pudieron listar usuarios (${res.status})`);
		} catch {
			toast.error('Backend no disponible');
		}
	}

	/** #admins actuales (para deshabilitar borrar al último admin en UI). */
	const adminCount = $derived(users.filter((u) => u.role === 'admin').length);

	/** Espeja la validación backend: self-delete → 409; último admin → 409. */
	function deleteDisabledReason(u: User): string {
		// username match (username es UNIQUE): /auth/me no devuelve id
		if (u.username === auth.user?.username) return 'No puedes eliminarte a ti mismo';
		if (u.role === 'admin' && adminCount <= 1) return 'Debe quedar al menos un administrador activo';
		return '';
	}

	async function submitUser(e: SubmitEvent): Promise<void> {
		e.preventDefault();
		if (creatingUser) return;
		creatingUser = true;
		try {
			const res = await createUser({ username: uUsername.trim(), password: uPassword, role: uRole });
			if (res.ok) {
				toast.success(`Usuario ${uUsername.trim()} creado (${uRole})`);
				uUsername = '';
				uPassword = '';
				uRole = 'viewer';
				await loadUsers();
			} else {
				toast.error(`No se pudo crear el usuario (${res.status})`, {
					description: res.error ?? undefined
				});
			}
		} catch {
			toast.error('Backend no disponible');
		} finally {
			creatingUser = false;
		}
	}

	async function deleteUserConfirm(): Promise<void> {
		const target = confirmDelete;
		if (!target) return;
		try {
			const res = await deleteUser(target.id);
			if (res.ok) {
				toast.success(`Usuario ${target.username} eliminado`);
				confirmOpen = false;
				confirmDelete = null;
				await loadUsers();
			} else if (res.status === 409) {
				// guard del backend (self / último admin)
				toast.error('No se puede borrar a ese usuario', {
					description: res.error ?? 'Debe quedar al menos un administrador activo.'
				});
				confirmOpen = false;
				confirmDelete = null;
			} else if (res.status === 404) {
				toast.warning('Ese usuario ya no existe');
				confirmOpen = false;
				confirmDelete = null;
				await loadUsers();
			} else {
				toast.error(`Error al eliminar (${res.status})`);
			}
		} catch {
			toast.error('Backend no disponible');
		}
	}

	function askPassword(u: User): void {
		pwTarget = u;
		pwValue = '';
		pwOpen = true;
	}

	async function submitPassword(e: SubmitEvent): Promise<void> {
		e.preventDefault();
		const target = pwTarget;
		if (!target || pwValue.length < 8) return;
		try {
			const res = await updateUserPassword(target.id, pwValue);
			if (res.ok) {
				toast.success(`Contraseña de ${target.username} actualizada`, {
					description: 'Ya funciona en el próximo login (la sesión actual no cambia).'
				});
				pwOpen = false;
				pwTarget = null;
				pwValue = '';
			} else {
				toast.error(`No se pudo actualizar (${res.status})`, {
					description: res.error ?? undefined
				});
			}
		} catch {
			toast.error('Backend no disponible');
		}
	}

	// carga al abrir (el consumidor monta/desmonta con bind de open)
	$effect(() => {
		if (!open) return;
		void loadUsers();
	});
</script>

<Dialog bind:open title="Usuarios del sistema" contentClass="w-[min(96vw,920px)] max-h-[86dvh]">
	{#if open}
		<div class="users-modal-grid">
			<section class="users-list-area" aria-label="Usuarios existentes">
				<div class="mb-3 flex flex-wrap items-center justify-between gap-2">
					<div>
						<p class="font-display text-sm font-semibold uppercase tracking-[0.14em]">Usuarios</p>
						<p class="mt-1 text-xs text-muted-foreground">{users.length} cuenta(s) · {adminCount} administrador(es)</p>
					</div>
				</div>
				<div class="overflow-x-auto rounded-lg border border-border">
				<Table class="min-w-[560px]">
				<thead>
					<TableRow class="bg-muted/40">
						<TableHead>Usuario</TableHead>
						<TableHead>Rol</TableHead>
						<TableHead>Creado</TableHead>
						<TableHead class="text-right">Acciones</TableHead>
					</TableRow>
				</thead>
				<tbody>
					{#each users as u (u.id)}
						{@const reason = deleteDisabledReason(u)}
						<TableRow>
							<TableCell class="font-medium">
								{u.username}
								{#if u.username === auth.user?.username}
									<span class="ml-1 text-[10px] text-muted-foreground">(tú)</span>
								{/if}
							</TableCell>
							<TableCell>
								<Badge variant={u.role === 'admin' ? 'default' : 'outline'}>
									{u.role}
								</Badge>
							</TableCell>
							<TableCell class="whitespace-nowrap text-muted-foreground">
								{fmtTs(u.created_at)}
							</TableCell>
							<TableCell class="text-right">
								<div class="flex justify-end gap-1 whitespace-nowrap">
									<Button variant="action" size="sm" onclick={() => askPassword(u)}>
										<KeyRound size={14} strokeWidth={1.8} aria-hidden="true" />
										Clave
									</Button>
									<Button
										variant="destructive-ghost"
										size="sm"
										disabled={reason !== ''}
										title={reason}
										onclick={() => { confirmDelete = u; confirmOpen = true; }}
									>
										<Trash2 size={14} aria-hidden="true" />
										Eliminar
									</Button>
								</div>
							</TableCell>
						</TableRow>
					{:else}
						<TableRow>
							<TableCell colspan={4}>
								<p class="py-6 text-center text-sm text-muted-foreground">Sin usuarios.</p>
							</TableCell>
						</TableRow>
					{/each}
				</tbody>
			</Table>
				</div>
			</section>

			<!-- Alta de usuario (misma lógica que /admin) -->
			<Card class="users-create-area h-fit">
				<CardHeader>
					<CardTitle class="flex items-center gap-2 text-base">
						<UserRoundPlus size={17} class="text-secondary" aria-hidden="true" />
						Nuevo usuario
					</CardTitle>
					<CardDescription>Invita a alguien a consultar o administrar el sistema.</CardDescription>
				</CardHeader>
				<CardContent>
				<form class="grid gap-3" onsubmit={submitUser}>
						<div class="grid gap-3">
						<div class="grid gap-1.5">
							<Label for="um-username">Usuario</Label>
							<Input id="um-username" bind:value={uUsername} required autocomplete="off" />
						</div>
						<div class="grid gap-1.5">
							<Label for="um-password">Contraseña</Label>
							<Input id="um-password" bind:value={uPassword} type="password" required minlength={8} />
						</div>
						<div class="grid gap-1.5">
							<Label for="um-role">Rol</Label>
							<select
								id="um-role"
								bind:value={uRole}
								class="h-10 w-full rounded-lg border border-input bg-background px-3 text-sm text-foreground
									focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
							>
								<option value="viewer">viewer (solo lectura)</option>
								<option value="admin">admin (todo)</option>
							</select>
						</div>
					</div>
					<p class="text-xs text-muted-foreground">Las claves deben tener al menos 8 caracteres.</p>
					<div class="flex justify-end border-t border-border/60 pt-3">
						<Button type="submit" disabled={creatingUser}>
							<UserRoundPlus size={15} aria-hidden="true" />
							{creatingUser ? 'Creando…' : 'Crear usuario'}
						</Button>
					</div>
				</form>
				</CardContent>
			</Card>
		</div>
	{/if}
</Dialog>

<!-- Confirmación de borrado de usuario -->
<Dialog bind:open={confirmOpen} title="Eliminar usuario">
	<div class="grid gap-4">
		<p class="text-sm text-muted-foreground">
			Se eliminará a <span class="font-medium text-foreground">{confirmDelete?.username}</span>
			({confirmDelete?.role}). Esta acción no se puede deshacer.
		</p>
			<div class="flex justify-end gap-2">
			<Button variant="action" onclick={() => (confirmOpen = false)}>Cancelar</Button>
			<Button variant="destructive" onclick={() => void deleteUserConfirm()}>Eliminar</Button>
		</div>
	</div>
</Dialog>

<!-- Cambio de contraseña (PUT /users/{id}/password) -->
<Dialog bind:open={pwOpen} title={`Cambiar contraseña — ${pwTarget?.username ?? ''}`}>
	{#if pwOpen && pwTarget}
		<form class="grid gap-4" onsubmit={submitPassword}>
			<p class="text-sm text-muted-foreground">
				Mínimo 8 caracteres. El dueño del usuario la cambia en su próximo login.
			</p>
			<div class="grid gap-2">
				<Label for="um-new-password">Nueva contraseña</Label>
				<Input
					id="um-new-password"
					bind:value={pwValue}
					type="password"
					required
					minlength={8}
					pattern={'.{8,}'}
					title="Mínimo 8 caracteres"
				/>
			</div>
			<div class="flex justify-end gap-2">
				<Button variant="action" type="button" onclick={() => { pwOpen = false; pwTarget = null; }}>
					Cancelar
				</Button>
				<Button type="submit">Guardar contraseña</Button>
			</div>
		</form>
	{/if}
</Dialog>
