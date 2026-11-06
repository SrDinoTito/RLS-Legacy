<script lang="ts">
	import { page } from '$app/state';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Server from '@lucide/svelte/icons/server';
	import Eye from '@lucide/svelte/icons/eye';
	import EyeOff from '@lucide/svelte/icons/eye-off';
	import { toast } from 'svelte-sonner';
	import { goto } from '$app/navigation';
	import { auth } from '#lib/stores/auth.svelte.js';
	import { sse, refreshAgents } from '#lib/stores/sse.svelte.js';
	import AgentCard from '#lib/components/AgentCard.svelte';
	import AgentDetailModal from '#lib/components/AgentDetailModal.svelte';
	import ContainerTagsEditor from '#lib/components/ContainerTagsEditor.svelte';
	import NotificationPolicyControls from '#lib/components/NotificationPolicyControls.svelte';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Card } from '#lib/components/ui/card/index.js';
	import { allAgentTags } from '#lib/tags.js';
	import { agentBelongsToVPS, groupAgentsByVPS, UNKNOWN_VPS_KEY } from '#lib/vps.js';
	import { putAgentMonitoringIgnored } from '#lib/api/client.js';
	import type { Agent } from '#lib/api/types.js';
	import { Dialog } from '#lib/components/ui/dialog/index.js';

	const key = $derived(page.params.hostname ?? '');
	const agents = $derived(sse.list());
	const group = $derived(groupAgentsByVPS(agents).find((item) => item.key === key));
	const groupAgents = $derived(agents.filter((agent) => agentBelongsToVPS(agent, key)));
	const monitoredAgents = $derived(groupAgents.filter((agent) => !agent.monitoring_ignored));
	const ignoredAgents = $derived(groupAgents.filter((agent) => agent.monitoring_ignored));
	const groupName = $derived(group?.name ?? (key === UNKNOWN_VPS_KEY ? 'Sin hostname' : key));
	const isAdmin = $derived(auth.user?.role === 'admin');
	const onlineCount = $derived(monitoredAgents.filter((agent) => agent.status === 'online').length);
	const offlineCount = $derived(monitoredAgents.length - onlineCount);
	const alertCount = $derived(monitoredAgents.reduce((total, agent) => total + (agent.alert_count ?? 0), 0));
	const tagCounts = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const agent of monitoredAgents) for (const tag of allAgentTags(agent)) counts.set(tag, (counts.get(tag) ?? 0) + 1);
		return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]));
	});
	const suggestions = $derived([...new Set(monitoredAgents.flatMap(allAgentTags))].sort());
	const waiting = $derived(agents.length === 0 && (sse.loading || sse.status !== 'live'));
	let selectedTags = $state<string[]>([]);
	let ignoreTarget = $state<Agent | null>(null);
	let ignoreDialogOpen = $state(false);
	let updatingAgentID = $state<number | null>(null);
	let detailAgentID = $state<number | null>(null);
	let detailOpen = $state(false);
	const detailAgent = $derived(detailAgentID == null ? null : (groupAgents.find((agent) => agent.id === detailAgentID) ?? null));
	const visibleAgents = $derived.by(() => selectedTags.length === 0
		? monitoredAgents
		: monitoredAgents.filter((agent) => allAgentTags(agent).some((tag) => selectedTags.includes(tag))));

	function toggleTag(tag: string): void {
		selectedTags = selectedTags.includes(tag) ? selectedTags.filter((item) => item !== tag) : [...selectedTags, tag];
	}

	function askIgnore(agent: Agent): void {
		ignoreTarget = agent;
		ignoreDialogOpen = true;
	}

	function openAgentDetails(agent: Agent): void {
		detailAgentID = agent.id;
		detailOpen = true;
	}

	async function setIgnored(agent: Agent, ignored: boolean): Promise<void> {
		updatingAgentID = agent.id;
		try {
			const result = await putAgentMonitoringIgnored(agent.id, ignored);
			if (!result.ok) {
				toast.error(`No se pudo actualizar el monitoreo (${result.status})`, { description: result.error ?? undefined });
				return;
			}
			if (ignored) {
				toast.success('Contenedor ignorado', { description: `Se cerraron ${result.data?.alerts_resolved ?? 0} alerta(s); el resto de la VPS sigue en monitoreo.` });
				ignoreDialogOpen = false;
				ignoreTarget = null;
			} else {
				toast.success('Contenedor incluido en el monitoreo');
			}
			await refreshAgents();
		} catch {
			toast.error('No se pudo conectar con el backend');
		} finally {
			updatingAgentID = null;
		}
	}
</script>

<svelte:head><title>{groupName} · VPS — RLS Legacy</title></svelte:head>

<div class="grid gap-6">
	<header class="route-heading">
		<div class="min-w-0">
			<p class="route-kicker">VPS · perfil de servidor</p>
			<h1 class="route-title break-words">{groupName}</h1>
			<p class="route-copy">Estado, etiquetas e historial de los contenedores reportados por esta VPS.</p>
		</div>
		<div class="flex flex-wrap items-center gap-2">
			<Badge variant={sse.status === 'live' ? 'secondary' : 'outline'}>{sse.status === 'live' ? 'DATOS EN VIVO' : sse.status.toUpperCase()}</Badge>
			<Button variant="action" size="sm" onclick={() => void refreshAgents()} disabled={sse.loading}><RefreshCw size={14} class={sse.loading ? 'animate-spin' : ''} aria-hidden="true" />Actualizar</Button>
			<Button variant="action" size="sm" onclick={() => goto('/vps')}><ArrowLeft size={14} aria-hidden="true" />Todas las VPS</Button>
		</div>
	</header>

	<section class="vps-profile-summary" aria-label="Resumen de la VPS">
		<Card class="vps-profile-stat"><span>En monitoreo</span><strong>{monitoredAgents.length.toString().padStart(2, '0')}</strong></Card>
		<Card class="vps-profile-stat vps-profile-stat--online"><span>En línea</span><strong>{onlineCount.toString().padStart(2, '0')}</strong></Card>
		<Card class="vps-profile-stat vps-profile-stat--offline"><span>Sin señal</span><strong>{offlineCount.toString().padStart(2, '0')}</strong></Card>
		<Card class="vps-profile-stat vps-profile-stat--alerts"><span>Alertas abiertas</span><strong>{alertCount.toString().padStart(2, '0')}</strong></Card>
		{#if isAdmin}<Card class="vps-profile-stat"><span>Ignorados</span><strong>{ignoredAgents.length.toString().padStart(2, '0')}</strong></Card>{/if}
	</section>

	{#if isAdmin}
		<details class="vps-settings">
			<summary><span><strong>Avisos de esta VPS</strong><small>Preferencias de notificación para sus contenedores</small></span><span class="vps-settings-toggle">Configurar</span></summary>
			<div class="vps-settings-content"><NotificationPolicyControls scope="vps" scopeId={key} /></div>
		</details>
	{/if}

	<section class="grid gap-3" aria-labelledby="vps-containers-title">
		<div class="flex flex-wrap items-end justify-between gap-3">
			<div class="flex items-center gap-2"><Server size={17} class="text-secondary" aria-hidden="true" /><div><p class="page-subtitle font-display uppercase tracking-[0.14em]">Inventario</p><h2 id="vps-containers-title" class="mt-1 font-display text-base font-semibold">Contenedores</h2></div></div>
			<span class="text-xs text-muted-foreground">{visibleAgents.length} visibles · {monitoredAgents.length} en monitoreo</span>
		</div>
		{#if tagCounts.length > 0}
			<div class="vps-tag-filter" aria-label="Filtrar contenedores por etiqueta">
				<button type="button" class:active={selectedTags.length === 0} onclick={() => (selectedTags = [])}>Todas <span>{monitoredAgents.length}</span></button>
				{#each tagCounts as [tag, count] (tag)}
					<button type="button" class:active={selectedTags.includes(tag)} onclick={() => toggleTag(tag)}>{tag} <span>{count}</span></button>
				{/each}
			</div>
		{/if}
		{#if visibleAgents.length > 0}
			<div class="vps-container-grid">
				{#each visibleAgents as agent (agent.id)}
					<div class="vps-container-entry">
						<AgentCard {agent} onDetails={() => openAgentDetails(agent)} />
						{#if isAdmin}
							<details class="vps-container-settings">
								<summary>Etiquetas y monitoreo<span>Configurar</span></summary>
								<div class="vps-container-settings-body">
									<ContainerTagsEditor {agent} {suggestions} />
									<div class="flex justify-end">
										<Button variant="ghost" size="sm" onclick={() => askIgnore(agent)} disabled={updatingAgentID === agent.id}>
											<EyeOff size={14} aria-hidden="true" />
											Ignorar monitoreo
										</Button>
									</div>
								</div>
							</details>
						{/if}
					</div>
				{/each}
			</div>
		{:else}
			<Card class="vps-empty">
				{#if waiting}<p class="font-display text-sm uppercase tracking-[0.12em]">Trazando los contenedores…</p>
				{:else if groupAgents.length === 0}<p class="font-display text-sm uppercase tracking-[0.12em]">No se encontraron contenedores</p><p class="mt-1 text-sm text-muted-foreground">El host puede haber cambiado su hostname o todavía no reportó.</p>
				{:else if monitoredAgents.length === 0}<p class="font-display text-sm uppercase tracking-[0.12em]">Todos los contenedores están ignorados</p><p class="mt-1 text-sm text-muted-foreground">Puedes volver a monitorearlos desde la lista de abajo.</p>
				{:else}<p class="font-display text-sm uppercase tracking-[0.12em]">Ningún contenedor coincide</p><p class="mt-1 text-sm text-muted-foreground">Quitá algún filtro de etiqueta para volver a ver el inventario.</p>{/if}
			</Card>
		{/if}
	</section>

	{#if ignoredAgents.length > 0}
		<section class="grid gap-3" aria-labelledby="vps-ignored-title">
			<div>
				<p class="page-subtitle font-display uppercase tracking-[0.14em]">Excepciones de monitoreo</p>
				<h2 id="vps-ignored-title" class="mt-1 font-display text-base font-semibold">Contenedores ignorados ({ignoredAgents.length})</h2>
				<p class="mt-1 text-sm text-muted-foreground">Siguen en el inventario; no reciben métricas nuevas ni generan alertas.</p>
			</div>
			<div class="grid gap-2">
				{#each ignoredAgents as agent (agent.id)}
					<Card class="vps-ignored-card">
						<div class="min-w-0">
							<div class="flex flex-wrap items-center gap-2"><h3 class="truncate font-medium">{agent.name}</h3><Badge variant="outline">IGNORADO</Badge></div>
							<p class="mt-1 truncate text-xs text-muted-foreground">{agent.hostname} · <span class="font-mono">{agent.container_id ?? `contenedor #${agent.id}`}</span></p>
						</div>
						<div class="flex flex-wrap justify-end gap-1">
							<Button variant="ghost" size="sm" onclick={() => openAgentDetails(agent)}>Ver detalle</Button>
							{#if isAdmin}
								<Button variant="action" size="sm" onclick={() => void setIgnored(agent, false)} disabled={updatingAgentID === agent.id}>
									<Eye size={14} aria-hidden="true" />
									{updatingAgentID === agent.id ? 'Actualizando…' : 'Volver a monitorear'}
								</Button>
							{/if}
						</div>
					</Card>
				{/each}
			</div>
		</section>
	{/if}
</div>

{#if detailAgent}
	<AgentDetailModal agent={detailAgent} bind:open={detailOpen} />
{/if}

<Dialog bind:open={ignoreDialogOpen} title="Ignorar contenedor">
	<div class="grid gap-4">
		<p class="text-sm text-muted-foreground">
			<span class="font-medium text-foreground">{ignoreTarget?.name}</span> seguirá apareciendo en el inventario con la marca <strong class="text-foreground">IGNORADO</strong>. No recibirá métricas nuevas ni generará alertas; los demás contenedores de esta VPS no cambian y el historial existente se conserva.
		</p>
		<div class="flex justify-end gap-2">
			<Button variant="action" onclick={() => { ignoreDialogOpen = false; ignoreTarget = null; }}>Cancelar</Button>
			<Button variant="action" onclick={() => ignoreTarget && void setIgnored(ignoreTarget, true)} disabled={!ignoreTarget || updatingAgentID === ignoreTarget.id}>
				{updatingAgentID === ignoreTarget?.id ? 'Actualizando…' : 'Ignorar monitoreo'}
			</Button>
		</div>
	</div>
</Dialog>
