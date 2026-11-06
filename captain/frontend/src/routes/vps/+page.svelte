<script lang="ts">
	import Server from '@lucide/svelte/icons/server';
	import ArrowUpRight from '@lucide/svelte/icons/arrow-up-right';
	import { auth } from '#lib/stores/auth.svelte.js';
	import { sse, refreshAgents } from '#lib/stores/sse.svelte.js';
	import { groupAgentsByVPS } from '#lib/vps.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Card } from '#lib/components/ui/card/index.js';

	const allAgents = $derived(sse.list());
	const agents = $derived(allAgents.filter((agent) => !agent.monitoring_ignored));
	const groups = $derived(groupAgentsByVPS(allAgents));
	const onlineCount = $derived(agents.filter((agent) => agent.status === 'online').length);
	const offlineCount = $derived(agents.length - onlineCount);
	const alertCount = $derived(agents.reduce((total, agent) => total + (agent.alert_count ?? 0), 0));
	const waiting = $derived(allAgents.length === 0 && (sse.loading || sse.status !== 'live'));
	const isAdmin = $derived(auth.user?.role === 'admin');
</script>

<svelte:head><title>VPS · RLS Legacy</title></svelte:head>

<div class="grid gap-6">
	<header class="route-heading">
		<div class="min-w-0">
			<p class="route-kicker">INFRAESTRUCTURA · VPS EXTERNAS</p>
			<h1 class="route-title">Servidores VPS</h1>
			<p class="route-copy">Cada token identifica una VPS externa. Stowaway detecta sus contenedores; selecciona un servidor para revisar métricas, alertas y etiquetas.</p>
		</div>
		<div class="flex flex-wrap items-center gap-2">
			<Badge variant={sse.status === 'live' ? 'secondary' : 'outline'}>{sse.status === 'live' ? 'DATOS EN VIVO' : sse.status.toUpperCase()}</Badge>
			<Button variant="action" size="sm" onclick={() => void refreshAgents()} disabled={sse.loading}>Actualizar</Button>
		</div>
	</header>

	<section class="vps-overview" aria-label="Resumen de VPS y contenedores">
		<Card class="vps-overview-stat"><span>VPS identificados</span><strong>{groups.length.toString().padStart(2, '0')}</strong></Card>
		<Card class="vps-overview-stat"><span>Contenedores</span><strong>{agents.length.toString().padStart(2, '0')}</strong></Card>
		<Card class="vps-overview-stat vps-overview-stat--online"><span>En línea</span><strong>{onlineCount.toString().padStart(2, '0')}</strong></Card>
		<Card class="vps-overview-stat vps-overview-stat--offline"><span>Sin señal</span><strong>{offlineCount.toString().padStart(2, '0')}</strong></Card>
		<Card class="vps-overview-stat vps-overview-stat--alerts"><span>Alertas abiertas</span><strong>{alertCount.toString().padStart(2, '0')}</strong></Card>
	</section>

	<section class="grid gap-3" aria-labelledby="vps-list-title">
		<div class="flex flex-wrap items-end justify-between gap-2">
			<div><p class="page-subtitle font-display uppercase tracking-[0.14em]">Servidores registrados</p><h2 id="vps-list-title" class="mt-1 font-display text-base font-semibold">Seleccioná una VPS para ver sus contenedores</h2></div>
			<span class="text-xs text-muted-foreground">Agrupadas por Docker Engine · fallback a hostname en agentes antiguos</span>
		</div>
		{#if groups.length > 0}
			<div class="vps-grid">
				{#each groups as group (group.key)}
					<a class="vps-link-card" href={`/vps/${encodeURIComponent(group.key)}`} aria-label={`Abrir VPS ${group.name}`}>
						<Card class="vps-card">
							<div class="flex items-start justify-between gap-3">
								<span class="vps-icon"><Server size={20} strokeWidth={1.6} aria-hidden="true" /></span>
								<ArrowUpRight size={16} class="text-muted-foreground" aria-hidden="true" />
							</div>
							<p class="vps-card-kicker">VPS · {group.agents.length - group.ignoredCount} en monitoreo · {group.ignoredCount} ignorado(s)</p>
							<h3 class="vps-card-title" title={group.name}>{group.name}</h3>
							<div class="vps-card-stats">
								<span><i class="status-dot status-dot--online"></i>{group.onlineCount} en línea</span>
								<span><i class="status-dot status-dot--offline"></i>{group.offlineCount} sin señal</span>
								<span class:has-alerts={group.alertCount > 0}>{group.alertCount} alertas</span>
							</div>
							<div class="vps-card-footer"><span>{isAdmin ? 'Ver y clasificar contenedores' : 'Ver contenedores'}</span><span class="vps-open-arrow">Explorar</span></div>
						</Card>
					</a>
				{/each}
			</div>
		{:else}
			<Card class="vps-empty">
				{#if waiting}<p class="font-display text-sm uppercase tracking-[0.12em]">Trazando los servidores…</p><p class="mt-1 text-sm text-muted-foreground">Esperando reportes de los agentes.</p>
				{:else}<p class="font-display text-sm uppercase tracking-[0.12em]">Todavía no hay VPS registradas</p><p class="mt-1 text-sm text-muted-foreground">Cuando un contenedor reporte su identidad de servidor aparecerá aquí agrupado automáticamente.</p>{/if}
			</Card>
		{/if}
	</section>
</div>
