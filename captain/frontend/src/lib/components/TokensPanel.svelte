<script lang="ts">
	import Anchor from '@lucide/svelte/icons/anchor';
	import Copy from '@lucide/svelte/icons/copy';
	import { toast } from 'svelte-sonner';
	import {
		revokeToken,
		listTokens,
		listAgents,
		putTokenTags
	} from '#lib/api/client.js';
	import type { AgentToken } from '#lib/api/types.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Dialog } from '#lib/components/ui/dialog/index.js';
	import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '#lib/components/ui/card/index.js';
	import TagInput from '#lib/components/TagInput.svelte';
	import TokenCreateModal from '#lib/components/TokenCreateModal.svelte';
	import VPSConnectInstructions from '#lib/components/VPSConnectInstructions.svelte';
	import {
		Table,
		TableCell,
		TableHead,
		TableRow
	} from '#lib/components/ui/table/index.js';
	import { fmtTs } from '#lib/format.js';

	/*
 * Panel de gestión de VPS tokens: token completo SIEMPRE
	 * visible/copiable en la lista (sin modal one-time), tags con chips
	 * (regex a-z0-9-_ ≤24, máx 5) editables inline vía PUT /tokens/{id}/tags,
	 * revoke SOFT con confirm ("el agente ligado pasa a dado de baja; el
	 * registro queda en la base") y el instalador único de Stowaway por VPS.
	 * Es el MISMO bloque que usaba la tab Tokens de /admin (hoy redirect a /):
	 * lo usan la barra global (TokensManageModal) y el panel embebido, sin duplicar.
	 */
	let tokens = $state<AgentToken[]>([]);
	/** agent_id → nombre (para la columna "Agent" sin join del backend). */
	let agentNames = $state<Record<number, string>>({});
	/** Creador de tokens como modal ("más al alcance"): mismo componente. */
	let createTokenOpen = $state(false);
	let showRevoked = $state(false);
	const activeCount = $derived(tokens.filter((token) => token.status === 'active').length);
	const revokedCount = $derived(tokens.length - activeCount);
	const visibleTokens = $derived(showRevoked ? tokens : tokens.filter((token) => token.status === 'active'));

	// edición inline de tags: una fila a la vez (chips reusados vía TagInput)
	let editingTagId = $state<number | null>(null);
	let editingTagDraft = $state<string[]>([]);
	let editingTagInput = $state('');
	let savingTags = $state(false);

	// revoke SOFT con confirmación
	let revokeTarget = $state<AgentToken | null>(null);
	let revokeOpen = $state(false);

	// Instructions for installing Stowaway or migrating a grandfathered token.
	let instructionsToken = $state<AgentToken | null>(null);
	let instructionsOpen = $state(false);

	async function loadTokens(): Promise<void> {
		try {
			const [res, ags] = await Promise.all([listTokens(), listAgents()]);
			if (res.ok) tokens = res.data ?? [];
			else toast.error(`No se pudieron listar tokens (${res.status})`);
			// mapa id→nombre para la columna Agent (el token trae solo agent_id)
			const names: Record<number, string> = {};
			for (const a of ags.data ?? []) names[a.id] = a.name;
			agentNames = names;
		} catch {
			toast.error('Backend no disponible');
		}
	}

	function startTagEdit(t: AgentToken): void {
		editingTagId = t.id;
		editingTagDraft = [...(t.tags ?? [])];
		editingTagInput = '';
	}

	async function saveTagEdit(): Promise<void> {
		const t = tokens.find((x) => x.id === editingTagId);
		if (!t) return;
		savingTags = true;
		try {
			const res = await putTokenTags(t.id, editingTagDraft);
			if (res.ok) {
				toast.success('Tags actualizados');
				editingTagId = null;
				await loadTokens();
			} else {
				toast.error(`No se pudieron guardar los tags (${res.status})`, {
					description: res.error ?? undefined
				});
			}
		} catch {
			toast.error('Backend no disponible');
		} finally {
			savingTags = false;
		}
	}

	/** El TokenCreateModal avisa: toast + refetch de la lista. */
	function onTokenCreated(t: { name: string }): void {
		toast.success(`Token ${t.name} creado`, {
			description: 'Ya está en la lista — cópialo cuando quieras.'
		});
		void loadTokens();
	}

	/** Copiar genérico con fallback textarea (sin permisos Clipboard API). */
	async function copyText(raw: string, okMsg = 'Copiado al portapapeles'): Promise<void> {
		try {
			await navigator.clipboard.writeText(raw);
			toast.success(okMsg);
		} catch {
			const ta = document.createElement('textarea');
			ta.value = raw;
			ta.style.position = 'fixed';
			ta.style.opacity = '0';
			document.body.appendChild(ta);
			ta.select();
			try {
				document.execCommand('copy');
				toast.success(okMsg);
			} catch {
				toast.error('No se pudo copiar — selecciónalo manualmente');
			} finally {
				ta.remove();
			}
		}
	}

	function tokenPreview(token: string): string {
		return `••••••••••••${token.slice(-4)}`;
	}

	function askRevoke(t: AgentToken): void {
		revokeTarget = t;
		revokeOpen = true;
	}

	async function revokeTokenConfirm(): Promise<void> {
		const t = revokeTarget;
		if (!t) return;
		try {
			const res = await revokeToken(t.id);
			if (res.ok) {
				toast.success(`${t.name} dado de baja`, {
						description: t.vps_scoped
						? 'Todos los contenedores de esa VPS salen del monitoreo; se conserva el historial.'
							: t.enrollment
								? 'Se revocan todos los agentes que adoptaron esta credencial legacy; el historial se conserva.'
								: 'La credencial legacy queda revocada y su historial se conserva.'
				});
				revokeOpen = false;
				revokeTarget = null;
				await loadTokens();
			} else if (res.status === 404) {
				toast.warning('Ese token ya no existe');
				revokeOpen = false;
				revokeTarget = null;
				await loadTokens();
			} else {
				toast.error(`No se pudo dar de baja (${res.status})`);
			}
		} catch {
			toast.error('Backend no disponible');
		}
	}

	// carga inicial (el consumidor decide cuándo monta el panel: página o modal)
	$effect(() => {
		void loadTokens();
	});
</script>

<Card>
	<CardHeader class="flex-row items-start justify-between gap-3 space-y-0">
		<div class="grid gap-1.5">
				<CardTitle class="text-base">Acceso de las VPS</CardTitle>
			<CardDescription>
					Un token de Stowaway por VPS externa: reporta sus contenedores en ejecución sin editar sus Compose. Los tokens anteriores sin alcance VPS quedan marcados como legacy.
			</CardDescription>
		</div>
		<Button onclick={() => (createTokenOpen = true)}>
			<Anchor size={15} strokeWidth={1.8} class="text-secondary" aria-hidden="true" />
			Conectar VPS
		</Button>
	</CardHeader>
	<CardContent class="grid gap-3">
		<div class="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border/70 bg-muted/20 px-3 py-2">
			<p class="text-xs text-muted-foreground">
				<span class="font-medium text-foreground">{activeCount} activos</span>
				<span class="mx-1.5 text-border">·</span>
				{revokedCount} dados de baja ocultos
			</p>
			<Button variant="ghost" size="sm" onclick={() => (showRevoked = !showRevoked)}>
				{showRevoked ? 'Mostrar solo activos' : `Incluir dados de baja (${revokedCount})`}
			</Button>
		</div>
		<div class="overflow-x-auto rounded-lg border border-border">
		<Table class="min-w-[660px]">
			<thead>
				<TableRow class="bg-muted/40">
					<TableHead>Nombre</TableHead>
					<TableHead>Token</TableHead>
					<TableHead>Tags</TableHead>
					<TableHead>Estado</TableHead>
					<TableHead>VPS · contenedores</TableHead>
					<TableHead class="text-right">Acciones</TableHead>
				</TableRow>
			</thead>
			<tbody>
				{#each visibleTokens as t (t.id)}
					<TableRow>
						<TableCell class="min-w-[6rem]" title={`Creado ${fmtTs(t.created_at)}`}>
							<div class="flex flex-wrap items-center gap-x-2 gap-y-1">
								<span class="font-medium">{t.name}</span>
								{#if t.vps_scoped}
									<span
										class="whitespace-nowrap rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-center text-[10px] uppercase tracking-wide text-primary"
										title="Credencial única de la VPS; Stowaway reporta los contenedores en ejecución"
									>
										VPS
									</span>
								{:else}
									<span class="whitespace-nowrap rounded-full border border-border px-2 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground" title="Credencial anterior conservada para compatibilidad; crear una nueva por VPS">Migración</span>
								{/if}
							</div>
						</TableCell>
						<!-- Credencial oculta por defecto; tocarla copia el valor íntegro. -->
						<TableCell>
							<button
								type="button"
								class="token-copy-trigger group"
								aria-label={`Copiar token de ${t.name}`}
								title={`Tocar para copiar el token de ${t.name}`}
								onclick={() => void copyText(t.token, `${t.name}: token copiado`)}
							>
								<code class="truncate font-mono text-xs">{tokenPreview(t.token)}</code>
								<Copy size={13} class="token-copy-icon shrink-0" aria-hidden="true" />
							</button>
						</TableCell>
						<!-- tags: chips clicables → edición inline (PUT /tags), chips reusados vía TagInput -->
						<TableCell>
							{#if editingTagId === t.id}
								<div class="grid w-full min-w-[200px] gap-2">
									<TagInput
										bind:tags={editingTagDraft}
										bind:input={editingTagInput}
										inputClass="h-7 w-24 rounded-md border border-input bg-background px-2 text-xs text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
										hint={false}
									/>
									<div class="flex gap-1">
										<Button
											variant="action"
											size="sm"
											disabled={savingTags}
											onclick={() => void saveTagEdit()}
										>
											{savingTags ? 'Guardando…' : 'Guardar'}
										</Button>
										<Button
											variant="action"
											size="sm"
											onclick={() => (editingTagId = null)}
										>
											Cancelar
										</Button>
									</div>
								</div>
							{:else if t.tags && t.tags.length > 0}
								<button
									type="button"
									class="flex flex-wrap items-center gap-1"
									title="Editar tags"
									onclick={() => startTagEdit(t)}
								>
									{#each t.tags as tg (tg)}
										<span
											class="whitespace-nowrap rounded-full border border-secondary/40 bg-secondary/10 px-2 py-0.5 text-[11px] text-secondary"
										>
											{tg}
										</span>
									{/each}
								</button>
							{:else}
								<button
									type="button"
									class="text-[11px] text-muted-foreground underline-offset-2 hover:underline"
									onclick={() => startTagEdit(t)}
								>
									+ tags
								</button>
							{/if}
						</TableCell>
						<TableCell class="whitespace-nowrap">
							<Badge variant={t.status === 'active' ? 'default' : 'outline'} class="whitespace-nowrap">
								{t.status === 'active' ? 'activo' : 'dado de baja'}
							</Badge>
						</TableCell>
						<TableCell>
							{#if t.vps_scoped}
								<div class="grid gap-1 text-muted-foreground" title="Docker Engine ID enlazado al primer reporte">
									<span>{t.adopted_count} contenedor{t.adopted_count === 1 ? '' : 'es'}</span>
									<span class="max-w-[140px] truncate text-[10px]">{t.vps_id ? `Engine ${t.vps_id}` : 'Esperando a Stowaway'}</span>
								</div>
							{:else if t.enrollment}
								<div class="grid gap-1 text-muted-foreground" title="Credencial previa sin asociación exclusiva a una VPS">
									<span>LEGACY · {t.adopted_count} agente{t.adopted_count === 1 ? '' : 's'}</span>
									<span class="text-[10px]">Sin vínculo exclusivo a VPS</span>
								</div>
							{:else}
								{#if t.agent_id != null}
								<span class="max-w-[110px] truncate" title={agentNames[t.agent_id] ?? ('#' + t.agent_id)}>
									{agentNames[t.agent_id] ?? ('#' + t.agent_id)}
								</span>
							{:else}—{/if}
							{/if}
						</TableCell>
						<TableCell class="text-right">
							<div class="flex justify-end gap-1 whitespace-nowrap">
								<Button variant="action" size="sm" onclick={() => { instructionsToken = t; instructionsOpen = true; }}>
									{t.vps_scoped ? 'Instalar Stowaway' : 'Migración'}
								</Button>
								{#if t.status === 'active'}
									<Button variant="destructive-ghost"
										size="sm" onclick={() => askRevoke(t)}>
										Dar de baja
									</Button>
								{/if}
							</div>
						</TableCell>
					</TableRow>
				{:else}
					<TableRow>
						<TableCell colspan={6}>
							<p class="py-6 text-center text-sm text-muted-foreground">
								{tokens.length === 0 ? 'Todavía no hay tokens.' : 'No hay tokens para mostrar con este filtro.'}
							</p>
						</TableCell>
					</TableRow>
				{/each}
			</tbody>
		</Table>
		</div>
	</CardContent>
</Card>

<!-- Creador de tokens como modal (mismo componente que el dashboard) -->
<TokenCreateModal bind:open={createTokenOpen} onCreated={onTokenCreated} />

<!-- Confirmación de revoke SOFT (contrato v2): el agent ligado también cae -->
<Dialog bind:open={revokeOpen} title="Dar de baja token">
	<div class="grid gap-4">
		<p class="text-sm text-muted-foreground">
			Darás de baja <span class="font-medium text-foreground">{revokeTarget?.name}</span>.
			{#if revokeTarget?.vps_scoped}
				Se detendrá el reporte de todos los contenedores de esa VPS, se cerrarán sus alertas y se conservará el historial. Primero detené Stowaway en el servidor.
			{:else if revokeTarget?.enrollment}
				Se revocarán todos los agentes que adoptaron esta credencial legacy y se conservará el historial.
			{:else}
				Se revocará la credencial legacy y el registro histórico quedará conservado.
			{/if}
		</p>
		<div class="flex justify-end gap-2">
			<Button variant="action" onclick={() => { revokeOpen = false; revokeTarget = null; }}>
				Cancelar
			</Button>
			<Button variant="destructive" onclick={() => void revokeTokenConfirm()}>Dar de baja</Button>
		</div>
	</div>
</Dialog>

<!-- VPS installation instructions; shared with the creation flow. -->
<Dialog bind:open={instructionsOpen} title={`${instructionsToken?.vps_scoped ? 'Instalar Stowaway' : 'Migrar credencial'} — ${instructionsToken?.name ?? ''}`}>
	{#if instructionsToken}
		<div class="grid gap-4">
			<VPSConnectInstructions token={instructionsToken} />
			<div class="flex justify-end">
				<Button variant="action" onclick={() => { instructionsOpen = false; instructionsToken = null; }}>
					Cerrar
				</Button>
			</div>
		</div>
	{/if}
</Dialog>
