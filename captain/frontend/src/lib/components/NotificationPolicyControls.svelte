<script lang="ts">
	import { toast } from 'svelte-sonner';
	import {
		getNotificationPolicy,
		putNotificationPolicy,
		resetNotificationPolicy
	} from '#lib/api/client.js';
	import type { NotificationPolicy, NotificationPolicyValues } from '#lib/api/types.js';
	import { Button } from '#lib/components/ui/button/index.js';

	let {
		scope,
		scopeId,
		compact = false
	}: {
		scope: 'vps' | 'agent';
		scopeId: string | number;
		compact?: boolean;
	} = $props();

	let policy = $state<NotificationPolicy | null>(null);
	let loading = $state(false);
	let saving = $state(false);
	let loadedKey = $state('');

	$effect(() => {
		const nextScope = scope;
		const nextId = String(scopeId);
		const nextKey = `${nextScope}:${nextId}`;
		if (!nextId || loadedKey === nextKey) return;
		loadedKey = nextKey;
		void load(nextScope, nextId);
	});

	async function load(nextScope = scope, nextId = String(scopeId)): Promise<void> {
		loading = true;
		try {
			const result = await getNotificationPolicy(nextScope, nextId);
			if (!result.ok || !result.data) {
				policy = null;
				toast.error('No se pudo leer la política de notificaciones', {
					description: result.error ?? `Error HTTP ${result.status}`
				});
				return;
			}
			policy = result.data;
		} catch {
			policy = null;
			toast.error('No se pudo conectar con el backend');
		} finally {
			loading = false;
		}
	}

	async function save(patch: Partial<NotificationPolicyValues>): Promise<void> {
		if (!policy || saving) return;
		saving = true;
		const values: NotificationPolicyValues = {
			notifications_enabled: patch.notifications_enabled ?? policy.notifications_enabled,
			resource_alerts_enabled: patch.resource_alerts_enabled ?? policy.resource_alerts_enabled
		};
		try {
			const result = await putNotificationPolicy(scope, scopeId, values);
			if (!result.ok || !result.data) {
				toast.error('No se pudo guardar la política', {
					description: result.error ?? `Error HTTP ${result.status}`
				});
				return;
			}
			policy = result.data;
			toast.success('Preferencias de avisos guardadas');
		} catch {
			toast.error('No se pudieron guardar las preferencias');
		} finally {
			saving = false;
		}
	}

	async function reset(): Promise<void> {
		if (saving) return;
		saving = true;
		try {
			const result = await resetNotificationPolicy(scope, scopeId);
			if (!result.ok) {
				toast.error('No se pudo restablecer la política', {
					description: result.error ?? `Error HTTP ${result.status}`
				});
				return;
			}
			await load();
			toast.success(scope === 'agent' ? 'El contenedor vuelve a heredar de la VPS' : 'Política VPS restablecida');
		} catch {
			toast.error('No se pudo restablecer la política');
		} finally {
			saving = false;
		}
	}
</script>

<section
	class="grid gap-3 rounded-xl border border-border/80 bg-card/70 p-3 {compact ? 'text-xs' : 'text-sm'}"
	aria-label={scope === 'vps' ? 'Notificaciones de esta VPS' : 'Notificaciones de este contenedor'}
>
	<header class="flex flex-wrap items-start justify-between gap-2">
		<div>
			<p class="font-display text-[11px] uppercase tracking-[0.12em] text-secondary">
				Avisos externos · {scope === 'vps' ? 'VPS' : 'contenedor'}
			</p>
			<p class="mt-1 text-xs text-muted-foreground">
				Discord / Telegram. Las alertas siguen visibles en el panel aunque se silencien acá.
			</p>
		</div>
		{#if policy?.inherited}
			<span class="rounded-full border border-secondary/30 px-2 py-0.5 text-[10px] text-secondary">HEREDA DE VPS</span>
		{:else if policy?.customized && scope === 'agent'}
			<span class="rounded-full border border-primary/30 px-2 py-0.5 text-[10px] text-primary">PERSONALIZADO</span>
		{/if}
	</header>

	{#if loading && !policy}
		<p class="text-xs text-muted-foreground">Cargando preferencias…</p>
	{:else if policy}
		{#if scope === 'agent' && policy.inherited}
			<p class="text-[11px] leading-relaxed text-muted-foreground">El contenedor hereda de su VPS. Al cambiar una opción, ambas quedan personalizadas; podés volver a heredar cuando quieras. El silencio general de la VPS siempre prevalece.</p>
		{/if}
		{#if scope === 'agent' && policy.vps_notifications_enabled === false}
			<p class="rounded-md border border-primary/25 bg-primary/5 px-2.5 py-2 text-[11px] text-primary">La VPS está silenciada: ningún contenedor de este servidor enviará avisos externos.</p>
		{/if}
		<div class="grid gap-2 sm:grid-cols-2">
			<label class="flex cursor-pointer items-start gap-2 rounded-lg border border-border/70 bg-background/40 p-2.5">
				<input
					class="theme-checkbox mt-0.5"
					type="checkbox"
					checked={policy.notifications_enabled}
					disabled={saving || (scope === 'agent' && policy.vps_notifications_enabled === false)}
					aria-label="Habilitar notificaciones externas para este alcance"
					onchange={(event) => void save({ notifications_enabled: event.currentTarget.checked })}
				/>
				<span>
					<strong class="block font-medium text-foreground">Notificaciones externas habilitadas</strong>
					<span class="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">Apagalo para silenciar todas las alertas de esta {scope === 'vps' ? 'VPS' : 'instancia'}.</span>
				</span>
			</label>
			<label class="flex cursor-pointer items-start gap-2 rounded-lg border border-border/70 bg-background/40 p-2.5">
				<input
					class="theme-checkbox mt-0.5"
					type="checkbox"
					checked={policy.resource_alerts_enabled}
					disabled={saving || (scope === 'agent' && policy.vps_notifications_enabled === false)}
					aria-label="Habilitar notificaciones externas de CPU y RAM"
					onchange={(event) => void save({ resource_alerts_enabled: event.currentTarget.checked })}
				/>
				<span>
					<strong class="block font-medium text-foreground">Consumo · CPU / RAM</strong>
					<span class="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">Desactivado por defecto para evitar ruido. Solo notifica si también está habilitada la opción general.</span>
				</span>
			</label>
		</div>
		{#if scope === 'agent' && policy.customized}
			<div class="flex flex-wrap items-center justify-between gap-2 border-t border-border/60 pt-2">
				<p class="text-[11px] text-muted-foreground">Este contenedor tiene una excepción a la política de su VPS.</p>
				<Button variant="action" size="sm" onclick={() => void reset()} disabled={saving}>Heredar de la VPS</Button>
			</div>
		{:else if scope === 'vps' && policy.customized}
			<div class="flex justify-end border-t border-border/60 pt-2">
				<Button variant="action" size="sm" onclick={() => void reset()} disabled={saving}>Restablecer valores por defecto</Button>
			</div>
		{/if}
	{:else}
		<p class="text-xs text-muted-foreground">No se pudieron cargar los controles.</p>
	{/if}
</section>
