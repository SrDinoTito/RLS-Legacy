<script lang="ts">
	import { page } from '$app/state';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Server from '@lucide/svelte/icons/server';
	import { toast } from 'svelte-sonner';
	import { goto } from '$app/navigation';
	import { auth } from '#lib/stores/auth.svelte.js';
	import { sse, refreshAgents } from '#lib/stores/sse.svelte.js';
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
	import {
		Table,
		TableCell,
		TableHead,
		TableRow
	} from '#lib/components/ui/table/index.js';
	import { fmtBytesPairMb, fmtPct, fmtUptime } from '#lib/format.js';

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
		for (const agent of groupAgents) for (const tag of allAgentTags(agent)) counts.set(tag, (counts.get(tag) ?? 0) + 1);
		return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]));
	});
	const suggestions = $derived([...new Set(groupAgents.flatMap(allAgentTags))].sort());
	const waiting = $derived(agents.length === 0 && (sse.loading || sse.status !== 'live'));
	let selectedTags = $state<string[]>([]);
	let ignoreTarget = $state<Agent | null>(null);
	let ignoreDialogOpen = $state(false);
	let updatingAgentID = $state<number | null>(null);
	let configAgentID = $state<number | null>(null);
	let detailAgentID = $state<number | null>(null);
	let detailOpen = $state(false);
	const detailAgent = $derived(detailAgentID == null ? null : (groupAgents.find((agent) => agent.id === detailAgentID) ?? null));
	const configAgent = $derived(configAgentID == null ? null : (groupAgents.find((agent) => agent.id === configAgentID) ?? null));
	const visibleAgents = $derived.by(() => selectedTags.length === 0
		? groupAgents
		: groupAgents.filter((agent) => allAgentTags(agent).some((tag) => selectedTags.includes(tag))));

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
			<span class="text-xs text-muted-foreground">{visibleAgents.length} visibles · {monitoredAgents.length} en monitoreo · {ignoredAgents.length} ignorados</span>
		</div>
		{#if tagCounts.length > 0}
			<div class="vps-tag-filter" aria-label="Filtrar contenedores por etiqueta">
				<button type="button" class:active={selectedTags.length === 0} onclick={() => (selectedTags = [])}>Todas <span>{groupAgents.length}</span></button>
				{#each tagCounts as [tag, count] (tag)}
					<button type="button" class:active={selectedTags.includes(tag)} onclick={() => toggleTag(tag)}>{tag} <span>{count}</span></button>
				{/each}
			</div>
		{/if}
		{#if visibleAgents.length > 0}
			<div class="vps-inventory-table">
				<Table class="fleet-table min-w-[1040px]">
					<thead>
						<TableRow class="bg-muted/40">
							<TableHead>Estado</TableHead>
							<TableHead>Contenedor</TableHead>
							<TableHead>Etiquetas</TableHead>
							<TableHead class="text-right">CPU</TableHead>
							<TableHead class="text-right">RAM</TableHead>
							<TableHead class="text-right">Uptime</TableHead>
							<TableHead>Alertas</TableHead>
							<TableHead class="text-right">Acciones</TableHead>
						</TableRow>
					</thead>
					<tbody>
						{#each visibleAgents as agent (agent.id)}
							{@const ignored = agent.monitoring_ignored}
							<TableRow class={ignored ? 'ignored-fleet-row' : ''}>
								<TableCell>
									<span class="flex items-center gap-2 whitespace-nowrap">
										<span class="status-dot {ignored ? '' : agent.status === 'online' ? 'status-dot--online' : 'status-dot--offline'}" aria-hidden="true"></span>
										<span>{ignored ? 'IGNORADO' : agent.status === 'online' ? 'ONLINE' : 'OFFLINE'}</span>
									</span>
								</TableCell>
								<TableCell class="max-w-[250px]">
									<button type="button" class="vps-container-name" onclick={() => openAgentDetails(agent)}>
										<span>{agent.name}</span>
										<small>{agent.hostname} · {agent.container_id ? agent.container_id.slice(0, 12) : `#${agent.id}`}</small>
									</button>
								</TableCell>
								<TableCell>
									<div class="flex flex-wrap gap-1">
										{#each allAgentTags(agent) as tag (`${agent.id}-${tag}`)}
											<a href={`/tags/${encodeURIComponent(tag)}`} class="vps-inventory-tag">{tag}</a>
										{:else}
											<span class="text-muted-foreground">—</span>
										{/each}
									</div>
								</TableCell>
								<TableCell class="whitespace-nowrap text-right tabular-nums">{fmtPct(agent.metrics?.cpu_pct)}</TableCell>
								<TableCell class="whitespace-nowrap text-right tabular-nums">{fmtBytesPairMb(agent.metrics?.ram_used_mb, agent.metrics?.ram_limit_mb)}</TableCell>
								<TableCell class="whitespace-nowrap text-right tabular-nums">{fmtUptime(agent.metrics?.uptime_sec)}</TableCell>
								<TableCell class="whitespace-nowrap">
									{#if ignored}<span class="text-xs text-muted-foreground">Pausadas</span>
									{:else if agent.alert_count > 0}<Badge variant="destructive">{agent.alert_count} alerta(s)</Badge>
									{:else}<span class="text-muted-foreground">—</span>{/if}
								</TableCell>
								<TableCell class="whitespace-nowrap text-right">
									<div class="flex flex-wrap justify-end gap-1">
										<Button variant="ghost" size="sm" onclick={() => openAgentDetails(agent)}>Historial</Button>
										{#if isAdmin}
											<Button variant="action" size="sm" onclick={() => (configAgentID = configAgentID === agent.id ? null : agent.id)}>Configurar</Button>
											{#if ignored}
												<Button variant="action" size="sm" onclick={() => void setIgnored(agent, false)} disabled={updatingAgentID === agent.id}>{updatingAgentID === agent.id ? 'Actualizando…' : 'Restaurar'}</Button>
											{:else}
												<Button variant="ghost" size="sm" onclick={() => askIgnore(agent)} disabled={updatingAgentID === agent.id}>Ignorar</Button>
											{/if}
										{/if}
									</div>
								</TableCell>
							</TableRow>
						{/each}
					</tbody>
				</Table>
			</div>
			{#if isAdmin && configAgent}
				<Card class="vps-config-panel">
					<div class="flex flex-wrap items-start justify-between gap-3">
						<div><p class="page-subtitle font-display uppercase tracking-[0.12em]">Configuración del contenedor</p><h3 class="mt-1 font-medium">{configAgent.name}</h3></div>
						<Button variant="ghost" size="sm" onclick={() => (configAgentID = null)}>Cerrar configuración</Button>
					</div>
					<ContainerTagsEditor agent={configAgent} {suggestions} />
				</Card>
			{/if}
		{:else}
			<Card class="vps-empty">
				{#if waiting}<p class="font-display text-sm uppercase tracking-[0.12em]">Trazando los contenedores…</p>
				{:else if groupAgents.length === 0}<p class="font-display text-sm uppercase tracking-[0.12em]">No se encontraron contenedores</p><p class="mt-1 text-sm text-muted-foreground">El host puede haber cambiado su hostname o todavía no reportó.</p>
				{:else}<p class="font-display text-sm uppercase tracking-[0.12em]">Ningún contenedor coincide</p><p class="mt-1 text-sm text-muted-foreground">Quitá algún filtro de etiqueta para volver a ver el inventario.</p>{/if}
			</Card>
		{/if}
	</section>
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
