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

	async function setIgnored(agent: Agent, ignored: boolean): Promise<void> {
		updatingAgentID = agent.id;
		try {
			const result = await putAgentMonitoringIgnored(agent.id, ignored);
			if (!result.ok) {
				toast.error(`No se pudo actualizar el monitoreo (${result.status})`, { description: result.error ?? undefined });
				return;
			}
			if (ignored) {
				toast.success('Contenedor excluido', { description: `Se resolvieron ${result.data?.alerts_resolved ?? 0} alerta(s); sus compañeros en la VPS siguen vigilados.` });
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
			<h1 class="route-title break-all">{groupName}</h1>
			<p class="route-copy">Contenedores reportados por esta VPS. La identidad del Docker Engine la mantiene separada aunque comparta hostname con otra. Las etiquetas manuales clasifican cada contenedor sin modificar las del token.</p>
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
		<Card class="vps-profile-stat vps-profile-stat--alerts"><span>Alertas abiertas</span><strong>{alertCount.toString().padStart(2, '0')}</strong></Card>
		{#if isAdmin}<Card class="vps-profile-stat"><span>Excluidos</span><strong>{ignoredAgents.length.toString().padStart(2, '0')}</strong></Card>{/if}
	</section>

	{#if isAdmin}
		<NotificationPolicyControls scope="vps" scopeId={key} />
	{/if}

	<section class="grid gap-3" aria-labelledby="vps-containers-title">
		<div class="flex flex-wrap items-end justify-between gap-3">
			<div class="flex items-center gap-2"><Server size={17} class="text-secondary" aria-hidden="true" /><div><p class="page-subtitle font-display uppercase tracking-[0.14em]">Inventario del servidor</p><h2 id="vps-containers-title" class="mt-1 font-display text-base font-semibold">Contenedores de esta VPS</h2></div></div>
			<span class="text-xs text-muted-foreground">{visibleAgents.length} de {monitoredAgents.length} en monitoreo</span>
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
						<AgentCard {agent} />
						{#if isAdmin}
							<ContainerTagsEditor {agent} {suggestions} />
							<div class="flex justify-end px-1">
								<Button variant="ghost" size="sm" onclick={() => askIgnore(agent)} disabled={updatingAgentID === agent.id}>
									<EyeOff size={14} aria-hidden="true" />
									Ignorar este contenedor
								</Button>
							</div>
						{/if}
					</div>
				{/each}
			</div>
		{:else}
			<Card class="vps-empty">
				{#if waiting}<p class="font-display text-sm uppercase tracking-[0.12em]">Trazando los contenedores…</p>
				{:else if groupAgents.length === 0}<p class="font-display text-sm uppercase tracking-[0.12em]">No se encontraron contenedores</p><p class="mt-1 text-sm text-muted-foreground">El host puede haber cambiado su hostname o todavía no reportó.</p>
				{:else if monitoredAgents.length === 0}<p class="font-display text-sm uppercase tracking-[0.12em]">Todos los contenedores están excluidos</p><p class="mt-1 text-sm text-muted-foreground">Podés volver a incluirlos desde la lista de abajo.</p>
				{:else}<p class="font-display text-sm uppercase tracking-[0.12em]">Ningún contenedor coincide</p><p class="mt-1 text-sm text-muted-foreground">Quitá algún filtro de etiqueta para volver a ver el inventario.</p>{/if}
			</Card>
		{/if}
	</section>

	{#if isAdmin && ignoredAgents.length > 0}
		<section class="grid gap-3" aria-labelledby="vps-ignored-title">
			<div>
				<p class="page-subtitle font-display uppercase tracking-[0.14em]">Reglas locales</p>
				<h2 id="vps-ignored-title" class="mt-1 font-display text-base font-semibold">Contenedores excluidos ({ignoredAgents.length})</h2>
				<p class="mt-1 text-sm text-muted-foreground">Siguen identificados para poder reincorporarlos; no generan métricas nuevas ni alertas.</p>
			</div>
			<div class="grid gap-2">
				{#each ignoredAgents as agent (agent.id)}
					<Card class="flex flex-wrap items-center justify-between gap-3 p-3">
						<div class="min-w-0">
							<div class="flex flex-wrap items-center gap-2"><h3 class="truncate font-medium">{agent.name}</h3><Badge variant="outline">IGNORADO</Badge></div>
							<p class="mt-1 truncate font-mono text-xs text-muted-foreground">{agent.container_id ?? `agent #${agent.id}`}</p>
						</div>
						<Button variant="action" size="sm" onclick={() => void setIgnored(agent, false)} disabled={updatingAgentID === agent.id}>
							<Eye size={14} aria-hidden="true" />
							{updatingAgentID === agent.id ? 'Actualizando…' : 'Volver a incluir'}
						</Button>
					</Card>
				{/each}
			</div>
		</section>
	{/if}
</div>

<Dialog bind:open={ignoreDialogOpen} title="Excluir contenedor del monitoreo">
	<div class="grid gap-4">
		<p class="text-sm text-muted-foreground">
			<span class="font-medium text-foreground">{ignoreTarget?.name}</span> seguirá apareciendo en la lista de excluidos, pero no recibirá nuevas métricas ni alertas de caída o consumo. Los demás contenedores de esta VPS no cambian; las alertas abiertas se resolverán y el historial se conservará.
		</p>
		<div class="flex justify-end gap-2">
			<Button variant="action" onclick={() => { ignoreDialogOpen = false; ignoreTarget = null; }}>Cancelar</Button>
			<Button variant="action" onclick={() => ignoreTarget && void setIgnored(ignoreTarget, true)} disabled={!ignoreTarget || updatingAgentID === ignoreTarget.id}>
				{updatingAgentID === ignoreTarget?.id ? 'Actualizando…' : 'Excluir contenedor'}
			</Button>
		</div>
	</div>
</Dialog>
