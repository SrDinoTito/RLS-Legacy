<script lang="ts">
	import { goto } from '$app/navigation';
	import { toast } from 'svelte-sonner';
	import {
		agentHistory,
		putThresholds,
		listAgentAlerts,
		resolveAlert,
		putAgentMonitoringIgnored
	} from '#lib/api/client.js';
	import type { Agent, HistoryPoint } from '#lib/api/types.js';
	import { normalizeAlerts, type NormalizedAlert } from '#lib/alerts.js';
	import { sse, refreshAgents } from '#lib/stores/sse.svelte.js';
	import AgentMetrics from '#lib/components/AgentMetrics.svelte';
	import UplotChart from '#lib/components/UplotChart.svelte';
	import { Dialog } from '#lib/components/ui/dialog/index.js';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Label } from '#lib/components/ui/label/index.js';
	import { Tabs } from '#lib/components/ui/tabs/index.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import PaginationControls from '#lib/components/PaginationControls.svelte';
	import { fmtAgo, fmtTs, alertTypeLabel, fmtAlertMessage, fmtAlertReading } from '#lib/format.js';
	import { auth } from '#lib/stores/auth.svelte.js';
	import NotificationPolicyControls from '#lib/components/NotificationPolicyControls.svelte';

	/*
	 * Modal de vista rápida de agent (refactor dashboard): DOS COLUMNAS —
	 * izquierda = resumen (header + tiles de métricas + thresholds editables
	 * inline reusando la lógica del page detail + labels), derecha = "Historial"
	 * (timeframe 1h/6h/24h + UplotChart CPU+RAM con límite dashed + RED + timeline
	 * de alertas con resolver y preferencias externas por contenedor). El agent llega reactivo del store SSE → las
	 * métricas/estado se refrescan en vivo con cada tick. El history refetch SOLO
	 * cambia de timeframe o al reabrir; alertas refetch en cada tick (abierto).
	 * El page /agents/[id] queda como deep-link completo (footer "Ver página").
	 */
	let { open = $bindable(false), agent }: { open?: boolean; agent: Agent } = $props();

	type TF = '1h' | '6h' | '24h';
	type DetailPanel = 'overview' | 'history';
	/** Ventanas referenciales (mismo stepper que el page detail). */
	const WINDOW: Record<TF, { span: number; step: number }> = {
		'1h': { span: 3600, step: 60 },
		'6h': { span: 21_600, step: 300 },
		'24h': { span: 86_400, step: 900 }
	};
	const TF_OPTIONS: { value: TF; label: string }[] = [
		{ value: '1h', label: '1 h' },
		{ value: '6h', label: '6 h' },
		{ value: '24h', label: '24 h' }
	];
	const PANEL_OPTIONS: { value: DetailPanel; label: string }[] = [
		{ value: 'overview', label: 'Resumen y ajustes' },
		{ value: 'history', label: 'Historial y eventos' }
	];

	const offline = $derived(agent.status !== 'online');
	const ignored = $derived(agent.monitoring_ignored);
	const isAdmin = $derived(auth.user?.role === 'admin');
	const nowSecs = $state(Math.floor(Date.now() / 1000));

	let tf = $state<TF>('1h');
	let hist = $state<HistoryPoint[]>([]);
	let alerts = $state<NormalizedAlert[]>([]);
	const alertPageSize = 8;
	let alertPage = $state(0);

	let loadedForId = $state(-1);
	let thrCpu = $state('90');
	let thrRam = $state('95');
	let savingThr = $state(false);
	let panel = $state<DetailPanel>('overview');
	let ignoreConfirmOpen = $state(false);
	let updatingMonitoring = $state(false);

	async function fetchHistory(id: number): Promise<void> {
		try {
			const now = Math.floor(Date.now() / 1000);
			const { span, step } = WINDOW[tf];
			const res = await agentHistory(id, now - span, now, step);
			hist = res.data?.points ?? [];
		} catch {
			// tolerado: la vista rápida no duplica toasts de red
		}
	}

	/** El backend devuelve el registro asociado exclusivamente a este contenedor. */
	async function fetchAlerts(id: number): Promise<void> {
		try {
			const res = await listAgentAlerts(id, 'all');
			alerts = normalizeAlerts(res.data);
		} catch {
			// tolerado
		}
	}

	// historial: SOLO al abrir o cambiar timeframe (no en cada tick)
	$effect(() => {
		if (!open) return;
		void tf;
		const id = agent.id;
		void fetchHistory(id);
	});

	// alerts + estado: cada tick SSE con el modal abierto
	$effect(() => {
		if (!open) return;
		void sse.eventTick;
		const id = agent.id;
		void fetchAlerts(id);
	});

	// thresholds: se sincronizan UNA vez por agent (los ticks no pisan la edición)
	$effect(() => {
		if (!open) return;
		if (loadedForId === agent.id) return;
		loadedForId = agent.id;
		thrCpu = String(agent.thresholds_cpu ?? 90);
		thrRam = String(agent.thresholds_ram ?? 95);
		panel = 'overview';
		alertPage = 0;
		ignoreConfirmOpen = false;
	});

	async function saveThresholds(): Promise<void> {
		const cpu = Number(thrCpu);
		const ram = Number(thrRam);
		if (!(cpu >= 10 && cpu <= 100)) {
			toast.error('thresholds_cpu debe estar entre 10 y 100');
			return;
		}
		if (!(ram >= 50 && ram <= 100)) {
			toast.error('thresholds_ram debe estar entre 50 y 100');
			return;
		}
		savingThr = true;
		try {
			const res = await putThresholds(agent.id, { thresholds_cpu: cpu, thresholds_ram: ram });
			if (res.ok) {
				toast.success('Umbrales guardados', { description: `CPU ${cpu}% · RAM ${ram}%` });
				void refreshAgents(); // el propio modal no depende: PUT devuelve ok
			} else {
				toast.error(`No se pudo guardar (${res.status})`, {
					description: res.error ?? undefined
				});
			}
		} catch {
			toast.error('Backend no disponible');
		} finally {
			savingThr = false;
		}
	}

	async function resolveAlertRow(id: number): Promise<void> {
		try {
			const res = await resolveAlert(id);
			if (res.ok) {
				toast.success('Alerta cerrada', {
					description: 'Se cierra este registro; las reglas automáticas siguen activas.'
				});
				void fetchAlerts(agent.id);
				void refreshAgents();
			} else if (res.status === 409) {
				toast.warning('La alerta ya estaba cerrada');
				void fetchAlerts(agent.id);
			} else {
				toast.error(`No se pudo cerrar (${res.status})`);
			}
		} catch {
			toast.error('Backend no disponible');
		}
	}

	async function updateMonitoring(shouldIgnore: boolean): Promise<void> {
		if (updatingMonitoring) return;
		updatingMonitoring = true;
		try {
			const result = await putAgentMonitoringIgnored(agent.id, shouldIgnore);
			if (!result.ok) {
				toast.error('No se pudo actualizar el monitoreo', { description: result.error ?? `Error HTTP ${result.status}` });
				return;
			}
			ignoreConfirmOpen = false;
			toast.success(shouldIgnore ? 'Contenedor ignorado' : 'Contenedor incluido en el monitoreo', {
				description: shouldIgnore ? 'Seguirá visible en la lista como ignorado.' : 'Volverá a generar métricas y alertas.'
			});
			await refreshAgents();
		} catch {
			toast.error('No se pudo conectar con el dashboard');
		} finally {
			updatingMonitoring = false;
		}
	}

	function openFullPage(): void {
		open = false;
		goto(`/agents/${agent.id}`);
	}

	/* ------------------------------ series para uPlot ------------------------------ */
	const gx = $derived(hist.map((p) => p.ts));
	const cpuData = $derived(hist.map((p) => p.cpu_pct));
	const ramUsed = $derived(hist.map((p) => p.ram_used_mb));
	const ramLimit = $derived(hist.map((p) => p.ram_limit_mb));
	const rxData = $derived(hist.map((p) => p.rx_mb));
	const txData = $derived(hist.map((p) => p.tx_mb));
	const openAlerts = $derived(alerts.filter((a) => !a.resolved_at));
	const pageAlerts = $derived(alerts.slice(alertPage * alertPageSize, (alertPage + 1) * alertPageSize));
</script>

<Dialog
	bind:open
	title={offline ? `${agent.name} — OFFLINE` : agent.name}
	contentClass="w-[min(96vw,1120px)] h-[84vh] max-h-[900px]"
>
	{#if open}
		<div class="agent-detail-layout">
			<div class="agent-detail-top">
				<div class="agent-detail-identity">
					<div class="min-w-0">
						<p class="agent-detail-host">{agent.hostname}{#if agent.container_id} · <span title={agent.container_id}>{agent.container_id.slice(0, 12)}</span>{/if}</p>
						<div class="mt-2 flex flex-wrap items-center gap-2">
							{#if ignored}<Badge variant="outline">IGNORADO</Badge>{:else if offline}<Badge variant="destructive">OFFLINE</Badge>{:else}<Badge variant="default">ONLINE</Badge>{/if}
							<span class="text-xs text-muted-foreground">{ignored ? 'Monitoreo pausado' : `Visto ${fmtAgo(agent.last_seen, nowSecs)} · intervalo ${agent.interval}s`}</span>
						</div>
					</div>
					<Tabs class="agent-detail-tabs" options={PANEL_OPTIONS} bind:value={panel} />
				</div>
				{#if agent.tags?.length || agent.container_tags?.length}
					<div class="flex flex-wrap gap-1.5">
						{#each agent.tags ?? [] as tg (`system-${tg}`)}<a href={`/tags/${encodeURIComponent(tg)}`} class="agent-detail-tag" title={`Ver contenedores con la etiqueta ${tg}`}>{tg}</a>{/each}
						{#each agent.container_tags ?? [] as tg (`manual-${tg}`)}<a href={`/tags/${encodeURIComponent(tg)}`} class="agent-detail-tag agent-detail-tag--manual" title={`Ver contenedores con la etiqueta ${tg}`}>{tg}</a>{/each}
					</div>
				{/if}
			</div>

			<div class="agent-detail-content">
				{#if panel === 'overview'}
					<div class="agent-overview-grid">
						<section class="agent-panel-card agent-metrics-panel">
							<header class="agent-panel-heading"><div><p class="agent-panel-eyebrow">Estado actual</p><h3>Métricas</h3></div><span class="text-xs text-muted-foreground">{ignored ? 'Últimos datos guardados' : offline ? 'Última muestra' : 'En tiempo real'}</span></header>
							<AgentMetrics agent={agent} muted={offline || ignored} />
						</section>

						{#if !ignored}
							<section class="agent-panel-card">
								<header class="agent-panel-heading"><div><p class="agent-panel-eyebrow">Reglas automáticas</p><h3>Umbrales de consumo</h3></div></header>
								<p class="agent-panel-copy">El panel registra alertas cuando CPU o RAM supera estos límites. Los avisos externos se configuran por separado.</p>
								<div class="agent-threshold-fields">
									<div class="grid gap-1.5"><Label>CPU % <span class="text-muted-foreground">(10–100)</span></Label><Input bind:value={thrCpu} type="number" min="10" max="100" /></div>
									<div class="grid gap-1.5"><Label>RAM % <span class="text-muted-foreground">(50–100)</span></Label><Input bind:value={thrRam} type="number" min="50" max="100" /></div>
								</div>
								<Button variant="action" size="sm" class="mt-3" onclick={() => void saveThresholds()} disabled={savingThr}>{savingThr ? 'Guardando…' : 'Guardar umbrales'}</Button>
							</section>
							{#if isAdmin}<NotificationPolicyControls scope="agent" scopeId={agent.id} compact />{/if}
						{:else}
							<section class="agent-monitoring-paused"><p class="agent-panel-eyebrow">Monitoreo pausado</p><p>Este contenedor permanece visible, pero no recibe métricas nuevas ni genera alertas.</p></section>
						{/if}

						{#if agent.labels && Object.keys(agent.labels).length > 0}
							<section class="agent-panel-card"><header class="agent-panel-heading"><div><p class="agent-panel-eyebrow">Docker</p><h3>Labels</h3></div></header><div class="flex flex-wrap gap-1.5">{#each Object.entries(agent.labels) as [k, v] (`${k}=${v}`)}<span class="agent-detail-label">{k}: {v}</span>{/each}</div></section>
						{/if}

						{#if isAdmin}
							<section class="agent-monitoring-control">
								<div class="min-w-0"><p class="font-medium">{ignored ? 'Este contenedor está ignorado' : 'Control de monitoreo'}</p><p class="mt-1 text-xs text-muted-foreground">{ignored ? 'El registro y el historial se conservan. Puedes volver a incluirlo en cualquier momento.' : 'Ignorarlo no lo detiene ni lo elimina de Docker; solo pausa la recolección y sus alertas.'}</p></div>
								{#if ignored}
									<Button variant="action" size="sm" onclick={() => void updateMonitoring(false)} disabled={updatingMonitoring}>{updatingMonitoring ? 'Actualizando…' : 'Volver a monitorear'}</Button>
								{:else if ignoreConfirmOpen}
									<div class="agent-ignore-confirm"><span>¿Ignorar {agent.name}?</span><Button variant="ghost" size="sm" onclick={() => (ignoreConfirmOpen = false)} disabled={updatingMonitoring}>Cancelar</Button><Button variant="action" size="sm" onclick={() => void updateMonitoring(true)} disabled={updatingMonitoring}>{updatingMonitoring ? 'Actualizando…' : 'Confirmar'}</Button></div>
								{:else}
									<Button variant="ghost" size="sm" onclick={() => (ignoreConfirmOpen = true)}>Ignorar monitoreo</Button>
								{/if}
							</section>
						{/if}
					</div>
				{:else}
					<section class="agent-panel-card agent-history-panel">
						<header class="agent-panel-heading"><div><p class="agent-panel-eyebrow">Actividad del contenedor</p><h3>Historial y eventos</h3></div><Tabs options={TF_OPTIONS} bind:value={tf} /></header>
						{#if hist.length > 0}
							<div class="agent-history-charts">
								<div class="agent-chart-card"><p class="agent-panel-eyebrow">CPU y memoria</p><UplotChart times={gx} height={220} series={[{ label: 'CPU %', values: cpuData, color: 'hsl(42 85% 52%)', unit: '%', digits: 1, fillAlpha: 0.08 }, { label: 'RAM usada', values: ramUsed, color: 'hsl(187 80% 45%)', unit: 'MB', digits: 1, fillAlpha: 0.08 }, { label: 'RAM límite', values: ramLimit, color: 'hsl(240 8% 56%)', dash: [6, 4], unit: 'MB', digits: 0 }]} /></div>
								<div class="agent-chart-card"><p class="agent-panel-eyebrow">Tráfico de red</p><UplotChart times={gx} height={180} series={[{ label: 'Recibido', values: rxData, color: 'hsl(187 80% 45%)', unit: 'MB', digits: 1, fillAlpha: 0.08 }, { label: 'Enviado', values: txData, color: 'hsl(42 85% 52%)', unit: 'MB', digits: 1 }]} /></div>
							</div>
						{:else}
							<div class="agent-history-empty"><p class="font-medium">Todavía no hay muestras en este período</p><p class="mt-1 text-sm text-muted-foreground">Prueba con una ventana más amplia o vuelve cuando haya nuevos datos.</p></div>
						{/if}
						<div class="agent-alerts-panel">
						<header class="agent-panel-heading"><div><p class="agent-panel-eyebrow">Registro de eventos</p><h3>Alertas del contenedor</h3></div><Badge variant={openAlerts.length ? 'destructive' : 'outline'}>{openAlerts.length} abiertas · {alerts.length} total</Badge></header>
						{#if alerts.length > 0}
							<div class="agent-alert-list">
								{#each pageAlerts as alert (alert.id)}
									<article class="agent-alert-row" class:agent-alert-row--resolved={!!alert.resolved_at}>
										<div class="min-w-0"><div class="flex flex-wrap items-center gap-2"><Badge variant={alert.resolved_at ? 'outline' : 'destructive'}>{alertTypeLabel(alert.type)}</Badge><span class="text-sm font-medium">{fmtAlertMessage(alert.message)}</span></div><p class="mt-2 text-xs text-muted-foreground">{fmtAlertReading(alert.type, alert.value)} · {fmtTs(alert.fired_at)}{alert.resolved_at ? ` · marcada resuelta ${fmtTs(alert.resolved_at)}` : ' · abierta'}</p></div>
										{#if !alert.resolved_at}<Button variant="ghost" size="sm" class="shrink-0" title="Marca este evento como resuelto; las reglas siguen activas" onclick={() => void resolveAlertRow(alert.id)}>Marcar resuelta</Button>{/if}
									</article>
								{/each}
							</div>
							<PaginationControls bind:page={alertPage} pageSize={alertPageSize} total={alerts.length} itemLabel="eventos" />
						{:else}
							<div class="agent-history-empty"><p class="font-medium">Sin alertas registradas</p><p class="mt-1 text-sm text-muted-foreground">Las alertas que se generen aparecerán aquí.</p></div>
						{/if}
						</div>
					</section>
				{/if}
			</div>
		</div>
	{/if}
	{#snippet footer()}
		{#if open}
			<div class="flex items-center justify-end gap-2">
				<Button variant="action" onclick={openFullPage}>Abrir detalle completo</Button>
			</div>
		{/if}
	{/snippet}
</Dialog>
