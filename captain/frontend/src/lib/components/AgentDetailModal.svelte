<script lang="ts">
	import { goto } from '$app/navigation';
	import { toast } from 'svelte-sonner';
	import { agentHistory, putThresholds, listAlerts, resolveAlert } from '#lib/api/client.js';
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
	import { fmtPct, fmtUptime, fmtAgo, fmtTs, alertTypeLabel, fmtAlertMessage, fmtAlertReading } from '#lib/format.js';
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

	const offline = $derived(agent.status !== 'online');
	const isAdmin = $derived(auth.user?.role === 'admin');
	const nowSecs = $state(Math.floor(Date.now() / 1000));

	let tf = $state<TF>('1h');
	let hist = $state<HistoryPoint[]>([]);
	let alerts = $state<NormalizedAlert[]>([]);

	let loadedForId = $state(-1);
	let thrCpu = $state('90');
	let thrRam = $state('95');
	let savingThr = $state(false);

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

	/** La API de alerts no filtra por agent → scope=all + filtro client-side. */
	async function fetchAlerts(id: number): Promise<void> {
		try {
			const res = await listAlerts('all');
			// normalizeAlerts: rows null-safe (value/agent_name nullable, shape v2)
			alerts = normalizeAlerts(res.data).filter((a) => a.agent_id === id);
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
</script>

<Dialog
	bind:open
	title={offline ? `${agent.name} — OFFLINE` : agent.name}
	contentClass="w-[min(96vw,980px)] h-[80vh] max-h-[800px]"
>
	{#if open}
		<div class="flex h-full min-h-0 flex-col gap-3">
			<!-- DOS COLUMNAS: reset compacto (izq) · Historial (der) -->
			<div class="grid min-h-0 flex-1 gap-4 min-[880px]:grid-cols-2">
				<!-- Izquierda: header + métricas + thresholds + labels -->
				<div class="min-h-0">
					<div class="grid gap-4">
						<div>
							<p class="truncate text-[13px] text-muted-foreground">
								{agent.hostname}
								{#if agent.container_id}
									· {agent.container_id.slice(0, 12)}
								{/if}
							</p>
							<div class="mt-1 flex items-center gap-2">
								{#if offline}
									<Badge variant="destructive">OFFLINE</Badge>
									<span class="text-xs text-muted-foreground">
										visto {fmtAgo(agent.last_seen, nowSecs)}
									</span>
								{:else}
									<Badge variant="default">ONLINE</Badge>
									<span class="text-xs text-muted-foreground">
										{fmtAgo(agent.last_seen, nowSecs)} · intervalo {agent.interval}s
									</span>
								{/if}
							</div>
						</div>

						{#if agent.tags && agent.tags.length > 0}
							<div class="flex flex-wrap gap-1">
								{#each agent.tags as tg (tg)}
									<a
										href={`/tags/${encodeURIComponent(tg)}`}
										class="rounded-full border border-secondary/40 bg-secondary/15 px-2 py-px text-[10px] text-secondary transition-colors hover:border-primary/50 hover:bg-primary/10 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
										title={`Ver todas las instancias con tag ${tg}`}
									>
										{tg}
									</a>
								{/each}
							</div>
						{/if}

						<AgentMetrics agent={agent} muted={offline} />

						<!-- thresholds editables inline (misma lógica que /agents/[id]) -->
						<div class="rounded-lg border border-border bg-muted/30 p-3">
							<p class="text-[11px] uppercase tracking-wider text-muted-foreground">
								Umbrales de alerta automáticos
							</p>
							<p class="mt-1 text-xs text-muted-foreground">
				Las alertas CPU/RAM quedan registradas en el panel. Los avisos externos de consumo se controlan abajo.
				Valores iniciales: CPU 90% · RAM 95%.
							</p>
							<div class="mt-2 grid grid-cols-2 gap-3">
								<div class="grid gap-1">
									<Label>CPU % (10–100)</Label>
									<Input bind:value={thrCpu} type="number" min="10" max="100" />
								</div>
								<div class="grid gap-1">
									<Label>RAM % (50–100)</Label>
									<Input bind:value={thrRam} type="number" min="50" max="100" />
								</div>
							</div>
							<Button
								variant="action"
								size="sm"
								class="mt-3"
								onclick={() => void saveThresholds()}
								disabled={savingThr}
							>
								{savingThr ? 'Guardando…' : 'Guardar umbrales'}
							</Button>
						</div>
						{#if isAdmin}
							<NotificationPolicyControls scope="agent" scopeId={agent.id} compact />
						{/if}

						{#if agent.labels && Object.keys(agent.labels).length > 0}
							<div>
								<p class="text-[11px] uppercase tracking-wider text-muted-foreground">Labels</p>
								<div class="mt-1 flex flex-wrap gap-1">
									{#each Object.entries(agent.labels) as [k, v] (`${k}=${v}`)}
										<span
											class="rounded-full border border-secondary/40 bg-secondary/10 px-2 py-px text-[10px] text-secondary"
										>
											{k}: {v}
										</span>
									{/each}
								</div>
							</div>
						{/if}
					</div>
				</div>

				<!-- Derecha: Historial -->
				<div class="min-h-0">
					<div class="grid gap-3">
						<div class="flex items-center justify-between gap-2">
							<h4 class="font-display text-sm font-semibold uppercase tracking-[0.14em]">
								Historial
							</h4>
							<Tabs options={TF_OPTIONS} bind:value={tf} />
						</div>

						<!-- CPU % + RAM MB en un chart (límite dashed si reporta) -->
						<UplotChart
							times={gx}
							height={170}
							series={[
								{
									label: 'CPU %',
									values: cpuData,
									color: 'hsl(42 85% 52%)',
									unit: '%',
									digits: 1,
									fillAlpha: 0.08
								},
								{
									label: 'RAM usada',
									values: ramUsed,
									color: 'hsl(187 80% 45%)',
									unit: 'MB',
									digits: 1,
									fillAlpha: 0.08
								},
								{
									label: 'RAM límite',
									values: ramLimit,
									color: 'hsl(240 8% 56%)',
									dash: [6, 4],
									unit: 'MB',
									digits: 0
								}
							]}
						/>

						<!-- RED mini (rx/tx) -->
						<UplotChart
							times={gx}
							height={110}
							series={[
								{ label: 'Rx recibida', values: rxData, color: 'hsl(187 80% 45%)', unit: 'MB', digits: 1, fillAlpha: 0.08 },
								{ label: 'Tx enviada', values: txData, color: 'hsl(42 85% 52%)', unit: 'MB', digits: 1 }
							]}
						/>

						<!-- Timeline de alertas del agent -->
						<div class="rounded-lg border border-border bg-muted/30 p-3">
							<div class="flex items-center justify-between gap-2">
								<p class="text-[11px] uppercase tracking-wider text-muted-foreground">
									Alertas — {openAlerts.length} abierta(s) de {alerts.length}
								</p>
								<span class="text-[10px] tabular-nums text-muted-foreground">
									visto {fmtAgo(agent.last_seen, nowSecs)} · uptime {fmtUptime(agent.metrics?.uptime_sec)}
								</span>
							</div>
							<div class="mt-2 grid gap-2">
								{#each alerts as alert (alert.id)}
									<div
										class="flex items-start justify-between gap-3 border-b border-border/60 pb-2 last:border-b-0"
									>
										<div class="min-w-0">
											<div class="flex items-center gap-2">
												<Badge variant={alert.resolved_at ? 'outline' : 'destructive'}>
													{alertTypeLabel(alert.type)}
												</Badge>
												<span class="truncate text-[13px] text-muted-foreground">
														{fmtAlertMessage(alert.message)}
												</span>
											</div>
											<p class="mt-0.5 text-[11px] text-muted-foreground">
								{fmtAlertReading(alert.type, alert.value)} ·
												{fmtTs(alert.fired_at)}
												{alert.resolved_at ? ' · resuelta ' + fmtTs(alert.resolved_at) : ' · abierta'}
											</p>
										</div>
										{#if !alert.resolved_at}
													<Button
														variant="action"
														class="h-7 shrink-0 px-2 text-xs"
														title="Cierra este registro; no desactiva las reglas automáticas"
														onclick={() => void resolveAlertRow(alert.id)}
													>
														Cerrar alerta
													</Button>
										{/if}
									</div>
								{:else}
									<p class="text-[13px] text-muted-foreground">
										Sin alertas registradas para este agent.
									</p>
								{/each}
							</div>
						</div>
					</div>
				</div>
			</div>
		</div>
	{/if}
	{#snippet footer()}
		{#if open}
			<div class="flex items-center justify-end gap-2">
				<Button variant="action" onclick={() => (open = false)}>Cerrar</Button>
				<Button variant="action" onclick={openFullPage}>Ver página completa</Button>
			</div>
		{/if}
	{/snippet}
</Dialog>
