<script lang="ts">
	import { onMount } from 'svelte';
	import { agentHistory } from '#lib/api/client.js';
	import type { Agent } from '#lib/api/types.js';
	import AgentMetrics from '#lib/components/AgentMetrics.svelte';
	import Sparkline from '#lib/components/Sparkline.svelte';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Card } from '#lib/components/ui/card/index.js';
	import { fmtAgo } from '#lib/format.js';

	/*
	 * Card de un agent (REQ-022): nombre/hostname, badge ONLINE (dorado
	 * glow-gold) / OFFLINE (destructive), métricas actuales + sparkline inline
	 * SVG de la CPU (6 h, step 300, sin librerías). Estado live vía store SSE.
	 * Pulido CON-007: header flex (nombre + badge derecha), tiles de métricas
	 * con barras de uso, RX/TX explícitos y tags con perfil navegable.
	 * OFFLINE: las métricas persisten (último beat) → se atenúan (.55) con
	 * mini-label "últimos datos · visto hace X" encima; el footer solo las
	 * lleva cuando está ONLINE.
	 */
	let { agent }: { agent: Agent } = $props();

	const nowSecs = $state(Math.floor(Date.now() / 1000));

	let cpuVals = $state<(number | null)[]>([]);
	let sparklineReady = $state(false);

	onMount(async () => {
		const res = await agentHistory(agent.id, nowSecs - 6 * 3600, nowSecs, 300);
		if (res.ok && res.data) {
			cpuVals = res.data.points.map((p) => p.cpu_pct);
		}
		sparklineReady = true;
	});

	const offline = $derived(agent.status !== 'online');
	const cardClass = $derived(
		offline
			// glow destructive sutil y PERSISTENTE en cards OFFLINE (exigido por la calidad visual)
			? 'agent-card--offline border-destructive/50 shadow-[0_0_14px_hsl(8_80%_55%/0.15)]'
			: 'agent-card--online hover:glow-gold'
	);
</script>

<Card class="agent-card {cardClass}">
	<!-- Header row: nombre (link) + badge a la derecha -->
	<div class="flex items-center justify-between gap-3 px-4 pt-4">
		<div class="min-w-0">
			<h3 class="truncate text-[15px] font-semibold">
				<a href={`/agents/${agent.id}`} class="transition-colors hover:text-primary">
					{agent.name}
				</a>
			</h3>
			<p class="truncate text-[13px] text-muted-foreground">
				{agent.hostname}
				{#if agent.container_id}
					· {agent.container_id.slice(0, 12)}
				{/if}
			</p>
			{#if (agent.tags?.length ?? 0) + (agent.container_tags?.length ?? 0) > 0}
				<!-- chips de tags (badge secondary cian, 10px) bajo el hostname -->
				<div class="mt-1 flex flex-wrap gap-1">
					{#each agent.tags ?? [] as tg (`system-${tg}`)}
						<a
							href={`/tags/${encodeURIComponent(tg)}`}
							class="rounded-full border border-secondary/40 bg-secondary/15 px-2 py-px text-[10px] text-secondary transition-colors hover:border-primary/50 hover:bg-primary/10 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
							title={`Ver todas las instancias con tag ${tg}`}
						>
							{tg}
						</a>
					{/each}
					{#each agent.container_tags ?? [] as tg (`container-${tg}`)}
						<a
							href={`/tags/${encodeURIComponent(tg)}`}
							class="rounded-full border border-primary/35 bg-primary/8 px-2 py-px text-[10px] text-primary/85 transition-colors hover:border-primary/55 hover:bg-primary/14 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
							title={`Etiqueta manual de contenedor · ver todas las instancias con ${tg}`}
						>
							{tg}
						</a>
					{/each}
				</div>
			{/if}
		</div>
		{#if offline}
			<Badge variant="destructive">OFFLINE</Badge>
		{:else}
			<Badge variant="default">ONLINE</Badge>
		{/if}
	</div>

	<!-- Métricas 2×2 + sparkline + estado -->
	<div class="grid gap-3 px-4 pb-4">
		{#if offline}
			<!-- métricas atenuadas: son el ÚLTIMO beat persistido, no en vivo -->
			<div class="flex items-center justify-between text-[10px] uppercase tracking-wider text-muted-foreground">
				<span>últimos datos</span>
				<span class="tabular-nums">visto {fmtAgo(agent.last_seen, nowSecs)}</span>
			</div>
		{/if}
		<AgentMetrics agent={agent} compact muted={offline} />

		<div class="grid gap-1 text-secondary" class:opacity-55={offline} aria-hidden={offline || undefined}>
			<div class="flex items-center justify-between text-xs text-muted-foreground">
				<span>CPU · últimas 6 h · lectura cada 5 min</span>
				{#if !sparklineReady}
					{#if offline}
						<span>cargando…</span>
					{:else}
						<span aria-live="polite">cargando…</span>
					{/if}
				{:else if cpuVals.length === 0}
					<span>sin historia aún</span>
				{/if}
			</div>
			{#if sparklineReady}
				<Sparkline values={cpuVals} />
			{/if}
		</div>

		<div class="flex items-center justify-between gap-2 text-xs text-muted-foreground">
			<span class="truncate">
				{#if offline}
					intervalo {agent.interval}s
				{:else}
					visto {fmtAgo(agent.last_seen, nowSecs)} · intervalo {agent.interval}s
				{/if}
			</span>
			{#if agent.alert_count > 0}
				<Badge variant="destructive">{agent.alert_count} alerta(s)</Badge>
			{/if}
		</div>
	</div>
</Card>
