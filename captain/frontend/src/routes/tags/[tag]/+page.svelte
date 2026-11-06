<script lang="ts">
	import { page } from '$app/state';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { goto } from '$app/navigation';
	import AgentCard from '#lib/components/AgentCard.svelte';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Card } from '#lib/components/ui/card/index.js';
	import { sse, refreshAgents } from '#lib/stores/sse.svelte.js';
	import { filterAgentsByTag } from '#lib/tags.js';

	const tag = $derived(page.params.tag ?? '');
	const agents = $derived(sse.list());
	const taggedAgents = $derived.by(() => filterAgentsByTag(agents, tag));
	const onlineCount = $derived(taggedAgents.filter((agent) => agent.status === 'online').length);
	const offlineCount = $derived(taggedAgents.length - onlineCount);
	const alertCount = $derived(taggedAgents.reduce((sum, agent) => sum + (agent.alert_count ?? 0), 0));
	const waitingForFleet = $derived(agents.length === 0 && (sse.loading || sse.status !== 'live'));
</script>

<svelte:head>
	<title>Etiqueta {tag} · Contenedores — RLS Legacy</title>
</svelte:head>

<div class="grid gap-6">
	<header class="route-heading">
		<div class="min-w-0">
			<p class="route-kicker">CONTENEDORES · AGRUPADOS POR ETIQUETA</p>
			<h1 class="route-title">{tag || 'Tag sin nombre'}</h1>
			<p class="route-copy">
				Vista de todos los contenedores que reportan esta etiqueta, con estado, métricas y alertas en vivo.
			</p>
		</div>
		<div class="flex flex-wrap items-center gap-2">
			<Badge variant={sse.status === 'live' ? 'secondary' : 'outline'}>{sse.status === 'live' ? 'DATOS EN VIVO' : sse.status.toUpperCase()}</Badge>
			<Button variant="action" size="sm" onclick={() => void refreshAgents()} disabled={sse.loading}>
				<RefreshCw size={14} class={sse.loading ? 'animate-spin' : ''} aria-hidden="true" />
				Actualizar
			</Button>
			<Button variant="action" size="sm" onclick={() => goto('/')}>
				<ArrowLeft size={14} aria-hidden="true" />
				Vista general
			</Button>
		</div>
	</header>

	<section class="tag-profile-stats" aria-label={`Resumen de instancias con tag ${tag}`}>
		<Card class="tag-profile-stat">
			<span>Contenedores</span>
			<strong>{taggedAgents.length.toString().padStart(2, '0')}</strong>
		</Card>
		<Card class="tag-profile-stat tag-profile-stat--online">
			<span>En línea</span>
			<strong>{onlineCount.toString().padStart(2, '0')}</strong>
		</Card>
		<Card class="tag-profile-stat tag-profile-stat--offline">
			<span>Sin señal</span>
			<strong>{offlineCount.toString().padStart(2, '0')}</strong>
		</Card>
		<Card class="tag-profile-stat tag-profile-stat--alerts">
			<span>Alertas abiertas</span>
			<strong>{alertCount.toString().padStart(2, '0')}</strong>
		</Card>
	</section>

	<section class="grid gap-3" aria-labelledby="tag-instances-title">
		<div class="flex flex-wrap items-end justify-between gap-3">
			<div>
				<p class="page-subtitle font-display uppercase tracking-[0.14em]">Instrumentos del sector</p>
				<h2 id="tag-instances-title" class="mt-1 font-display text-base font-semibold">
					Instancias con tag <span class="text-secondary">{tag}</span>
				</h2>
			</div>
			<span class="text-xs text-muted-foreground">{taggedAgents.length} contenedor(es)</span>
		</div>

		{#if taggedAgents.length > 0}
			<div class="tag-agent-grid">
				{#each taggedAgents as agent (agent.id)}
					<AgentCard {agent} />
				{/each}
			</div>
		{:else}
			<Card class="tag-profile-empty">
				{#if waitingForFleet}
					<p class="font-display text-sm uppercase tracking-[0.12em]">Cargando contenedores…</p>
					<p class="mt-1 text-sm text-muted-foreground">Esperando el primer reporte del dashboard.</p>
				{:else}
					<p class="font-display text-sm uppercase tracking-[0.12em]">Sin instancias en este sector</p>
					<p class="mt-1 text-sm text-muted-foreground">
						Ningún contenedor tiene ahora mismo la etiqueta <strong class="text-secondary">{tag}</strong>.
					</p>
				{/if}
			</Card>
		{/if}
	</section>
</div>
