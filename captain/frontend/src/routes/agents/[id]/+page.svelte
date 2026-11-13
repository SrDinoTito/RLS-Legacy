<script lang="ts">
	import { page } from '$app/state';
	import { toast } from 'svelte-sonner';
	import {
		listAgents,
		agentHistory,
		putThresholds,
		listAgentAlerts,
		resolveAlert
	} from '#lib/api/client.js';
	import type { Agent, HistoryPoint } from '#lib/api/types.js';
	import { normalizeAlerts, type NormalizedAlert } from '#lib/alerts.js';
	import AgentMetrics from '#lib/components/AgentMetrics.svelte';
	import { sse } from '#lib/stores/sse.svelte.js';
	import { auth } from '#lib/stores/auth.svelte.js';
	import NotificationPolicyControls from '#lib/components/NotificationPolicyControls.svelte';
	import UplotChart from '#lib/components/UplotChart.svelte';
	import { Button } from '#lib/components/ui/button/index.js';
	import { Input } from '#lib/components/ui/input/index.js';
	import { Tabs } from '#lib/components/ui/tabs/index.js';
	import { Badge } from '#lib/components/ui/badge/index.js';
	import PaginationControls from '#lib/components/PaginationControls.svelte';
	import {
		Card,
		CardContent,
		CardDescription,
		CardHeader,
		CardTitle
	} from '#lib/components/ui/card/index.js';
	import { fmtPct, fmtMb, fmtBytesUnitMb, fmtDisk, fmtUptime, fmtTs, alertTypeLabel, fmtAlertMessage, fmtAlertReading } from '#lib/format.js';

	/*
	 * Detalle de agent (REQ-022): gráficas CPU/RAM/RED (uPlot), selector de
	 * timeframe 1h/6h/24h → from/to/step referenciales, labels del agent,
	 * thresholds editables inline (PUT bankable 10..100 / 50..100) y timeline
	 * de alertas con resolver (409 → toast "ya resuelta").
	 */

	type TF = '1h' | '6h' | '24h';
	/** Ventanas referenciales: span en s y step del backend (≥30 s, ≤2000 buckets). */
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

	const idNum = $derived(Number(page.params.id) || 0);
	const isAdmin = $derived(auth.user?.role === 'admin');

	let agent = $state<Agent | null>(null);
	let hist = $state<HistoryPoint[]>([]);
	let tf = $state<TF>('1h');
	let alerts = $state<NormalizedAlert[]>([]);
	let loadingHist = $state(false);
	const alertPageSize = 8;
	let alertPage = $state(0);

	let thrCpu = $state('90');
	let thrRam = $state('95');
	let savingThr = $state(false);

	function loadAgent(): void {
		void (async () => {
			const res = await listAgents();
			const found = res.data?.find((a) => a.id === idNum) ?? null;
			agent = found;
			if (found) {
				thrCpu = String(found.thresholds_cpu ?? 90);
				thrRam = String(found.thresholds_ram ?? 95);
			}
		})();
	}

	async function loadHistory(): Promise<void> {
		if (!idNum) return;
		loadingHist = true;
		try {
			const now = Math.floor(Date.now() / 1000);
			const { span, step } = WINDOW[tf];
			const res = await agentHistory(idNum, now - span, now, step);
			hist = res.data?.points ?? [];
		} finally {
			loadingHist = false;
		}
	}

	function loadAlerts(): void {
		void (async () => {
			const res = await listAgentAlerts(idNum, 'all');
			alerts = normalizeAlerts(res.data);
		})();
	}

	// carga inicial + refetch live: cada evento SSE (tick) repasa agent/alertas
	// (loadHistory va aparte: reacciona a tf Y a tick para head-of-series fresco)
	$effect(() => {
		void sse.eventTick;
		loadAgent();
		loadAlerts();
		void loadHistory();
	});
	$effect(() => {
		void tf;
		void loadHistory();
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
			const res = await putThresholds(idNum, { thresholds_cpu: cpu, thresholds_ram: ram });
			if (res.ok) {
				toast.success('Umbrales guardados', { description: `CPU ${cpu}% · RAM ${ram}%` });
				loadAgent();
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
				loadAlerts();
			} else if (res.status === 409) {
				toast.warning('La alerta ya estaba cerrada');
				loadAlerts();
			} else {
				toast.error(`No se pudo cerrar (${res.status})`);
			}
		} catch {
			toast.error('Backend no disponible');
		}
	}

	/* ------------------------------ series para uPlot ------------------------------ */
	const gx = $derived(hist.map((p) => p.ts));
	const cpuData = $derived(hist.map((p) => p.cpu_pct));
	const ramUsed = $derived(hist.map((p) => p.ram_used_mb));
	const ramLimit = $derived(hist.map((p) => p.ram_limit_mb));
	const rxData = $derived(hist.map((p) => p.rx_mb));
	const txData = $derived(hist.map((p) => p.tx_mb));
	const hasData = $derived(hist.length > 0);
	const pageAlerts = $derived(alerts.slice(alertPage * alertPageSize, (alertPage + 1) * alertPageSize));
</script>

<svelte:head>
	<title>Agent #{idNum} — RLS Legacy</title>
</svelte:head>

<div class="grid gap-6">
	<header class="flex flex-wrap items-center justify-between gap-4">
		<div>
			<div class="flex items-center gap-3">
				<h1 class="page-title">
					{agent?.name ?? `Agent #${idNum}`}
				</h1>
				{#if agent?.status !== 'online'}
					<Badge variant="destructive">OFFLINE</Badge>
				{:else}
					<Badge variant="default">ONLINE</Badge>
				{/if}
			</div>
			{#if agent}
				<p class="text-sm text-muted-foreground">
					{agent.hostname}
					{#if agent.container_id}
						· {agent.container_id.slice(0, 16)}
					{/if}
					· intervalo {agent.interval}s · uptime {fmtUptime(agent.metrics?.uptime_sec)}
					<!-- Almacenamiento (explorer v4 disk): free prominente + total tenue -->
					{#if agent.disk_free_mb != null}
						· <span class="text-foreground">almacenamiento {fmtBytesUnitMb(agent.disk_free_mb)}</span>
						{#if agent.disk_total_mb != null && agent.disk_total_mb > 0}
							<span> / {fmtBytesUnitMb(agent.disk_total_mb)}</span>
						{/if}
					{:else}
						· almacenamiento {fmtDisk(agent.disk_free_mb, agent.disk_total_mb)}
					{/if}
				</p>
			{/if}
		</div>
		<Tabs options={TF_OPTIONS} bind:value={tf} />
	</header>

	{#if agent && ((agent.labels && Object.keys(agent.labels).length > 0) || (agent.tags && agent.tags.length > 0))}
		<!-- labels + tags del agent (cian holográfico) -->
		<div class="flex flex-wrap gap-2">
			{#each Object.entries(agent.labels ?? {}) as [k, v]}
				<span
					class="rounded-full border border-secondary/40 bg-secondary/10 px-2.5 py-0.5 text-xs text-secondary"
				>
					{k}: {v}
				</span>
			{/each}
			{#each agent.tags ?? [] as tg (tg)}
				<a
					href={`/tags/${encodeURIComponent(tg)}`}
					class="rounded-full border border-secondary/40 bg-secondary/15 px-2.5 py-0.5 text-xs text-secondary transition-colors hover:border-primary/50 hover:bg-primary/10 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
					title={`Ver todas las instancias con tag ${tg}`}
				>
					{tg}
				</a>
			{/each}
		</div>
	{/if}

	{#if agent}
		<AgentMetrics {agent} muted={agent.status !== 'online'} />
	{/if}
	{#if agent && isAdmin}
		<NotificationPolicyControls scope="agent" scopeId={agent.id} />
	{/if}

	<!-- Gráficas (uPlot ~45 KB; huecos nativos porque los buckets vacíos NO existen) -->
	<div class="grid gap-6 xl:grid-cols-2">
		<Card>
			<CardHeader class="flex-row items-start justify-between gap-3 space-y-0">
				<div>
					<CardTitle class="text-base">CPU %</CardTitle>
					<CardDescription>promedio por bucket · escala fija 0–100</CardDescription>
				</div>
				<p class="text-2xl font-medium tabular-nums text-primary">
					{fmtPct(agent?.metrics?.cpu_pct)}
				</p>
			</CardHeader>
			<CardContent>
				<UplotChart
					times={gx}
					series={[
						{
							label: 'CPU %',
							values: cpuData,
							color: 'hsl(42 85% 52%)',
							unit: '%',
							fillAlpha: 0.08
						}
					]}
					yMin={0}
					yMax={100}
				/>
			</CardContent>
		</Card>

		<Card>
			<CardHeader class="flex-row items-start justify-between gap-3 space-y-0">
				<div>
					<CardTitle class="text-base">RAM MB</CardTitle>
					<CardDescription>MAX usado por bucket + límite reportado</CardDescription>
				</div>
				<p class="text-2xl font-medium tabular-nums">
					{fmtMb(agent?.metrics?.ram_used_mb)}
					{#if agent?.metrics?.ram_limit_mb}
						<span class="text-sm text-muted-foreground">/ {agent.metrics.ram_limit_mb.toFixed(0)} MB</span>
					{/if}
				</p>
			</CardHeader>
			<CardContent>
				<UplotChart
					times={gx}
					series={[
						{
							label: 'Usada',
							values: ramUsed,
							color: 'hsl(187 80% 45%)',
							unit: 'MB',
							digits: 0,
							fillAlpha: 0.08
						},
						{
							label: 'Límite',
							values: ramLimit,
							color: 'hsl(240 8% 56%)',
							dash: [6, 4],
							unit: 'MB',
							digits: 0
						}
					]}
				/>
			</CardContent>
		</Card>

		<Card>
			<CardHeader>
				<CardTitle class="text-base">RED (rx/tx MB acum.)</CardTitle>
				<CardDescription>MAX de rx/tx por bucket desde el arranque del agent</CardDescription>
			</CardHeader>
			<CardContent>
				<UplotChart
					times={gx}
					series={[
						{ label: 'Rx ↓', values: rxData, color: 'hsl(187 80% 45%)', unit: 'MB', digits: 1, fillAlpha: 0.08 },
						{ label: 'Tx ↑', values: txData, color: 'hsl(42 85% 52%)', unit: 'MB', digits: 1 }
					]}
				/>
			</CardContent>
		</Card>

		<!-- Thresholds inline (PUT; validación 10..100 / 50..100) -->
		<Card>
			<CardHeader>
				<CardTitle class="text-base">Umbrales de alerta</CardTitle>
				<CardDescription>
					CPU y RAM generan alertas del panel al superar estos umbrales. El envío externo se activa por separado abajo.
					Valores iniciales: CPU 90% y RAM 95%. CPU compara el porcentaje; RAM compara uso sobre límite.
				</CardDescription>
			</CardHeader>
			<CardContent class="grid gap-4">
				<div class="grid grid-cols-2 gap-4">
					<div class="grid gap-1.5">
						<p class="text-xs text-muted-foreground">
							CPU % (10–100) · actual {fmtPct(agent?.thresholds_cpu)}
						</p>
						<Input bind:value={thrCpu} type="number" min="10" max="100" />
					</div>
					<div class="grid gap-1.5">
						<p class="text-xs text-muted-foreground">
							RAM % (50–100) · actual {fmtPct(agent?.thresholds_ram)}
						</p>
						<Input bind:value={thrRam} type="number" min="50" max="100" />
					</div>
				</div>
				<div>
					<Button onclick={() => void saveThresholds()} disabled={savingThr}>
						{savingThr ? 'Guardando…' : 'Guardar umbrales'}
					</Button>
				</div>
			</CardContent>
		</Card>

		<!-- Alertas y métricas forman una sola vista del historial de actividad. -->
		<Card class="xl:col-span-2">
		<CardHeader>
			<CardTitle class="text-base">Historial y eventos del contenedor</CardTitle>
			<CardDescription>
				{alerts.filter((a) => !a.resolved_at).length} alerta(s) abierta(s) de {alerts.length}; se muestran junto al historial de métricas.
				{#if loadingHist && !hasData}
					· cargando historia…
				{/if}
			</CardDescription>
		</CardHeader>
		<CardContent class="grid gap-3">
			<div class="grid gap-2">
			{#each pageAlerts as alert (alert.id)}
				<div
					class="flex items-start justify-between gap-4 border-b border-border/60 pb-2 last:border-b-0"
				>
					<div class="min-w-0">
						<div class="flex items-center gap-2">
							<Badge variant={alert.resolved_at ? 'outline' : 'destructive'}>
								{alertTypeLabel(alert.type)}
							</Badge>
							<span class="truncate text-sm text-muted-foreground">
								{fmtAlertMessage(alert.message)}
							</span>
						</div>
						<p class="mt-0.5 text-xs text-muted-foreground">
							{fmtAlertReading(alert.type, alert.value)} ·
							{fmtTs(alert.fired_at)}
							{alert.resolved_at ? ' · resuelta ' + fmtTs(alert.resolved_at) : ' · abierta'}
						</p>
					</div>
					{#if !alert.resolved_at}
						<Button
							variant="ghost"
							title="Cierra este registro; no desactiva las reglas automáticas"
							onclick={() => void resolveAlertRow(alert.id)}
						>
							Cerrar alerta
						</Button>
					{/if}
				</div>
			{:else}
				<p class="text-sm text-muted-foreground">Sin alertas registradas para este agent.</p>
			{/each}
			</div>
			<PaginationControls bind:page={alertPage} pageSize={alertPageSize} total={alerts.length} itemLabel="eventos" />
		</CardContent>
		</Card>
	</div>
</div>
