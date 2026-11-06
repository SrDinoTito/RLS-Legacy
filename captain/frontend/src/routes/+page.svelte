<script lang="ts">
	import Anchor from '@lucide/svelte/icons/anchor';
	import ArrowDown from '@lucide/svelte/icons/arrow-down';
	import ArrowUp from '@lucide/svelte/icons/arrow-up';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { auth } from '#lib/stores/auth.svelte.js';
	import { sse, refreshAgents } from '#lib/stores/sse.svelte.js';
	import AgentCard from '#lib/components/AgentCard.svelte';
	import AgentDetailModal from '#lib/components/AgentDetailModal.svelte';
	import TokenCreateModal from '#lib/components/TokenCreateModal.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Tabs } from '#lib/components/ui/tabs/index.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import { Card } from '#lib/components/ui/card/index.js';
	import {
		Table,
		TableCell,
		TableHead,
		TableRow
	} from '#lib/components/ui/table/index.js';
	import {
		fmtBytesPairMb,
		fmtBytesMb,
		fmtBytesUnitMb,
		fmtDisk,
		fmtPct,
		fmtUptime,
		fmtAgo
	} from '#lib/format.js';
	import type { Agent } from '#lib/api/types.js';
	import { allAgentTags } from '#lib/tags.js';

	/*
	 * Dashboard (REQ-022, refactor a vista de LISTA / top bar global):
	 * el header viejo desaparece — la barra global (AppShell) lleva marca +
	 * nav + accesos admin (iconos Lucide + modales, que ahora viven en
	 * AppShell) + grados de sesión. Aquí queda solo la BANDA "toolbar" de la
	 * vista: título muted + badge SSE + toggle lista/tarjetas + filtro
	 * Todos/Offline + refrescar, y debajo los chips de tags. View switch
	 * persistido en 'rls.view' (default "lista"), filtro de tags persistido
	 * en 'rls.tag.filter'. Click en fila → modal de detalle.
	 */

	type ViewMode = 'lista' | 'cards';

	/** Filtro de estado (REQ-022): "Todos / Solo offline". */
	let filter = $state<'todo' | 'offline'>('todo');

	/** View switch (refactor): 'rls.view' con default "lista". */
	let view = $state<ViewMode>(readView());
	let detailAgentId = $state<number | null>(null);
	let detailOpen = $state(false);
	let tokenModalOpen = $state(false);

	function readView(): ViewMode {
		// ssr=false pero el prerender del shell puede correr fuera de window → guard
		if (typeof window === 'undefined') return 'lista';
		return localStorage.getItem('rls.view') === 'cards' ? 'cards' : 'lista';
	}

	$effect(() => {
		if (typeof window === 'undefined') return;
		localStorage.setItem('rls.view', view);
	});

	/** Filtro por tags (multi-select OR). Persistido en 'rls.tag.filter'. */
	let tagFilter = $state<string[]>(readTagFilter());

	function readTagFilter(): string[] {
		// ssr=false pero el prerender del shell puede correr fuera de window → guard
		if (typeof window === 'undefined') return [];
		try {
			const raw = localStorage.getItem('rls.tag.filter');
			const parsed: unknown = raw ? JSON.parse(raw) : [];
			return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string') : [];
		} catch {
			return [];
		}
	}

	$effect(() => {
		if (typeof window === 'undefined') return;
		localStorage.setItem('rls.tag.filter', JSON.stringify(tagFilter));
	});

	function toggleTag(t: string): void {
		tagFilter = tagFilter.includes(t) ? tagFilter.filter((x) => x !== t) : [...tagFilter, t];
	}

	// reactividad del SvelteMap: se reevalúa con cada evento SSE
	const allAgents = $derived(sse.list());
	const agents = $derived(allAgents.filter((agent) => !agent.monitoring_ignored));
	const ignoredAgents = $derived(allAgents.filter((agent) => agent.monitoring_ignored));
	/** Tags distintas presentes en la flota (ordenadas) con su contador. */
	const tagOptions = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const a of agents) {
			for (const t of allAgentTags(a)) counts.set(t, (counts.get(t) ?? 0) + 1);
		}
		return [...counts.entries()].sort((x, y) => x[0].localeCompare(y[0]));
	});
	/** OR-match: un agent pasa si ALGUNO de sus tags está en la selección. */
	const filtered = $derived.by(() => {
		let list = agents;
		if (tagFilter.length > 0) list = list.filter((a) => allAgentTags(a).some((t) => tagFilter.includes(t)));
		if (filter === 'offline') list = list.filter((a) => a.status !== 'online');
		return list;
	});
	const offlineCount = $derived(agents.filter((a) => a.status !== 'online').length);
	const onlineCount = $derived(agents.length - offlineCount);

	const isAdmin = $derived(auth.user?.role === 'admin');

	/** El agent del modal SIEMPRE vivo: resuelto de la lista SSE en cada tick. */
	const detailAgent = $derived(
		detailAgentId == null ? null : (agents.find((a) => a.id === detailAgentId) ?? null)
	);

	function openDetails(a: Agent): void {
		detailAgentId = a.id;
		detailOpen = true;
	}

	function closeDetails(): void {
		detailOpen = false;
	}

	let nowSecs = $state(Math.floor(Date.now() / 1000));

	$effect(() => {
		const t = setInterval(() => (nowSecs = Math.floor(Date.now() / 1000)), 30_000);
		return () => clearInterval(t);
	});

	function onRefresh(): void {
		void refreshAgents();
	}

	function onTokenCreated(): void {
		void refreshAgents(); // el token fuerza auto-registro al primer beat; refresca igual
	}

	/* ------------------- onboarding: una VPS, un Stowaway ------------------- */
</script>

<svelte:head>
	<title>Dashboard — RLS Legacy</title>
</svelte:head>

<div class="grid gap-5">
	<section class="dashboard-hero" aria-labelledby="fleet-hero-title">
		<div>
			<p class="hero-kicker">ZONA DE CONTROL · INFRAESTRUCTURA</p>
			<h1 id="fleet-hero-title" class="hero-title">Estado general</h1>
			<p class="hero-copy">
				Consulta el estado, las métricas y las alertas de los contenedores conectados desde un solo panel.
			</p>
			<div class="hero-stats" aria-label="Resumen de contenedores">
				<div class="hero-stat">
					<span class="hero-stat-label">En línea</span>
					<strong class="hero-stat-value is-live">{onlineCount.toString().padStart(2, '0')}</strong>
				</div>
				<div class="hero-stat">
					<span class="hero-stat-label">Sin señal</span>
					<strong class="hero-stat-value is-offline">{offlineCount.toString().padStart(2, '0')}</strong>
				</div>
				<div class="hero-stat">
					<span class="hero-stat-label">Alertas abiertas</span>
					<strong class="hero-stat-value is-alert">{sse.activeAlerts.toString().padStart(2, '0')}</strong>
				</div>
			</div>
		</div>
		<div class="hero-navigation" aria-hidden="true">
			<div class="hero-compass">
				<svg viewBox="0 0 100 100" fill="none" xmlns="http://www.w3.org/2000/svg">
					<circle cx="50" cy="50" r="42" stroke="currentColor" stroke-opacity=".26" />
					<circle cx="50" cy="50" r="31" stroke="currentColor" stroke-opacity=".42" stroke-dasharray="1 5" />
					<path d="M50 12v10M50 78v10M12 50h10M78 50h10" stroke="currentColor" stroke-width="1.5" />
					<path d="m50 22 7.5 25.5L78 50l-20.5 5.5L50 78l-7.5-22.5L22 50l20.5-2.5L50 22Z" stroke="currentColor" stroke-linejoin="round" />
					<path d="m50 30 4.6 15.4L70 50l-15.4 4.6L50 70l-4.6-15.4L30 50l15.4-4.6L50 30Z" fill="currentColor" fill-opacity=".15" stroke="currentColor" stroke-opacity=".72" />
					<circle cx="50" cy="50" r="2.6" fill="currentColor" />
				</svg>
			</div>
			<p class="hero-coordinate">MONITOREO EN TIEMPO REAL</p>
		</div>
	</section>

	<!-- Banda "toolbar" de la vista (el header global vive en AppShell):
	     título muted + badge SSE + toggles/filtros + refrescar -->
	<div class="fleet-toolbar">
		<div class="flex flex-wrap items-center gap-3">
			<p class="page-subtitle font-display uppercase tracking-[0.14em]">Instrumentos de vuelo</p>
			{#if sse.status === 'live'}
				<Badge variant="secondary">SSE EN VIVO</Badge>
			{:else}
				<Badge variant="outline">{sse.status.toUpperCase()}</Badge>
			{/if}
		</div>
		<div class="flex flex-wrap items-center gap-3 tabular-nums">
			<!-- Toggle de vista (lista/cards, persistido en 'rls.view') -->
			<div
				class="inline-flex h-10 items-center gap-1 rounded-lg border border-border bg-muted/50 p-1"
				role="group"
				aria-label="Modo de vista"
			>
				<button
					type="button"
					aria-pressed={view === 'lista'}
					onclick={() => (view = 'lista')}
					class="h-8 rounded-md px-3 text-sm font-medium transition-colors
						{view === 'lista'
						? 'bg-primary text-primary-foreground'
						: 'text-muted-foreground hover:text-foreground'}"
				>
					Lista
				</button>
				<button
					type="button"
					aria-pressed={view === 'cards'}
					onclick={() => (view = 'cards')}
					class="h-8 rounded-md px-3 text-sm font-medium transition-colors
						{view === 'cards'
						? 'bg-primary text-primary-foreground'
						: 'text-muted-foreground hover:text-foreground'}"
				>
					Tarjetas
				</button>
			</div>
			<!-- tabular-nums (anti-CLS): los contadores (N) de los tabs cambian con
			     cada beat SSE — dígitos tabulares para que el ancho no baile -->
			<Tabs
				class="tabular-nums"
				options={[
					{ value: 'todo', label: `Todos (${agents.length})` },
					{ value: 'offline', label: `Solo offline (${offlineCount})` }
				]}
				bind:value={filter}
			/>
			<Button variant="action" size="sm" onclick={onRefresh} disabled={sse.loading}>
				<RefreshCw size={14} strokeWidth={1.8} class={sse.loading ? 'animate-spin' : ''} aria-hidden="true" />
				{sse.loading ? 'Actualizando…' : 'Refrescar'}
			</Button>
		</div>
	</div>

	<!-- Chips de tags (ecosistemas): OR multi-select, "Todos" resetea, contador por chip -->
	{#if tagOptions.length > 0}
		<div class="flex flex-wrap items-center gap-2">
			<button
				type="button"
				class="rounded-full border px-3 py-1 text-[13px] transition-colors
					{tagFilter.length === 0
					? 'border-primary/40 bg-primary/15 text-primary'
					: 'border-transparent bg-transparent text-muted-foreground hover:border-primary/45 hover:bg-primary/10 hover:text-foreground'}"
				onclick={() => (tagFilter = [])}
			>
				Todos
			</button>
			{#each tagOptions as [t, n] (t)}
				<button
					type="button"
					class="rounded-full border px-3 py-1 text-[13px] transition-colors
						{tagFilter.includes(t)
						? 'border-secondary/50 bg-secondary/15 text-secondary'
						: 'border-transparent bg-transparent text-muted-foreground hover:border-secondary/45 hover:bg-secondary/10 hover:text-secondary'}"
					onclick={() => toggleTag(t)}
				>
					{t} ({n})
				</button>
			{/each}
		</div>
	{/if}

	{#if agents.length === 0 && ignoredAgents.length === 0}
		<!-- Onboarding: sin agentes aún → guía compacta centrada (AC-002 arranque) -->
		<Card class="mx-auto mt-[10vh] w-full max-w-[520px]">
			<div class="flex flex-col items-center gap-4 px-8 py-8 text-center">
				<!-- Icono circular con glow dorado sutil -->
				<div
					class="grid h-14 w-14 shrink-0 place-items-center rounded-full border border-primary/40 bg-primary/10 text-primary shadow-[0_0_24px_hsl(42_85%_52%/0.18)]"
					aria-hidden="true"
				>
					<Anchor size={28} strokeWidth={1.6} />
				</div>
				<div class="grid gap-1.5">
					<h2 class="font-display text-base font-semibold uppercase tracking-[0.18em]">
						Ningún agent reporta todavía
					</h2>
					<p class="text-[13px] text-muted-foreground">
						Aún no hay contenedores reportando. Conecta una VPS externa para empezar:
					</p>
				</div>
				<!-- Mini-guía numerada -->
				<ol class="grid w-full gap-3 text-left">
					<li class="flex items-start gap-3">
						<span
							class="mt-0.5 grid h-5 w-5 shrink-0 place-items-center rounded-full border border-border bg-muted/60 font-display text-[11px] font-semibold text-primary"
							aria-hidden="true">1</span
						>
						<p class="text-[13px] text-muted-foreground">
							Creá un token —{#if isAdmin}
								usá el botón
								<button
									type="button"
									class="text-primary underline-offset-2 hover:underline"
									onclick={() => (tokenModalOpen = true)}>Nuevo token</button
								>.
							{:else}
								Solicita un token a un administrador (Administración → Tokens).
							{/if}
						</p>
					</li>
					<li class="flex items-start gap-3">
						<span
							class="mt-0.5 grid h-5 w-5 shrink-0 place-items-center rounded-full border border-border bg-muted/60 font-display text-[11px] font-semibold text-primary"
							aria-hidden="true">2</span
						>
						<p class="text-[13px] text-muted-foreground">
							Instala Stowaway en el host de la VPS externa. Detecta y reporta sus contenedores en ejecución.
						</p>
					</li>
					<li class="flex flex-col items-stretch gap-2">
						<div class="flex items-start gap-3">
							<span
								class="mt-0.5 grid h-5 w-5 shrink-0 place-items-center rounded-full border border-border bg-muted/60 font-display text-[11px] font-semibold text-primary"
								aria-hidden="true">3</span
							>
							<p class="text-[13px] text-muted-foreground">
								El instalador configura systemd y se comunica con el Docker Engine local; no hace falta tocar los Compose.
							</p>
						</div>
					</li>
				</ol>
				{#if isAdmin}
					<Button size="sm" onclick={() => (tokenModalOpen = true)}>
						<Anchor size={14} strokeWidth={1.8} class="text-secondary" aria-hidden="true" />
						Crear token
					</Button>
				{:else}
					<p class="text-[13px] text-muted-foreground">
						Solo un administrador puede crear tokens.
					</p>
				{/if}
			</div>
		</Card>
	{:else if agents.length === 0}
		<Card class="mx-auto mt-8 w-full max-w-[520px] p-6 text-center">
			<h2 class="font-display text-base uppercase tracking-[0.14em]">No hay contenedores incluidos</h2>
			<p class="mt-2 text-sm text-muted-foreground">{ignoredAgents.length} contenedor(es) están excluidos del monitoreo. Podés volver a incluirlos desde la página de su VPS.</p>
			<a class="mt-4 inline-flex justify-center text-sm text-primary underline-offset-4 hover:underline" href="/vps">Administrar VPS y contenedores</a>
		</Card>
	{:else if view === 'lista'}
		<!-- LISTA (refactor): tabla por agent; click en fila → modal de detalle -->
		<Table class="fleet-table min-w-[900px]">
			<thead>
				<TableRow class="bg-muted/40">
					<TableHead>Estado</TableHead>
					<TableHead>Agent</TableHead>
					<TableHead>Tags</TableHead>
					<TableHead class="text-right">CPU</TableHead>
					<TableHead class="text-right">RAM · usada / límite</TableHead>
					<TableHead class="text-right">RED · Rx / Tx</TableHead>
					<TableHead class="text-right">Disco · libre / total</TableHead>
					<TableHead class="text-right">Uptime</TableHead>
					<TableHead class="text-right">Última señal</TableHead>
					<TableHead>Alertas abiertas</TableHead>
					<TableHead class="text-right">Historial</TableHead>
				</TableRow>
			</thead>
			<tbody>
				{#each filtered as agent (agent.id)}
					{@const offline = agent.status !== 'online'}
					{@const ramPct =
						agent.metrics?.ram_used_mb != null && agent.metrics.ram_limit_mb != null && agent.metrics.ram_limit_mb > 0
							? (agent.metrics.ram_used_mb / agent.metrics.ram_limit_mb) * 100
							: null}
					{@const diskFreePct =
						agent.disk_free_mb != null && agent.disk_total_mb != null && agent.disk_total_mb > 0
							? (agent.disk_free_mb / agent.disk_total_mb) * 100
							: null}
					<TableRow
						role="button"
						tabindex={0}
						class="cursor-pointer {offline ? 'text-muted-foreground' : ''}"
						onclick={() => openDetails(agent)}
						onkeydown={(e) => {
							if (e.key === 'Enter' || e.key === ' ') {
								e.preventDefault();
								openDetails(agent);
							}
						}}
					>
						<TableCell>
							<span class="flex items-center gap-2 whitespace-nowrap">
								<span
									class="inline-block h-2 w-2 shrink-0 rounded-full {offline
										? 'bg-destructive shadow-[0_0_6px_hsl(8_80%_55%/0.5)]'
										: 'bg-primary shadow-[0_0_6px_hsl(42_85%_52%/0.4)]'}"
									aria-hidden="true"
								></span>
								<span class="text-[11px] tracking-wider {offline ? '' : 'text-foreground'}">
									{offline ? 'OFFLINE' : 'ONLINE'}
								</span>
							</span>
						</TableCell>
						<TableCell class="max-w-[220px]">
							<p class="truncate font-medium">{agent.name}</p>
							<p class="truncate text-[12px] text-muted-foreground">{agent.hostname}</p>
						</TableCell>
						<TableCell>
							{#if agent.tags && agent.tags.length > 0}
								<div class="flex flex-wrap gap-1">
									{#each agent.tags as tg (tg)}
										<a
											href={`/tags/${encodeURIComponent(tg)}`}
											class="rounded-full border border-secondary/40 bg-secondary/15 px-2 py-px text-[10px] text-secondary transition-colors hover:border-primary/50 hover:bg-primary/10 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
											title={`Ver todas las instancias con tag ${tg}`}
											onclick={(e) => e.stopPropagation()}
										>
											{tg}
										</a>
									{/each}
								</div>
							{:else}
								<span class="text-muted-foreground">—</span>
							{/if}
						</TableCell>
						<TableCell class="whitespace-nowrap text-right tabular-nums">
							{fmtPct(agent.metrics?.cpu_pct)}
						</TableCell>
						<TableCell class="whitespace-nowrap text-right tabular-nums">
							{#if agent.metrics?.ram_limit_mb}
								{fmtBytesPairMb(agent.metrics.ram_used_mb, agent.metrics.ram_limit_mb)}
							{:else}
								{fmtBytesMb(agent.metrics?.ram_used_mb)}
							{/if}
							{#if ramPct != null}
								<span class="block text-[10px] text-muted-foreground">{ramPct.toFixed(1)} % usado</span>
							{/if}
						</TableCell>
						<TableCell class="whitespace-nowrap text-right tabular-nums" title="Recibido (Rx) / enviado (Tx)">
							<span class="mr-1 inline-flex items-center gap-0.5 text-muted-foreground" aria-hidden="true">
								<ArrowDown size={12} strokeWidth={1.8} />
								<ArrowUp size={12} strokeWidth={1.8} />
							</span>
							{fmtBytesPairMb(agent.metrics?.rx_mb, agent.metrics?.tx_mb)}
						</TableCell>
						<TableCell class="whitespace-nowrap text-right tabular-nums">
							{#if agent.disk_free_mb != null}
								{fmtBytesUnitMb(agent.disk_free_mb)}
								{#if agent.disk_total_mb != null && agent.disk_total_mb > 0}
									<span class="text-muted-foreground">/ {fmtBytesUnitMb(agent.disk_total_mb)}</span>
								{/if}
							{:else}
								<span class="text-muted-foreground">{fmtDisk(agent.disk_free_mb, agent.disk_total_mb)}</span>
							{/if}
							{#if diskFreePct != null}
								<span class="block text-[10px] text-muted-foreground">{diskFreePct.toFixed(1)} % libre</span>
							{/if}
						</TableCell>
						<TableCell class="whitespace-nowrap text-right tabular-nums">
							{fmtUptime(agent.metrics?.uptime_sec)}
						</TableCell>
						<TableCell class="whitespace-nowrap text-right text-muted-foreground">
							{fmtAgo(agent.last_seen, nowSecs)}
						</TableCell>
						<TableCell>
							{#if agent.alert_count > 0}
								<Badge variant="destructive">{agent.alert_count} alerta(s)</Badge>
							{:else}
								—
							{/if}
						</TableCell>
						<TableCell class="text-right">
							<Button
								variant="action"
								size="sm"
								onclick={(e) => {
									e.stopPropagation();
									openDetails(agent);
								}}
							>
								Historial
							</Button>
						</TableCell>
					</TableRow>
				{:else}
					<TableRow>
						<TableCell colspan={11}>
							<p class="py-8 text-center text-sm text-muted-foreground">
								{#if tagFilter.length > 0}
									Ningún agent con los tags seleccionados — probá con otros chips.
								{:else if filter === 'offline'}
									Ningún agent está offline — buen trópico.
								{/if}
							</p>
						</TableCell>
					</TableRow>
				{/each}
			</tbody>
		</Table>
	{:else}
		<!-- Grid de cards: auto-fill con mínimo 300px (desktop-first) -->
		<div class="grid grid-cols-[repeat(auto-fill,minmax(300px,1fr))] gap-4">
			{#each filtered as agent (agent.id)}
				<AgentCard {agent} />
			{:else}
				<p class="py-8 text-sm text-muted-foreground">
					{#if tagFilter.length > 0}
						Ningún agent con los tags seleccionados — probá con otros chips.
					{:else if filter === 'offline'}
						Ningún agent está offline — buen trópico.
					{/if}
				</p>
			{/each}
		</div>
	{/if}
</div>

<!-- Modal de detalle (vista rápida con historial); el agent es SIEMPRE el vivo del store -->
{#if detailAgent}
	<AgentDetailModal agent={detailAgent} bind:open={detailOpen} />
{/if}

<!-- Creador de tokens del onboarding (solo admin; la barra global trae el
     gestor completo; el guard 403 del backend igual protege) -->
{#if isAdmin}
	<TokenCreateModal bind:open={tokenModalOpen} onCreated={onTokenCreated} />
{/if}
